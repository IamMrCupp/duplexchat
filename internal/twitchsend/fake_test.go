package twitchsend

// The fake Twitch of test/twitch-send.test.js at 29a9d2c, with real token
// bookkeeping: single-use refresh tokens, access tokens that must match, and
// a chat endpoint the test can script. No real Twitch involved.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

const (
	clientID = "fakeclientid0123456789abcdefgh"
	verify   = "https://www.twitch.tv/activate?public=true&device-code=ABCDEFGH"
)

type call struct {
	key, method, path string
	query             url.Values
	rawQuery          string
	header            http.Header
	body              map[string]any // a form's fields, or the JSON object
	raw               string
}

type reply struct {
	status int
	body   any // nil: no body
	header map[string]string
}

type fakeTwitch struct {
	srv *httptest.Server

	mu          sync.Mutex
	n           int
	access      string
	refresh     string
	pendingLeft int
	deny        string
	asked       string
	chat, users func(c call) reply
	streams     func(c call) reply
	revoked     []string
	calls       []call
	// a refresh answer is held until hold is closed; seen is closed when one arrives
	hold, seen chan struct{}
	// a refused refresh waits for this before it's answered
	beforeRefusal func()
	// a device-code grant's tokens are held until grantHold is closed;
	// grantSeen is closed when one is issued
	grantHold, grantSeen chan struct{}
	// device scripts POST /oauth2/device
	device func() reply
	// the signed-in user's channel, for stream info
	channel map[string]any
	patch   func(c call) reply
	search  func(c call) reply
	patches []call
}

func newFake(t *testing.T, pendingPolls int) *fakeTwitch {
	f := &fakeTwitch{pendingLeft: pendingPolls, asked: "user:write:chat",
		channel: map[string]any{"broadcaster_id": "1001", "broadcaster_login": "example_user", "broadcaster_name": "Example_User", "game_id": "100", "game_name": "Old Category", "title": "Old title", "tags": []any{"old"}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTwitch) base() string { return f.srv.URL }

func (f *fakeTwitch) state(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeTwitch) issue() reply {
	f.n++
	f.access = fmt.Sprintf("FAKE_ACCESS_%d", f.n)
	f.refresh = fmt.Sprintf("FAKE_REFRESH_%d", f.n)
	scope := []any{}
	for _, s := range strings.Split(f.asked, " ") {
		scope = append(scope, s)
	}
	return reply{200, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": 14400, "scope": scope, "token_type": "bearer"}, nil}
}

func (f *fakeTwitch) authed(c call) bool {
	return c.header.Get("authorization") == "Bearer "+f.access && c.header.Get("client-id") == clientID
}

var invalid = reply{401, map[string]any{"status": 401, "message": "Invalid OAuth token"}, nil}

func (f *fakeTwitch) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c := call{key: r.Method + " " + r.URL.Path, method: r.Method, path: r.URL.Path, query: r.URL.Query(), rawQuery: r.URL.RawQuery, header: r.Header, raw: string(b), body: map[string]any{}}
	ct := r.Header.Get("content-type")
	switch {
	case strings.Contains(ct, "json"):
		json.Unmarshal(b, &c.body)
	case strings.Contains(ct, "x-www-form-urlencoded"):
		q, _ := url.ParseQuery(string(b))
		for k := range q {
			c.body[k] = q.Get(k)
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var rep reply
	var hold chan struct{}
	var wait func()
	switch c.key {
	case "POST /oauth2/device":
		f.asked, _ = c.body["scopes"].(string)
		rep = reply{200, map[string]any{"device_code": "FAKE_DEVICE_CODE", "expires_in": 1800, "interval": 5, "user_code": "ABCDEFGH", "verification_uri": verify}, nil}
		if f.device != nil {
			rep = f.device()
		}
	case "POST /oauth2/token":
		switch {
		case c.body["grant_type"] == "refresh_token":
			if f.seen != nil {
				close(f.seen)
				f.seen = nil
			}
			if c.body["refresh_token"] != f.refresh {
				rep = reply{400, map[string]any{"status": 400, "message": "Invalid refresh token"}, nil}
				wait = f.beforeRefusal
			} else {
				rep = f.issue()
				hold = f.hold
			}
		case f.deny != "":
			rep = reply{400, map[string]any{"status": 400, "message": f.deny}, nil}
		case f.pendingLeft > 0:
			f.pendingLeft--
			rep = reply{400, map[string]any{"status": 400, "message": "authorization_pending"}, nil}
		default:
			rep = f.issue()
			hold = f.grantHold
			if f.grantSeen != nil {
				close(f.grantSeen)
				f.grantSeen = nil
			}
		}
	case "POST /oauth2/revoke":
		f.revoked = append(f.revoked, c.body["token"].(string))
		rep = reply{200, nil, nil}
	case "GET /helix/users":
		switch {
		case !f.authed(c):
			rep = invalid
		case f.users != nil:
			rep = f.users(c)
		case c.query.Get("login") != "":
			rep = reply{200, map[string]any{"data": []any{map[string]any{"id": "9001", "login": c.query.Get("login"), "display_name": c.query.Get("login")}}}, nil}
		default:
			rep = reply{200, map[string]any{"data": []any{map[string]any{"id": "1001", "login": "example_user", "display_name": "Example_User"}}}, nil}
		}
	case "GET /helix/streams":
		switch {
		case !f.authed(c):
			rep = invalid
		case f.streams != nil:
			rep = f.streams(c)
		default:
			rep = reply{200, map[string]any{"data": []any{map[string]any{"user_login": "examplechannel", "type": "live", "viewer_count": 42, "started_at": "2026-11-20T10:00:00Z"}}}, nil}
		}
	case "POST /helix/chat/messages":
		switch {
		case !f.authed(c):
			rep = invalid
		case f.chat != nil:
			rep = f.chat(c)
		default:
			rep = reply{200, map[string]any{"data": []any{map[string]any{"message_id": "fake-msg-1", "is_sent": true, "drop_reason": nil}}}, nil}
		}
	case "GET /helix/channels":
		switch {
		case !f.authed(c):
			rep = invalid
		case c.query.Get("broadcaster_id") == f.channel["broadcaster_id"]:
			rep = reply{200, map[string]any{"data": []any{f.channel}}, nil}
		default:
			rep = reply{200, map[string]any{"data": []any{}}, nil}
		}
	case "PATCH /helix/channels":
		switch {
		case !f.authed(c):
			rep = invalid
		default:
			f.patches = append(f.patches, c)
			switch {
			case f.patch != nil:
				rep = f.patch(c)
			case !strings.Contains(f.asked, "channel:manage:broadcast"):
				rep = reply{401, map[string]any{"status": 401, "message": "Missing scope: channel:manage:broadcast or user:edit:broadcast"}, nil}
			default:
				rep = reply{204, nil, nil}
			}
		}
	case "GET /helix/search/categories":
		switch {
		case !f.authed(c):
			rep = invalid
		case f.search != nil:
			rep = f.search(c)
		default:
			rep = reply{200, map[string]any{"data": []any{
				map[string]any{"id": "509670", "name": "Example Category", "box_art_url": "https://static-cdn.jtvnw.net/ttv-boxart/509670-52x72.jpg"},
				map[string]any{"name": "no id"},
			}, "pagination": map[string]any{}}, nil}
		}
	default:
		rep = reply{404, map[string]any{"status": 404, "message": "no such fake route"}, nil}
	}
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if wait != nil {
		wait()
	}
	w.Header().Set("content-type", "application/json")
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.status)
	if rep.body != nil {
		json.NewEncoder(w).Encode(rep.body)
	}
}

func (f *fakeTwitch) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.key == key {
			n++
		}
	}
	return n
}

func (f *fakeTwitch) find(pred func(c call) bool) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTwitch) refreshes() int {
	return len(f.find(func(c call) bool { return c.body["grant_type"] == "refresh_token" }))
}

func (f *fakeTwitch) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTwitch) get(fn func() string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fn()
}

// clock is the test's Date.now.
type clock struct{ ms atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ms.Store(time.Now().UnixMilli())
	return c
}

func (c *clock) now() time.Time      { return time.UnixMilli(c.ms.Load()) }
func (c *clock) add(d time.Duration) { c.ms.Add(d.Milliseconds()) }

type config struct {
	mu sync.Mutex
	id string
}

func (c *config) get() string  { c.mu.Lock(); defer c.mu.Unlock(); return c.id }
func (c *config) set(s string) { c.mu.Lock(); c.id = s; c.mu.Unlock() }

type rig struct {
	sender *Sender
	file   string
	cfg    *config
	clock  *clock
}

func makeSender(t *testing.T, f *fakeTwitch, edit ...func(*Options)) *rig {
	file := filepath.Join(t.TempDir(), "secrets.json")
	r := &rig{file: file, cfg: &config{id: clientID}, clock: newClock()}
	o := Options{
		Store: secrets.New(file, secrets.Options{}), ClientID: r.cfg.get, SetClientID: r.cfg.set,
		IDBase: f.base(), APIBase: f.base(), Now: r.clock.now, PollInterval: 5 * time.Millisecond,
	}
	for _, e := range edit {
		e(&o)
	}
	r.sender = New(o)
	t.Cleanup(func() {
		r.sender.mu.Lock()
		r.sender.stopPending()
		r.sender.mu.Unlock()
	})
	return r
}

// saved is the twitch section on disk, or nil.
func (r *rig) saved(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	return all["twitch"]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func setup(t *testing.T, s *Sender, action string, body string) (int, map[string]any) {
	t.Helper()
	if body == "" {
		body = "{}"
	}
	v, err := jsjson.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Setup(t.Context(), action, v.(*jsjson.Object), 3131)
	if err != nil {
		t.Fatalf("setup %s: %v", action, err)
	}
	if r == nil {
		return 0, nil
	}
	var out map[string]any
	if r.Body != nil {
		json.Unmarshal(jsjson.Stringify(r.Body, ""), &out)
	}
	return r.Status, out
}

func signIn(t *testing.T, s *Sender) {
	t.Helper()
	if code, body := setup(t, s, "signin", ""); code != 200 {
		t.Fatalf("signin: %d %v", code, body)
	}
	waitFor(t, "sign-in", func() bool { return s.Status().SignedIn })
}

func state(t *testing.T, s *Sender) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(jsjson.Stringify(s.SetupState(3131), ""), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func channel(c string) targets.Target { return targets.Target{Channel: &c} }

var target = channel("examplechannel")
