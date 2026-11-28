package youtubesend

// The fake Google of test/youtube-send.test.js at 29a9d2c, and the app the
// sender is mounted in. No real Google involved.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/oauth"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

const (
	clientID     = "000000000000-fakefakefakefake.apps.googleusercontent.com"
	clientSecret = "FAKE_CLIENT_SECRET_do_not_use"
	video        = "FAKEvideo01"
	channelID    = "UCfakefakefakefakefakefa"
)

func vid(v string) targets.Target { return targets.Target{VideoID: &v} }

var target = vid(video)

type call struct {
	key, method, path string
	query             url.Values
	header            http.Header
	body              map[string]any
	raw               string
}

type reply struct {
	status int
	body   any
}

func gErr(status int, reason, message string) reply {
	return reply{status, map[string]any{"error": map[string]any{"code": status, "message": message,
		"errors": []any{map[string]any{"domain": "youtube.liveChat", "reason": reason, "message": message}}}}}
}

type fakeGoogle struct {
	srv *httptest.Server

	mu                   sync.Mutex
	n                    int
	access, refresh      string
	code                 string
	verifier             string
	customURL            string
	noRefresh, noChannel bool
	insert, video        func(c call) reply
	revoked              []string
	calls                []call
	hold, seen           chan struct{} // a refresh answer waits for hold; seen closes when one arrives
	// a code exchange's answer waits for exchangeHold; exchangeSeen closes when one arrives
	exchangeHold, exchangeSeen chan struct{}
}

func newGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{code: "FAKE_AUTH_CODE"}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) endpoints() Endpoints {
	return Endpoints{Auth: "https://accounts.google.com/o/oauth2/v2/auth", Token: g.srv.URL + "/token", Revoke: g.srv.URL + "/revoke", API: g.srv.URL + "/youtube/v3"}
}

func (g *fakeGoogle) state(fn func()) { g.mu.Lock(); defer g.mu.Unlock(); fn() }

func (g *fakeGoogle) get(fn func() string) string { g.mu.Lock(); defer g.mu.Unlock(); return fn() }

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c := call{key: r.Method + " " + r.URL.Path, method: r.Method, path: r.URL.Path, query: r.URL.Query(), header: r.Header, raw: string(b), body: map[string]any{}}
	switch ct := r.Header.Get("content-type"); {
	case strings.Contains(ct, "json"):
		json.Unmarshal(b, &c.body)
	case strings.Contains(ct, "x-www-form-urlencoded"):
		q, _ := url.ParseQuery(string(b))
		for k := range q {
			c.body[k] = q.Get(k)
		}
	}
	g.mu.Lock()
	g.calls = append(g.calls, c)
	authed := r.Header.Get("authorization") == "Bearer "+g.access
	var rep reply
	var hold chan struct{}
	switch c.key {
	case "POST /token":
		if c.body["grant_type"] == "refresh_token" {
			if g.seen != nil {
				close(g.seen)
				g.seen = nil
			}
			if c.body["refresh_token"] != g.refresh {
				rep = reply{400, map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."}}
				break
			}
			g.n++
			g.access = fmt.Sprintf("FAKE_YT_ACCESS_%d", g.n)
			rep = reply{200, map[string]any{"access_token": g.access, "expires_in": 3599, "scope": Scope, "token_type": "Bearer"}}
			hold = g.hold
			break
		}
		if c.body["code"] != g.code {
			rep = reply{400, map[string]any{"error": "invalid_grant", "error_description": "Bad Request"}}
			break
		}
		g.verifier, _ = c.body["code_verifier"].(string)
		hold = g.exchangeHold
		if g.exchangeSeen != nil {
			close(g.exchangeSeen)
			g.exchangeSeen = nil
		}
		g.n++
		g.access = fmt.Sprintf("FAKE_YT_ACCESS_%d", g.n)
		g.refresh = fmt.Sprintf("FAKE_YT_REFRESH_%d", g.n)
		body := map[string]any{"access_token": g.access, "expires_in": 3599, "token_type": "Bearer"}
		if !g.noRefresh {
			body["refresh_token"] = g.refresh
		}
		rep = reply{200, body}
	case "POST /revoke":
		g.revoked = append(g.revoked, c.body["token"].(string))
		rep = reply{200, map[string]any{}}
	case "GET /youtube/v3/channels":
		switch {
		case !authed:
			rep = gErr(401, "authError", "Invalid Credentials")
		case g.noChannel:
			rep = reply{200, map[string]any{"items": []any{}}}
		default:
			snippet := map[string]any{"title": "Example Channel"}
			if g.customURL != "" {
				snippet["customUrl"] = g.customURL
			}
			rep = reply{200, map[string]any{"items": []any{map[string]any{"id": channelID, "snippet": snippet}}}}
		}
	case "GET /youtube/v3/videos":
		switch {
		case !authed:
			rep = gErr(401, "authError", "Invalid Credentials")
		case g.video != nil:
			rep = g.video(c)
		default:
			rep = reply{200, map[string]any{"items": []any{map[string]any{"id": c.query.Get("id"), "liveStreamingDetails": map[string]any{"activeLiveChatId": "FAKE_LIVE_CHAT_ID"}}}}}
		}
	case "POST /youtube/v3/liveChat/messages":
		switch {
		case !authed:
			rep = gErr(401, "authError", "Invalid Credentials")
		case g.insert != nil:
			rep = g.insert(c)
		default:
			rep = reply{200, map[string]any{"kind": "youtube#liveChatMessage", "id": "fake-yt-msg-1", "snippet": c.body["snippet"]}}
		}
	default:
		rep = reply{404, map[string]any{"status": 404, "message": "no such fake route"}}
	}
	g.mu.Unlock()
	if hold != nil {
		<-hold
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(rep.status)
	if rep.body != nil {
		json.NewEncoder(w).Encode(rep.body)
	}
}

func (g *fakeGoogle) find(pred func(c call) bool) []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []call
	for _, c := range g.calls {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func (g *fakeGoogle) count(key string) int {
	return len(g.find(func(c call) bool { return c.key == key }))
}

func (g *fakeGoogle) total() int { return len(g.find(func(call) bool { return true })) }

// apiCalls are the YouTube Data API calls.
func (g *fakeGoogle) apiCalls() []call {
	return g.find(func(c call) bool { return strings.HasPrefix(c.path, "/youtube/v3/") })
}

func (g *fakeGoogle) refreshes() []call {
	return g.find(func(c call) bool { return c.key == "POST /token" && c.body["grant_type"] == "refresh_token" })
}

type clock struct{ ms atomic.Int64 }

func (c *clock) now() time.Time      { return time.UnixMilli(c.ms.Load()) }
func (c *clock) add(d time.Duration) { c.ms.Add(d.Milliseconds()) }

// app is the send API on a real port, the way the server mounts it.
type app struct {
	port   int
	origin string
	srv    *httptest.Server
}

type response struct {
	status int
	header http.Header
	text   string
	json   map[string]any
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func startApp(t *testing.T, edit func(*server.Options)) *app {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	o := server.Options{Pages: fstest.MapFS{}, Port: port, Token: "tok-123", Targets: fixedTargets}
	edit(&o)
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: server.New(o)}}
	srv.Start()
	t.Cleanup(srv.Close)
	return &app{port: port, origin: fmt.Sprintf("http://127.0.0.1:%d", port), srv: srv}
}

func fixedTargets() map[string]targets.Target {
	ch, yt, v := "examplechannel", "Example Channel", video
	return map[string]targets.Target{
		"twitch": {Pinned: true, SourceID: "aaaa1111", Channel: &ch, Label: "#examplechannel", Key: "aaaa1111:examplechannel",
			Choices: []targets.Choice{{ID: "aaaa1111", Label: "#examplechannel"}}},
		"youtube": {Pinned: true, SourceID: "bbbb2222", VideoID: &v, Channel: &yt, Label: "Example Channel · video FAKEvideo01", Key: "bbbb2222:FAKEvideo01",
			Choices: []targets.Choice{{ID: "bbbb2222", Label: "Example Channel"}}},
	}
}

// request sends exactly these headers ("host" sets Host).
func (a *app) request(t *testing.T, method, path, body string, headers map[string]string) response {
	t.Helper()
	req, _ := http.NewRequest(method, a.origin+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	for k, v := range headers {
		if k == "host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	out := response{status: res.StatusCode, header: res.Header, text: string(b)}
	json.Unmarshal(b, &out.json)
	return out
}

// post is a well-formed request from the dock page; /api/send gets expect
// filled in as the dock does.
func (a *app) post(t *testing.T, path string, body map[string]any, headers map[string]string) response {
	t.Helper()
	if path == "/api/send" {
		if _, has := body["expect"]; !has {
			body["expect"] = map[string]any{"twitch": "aaaa1111:examplechannel", "youtube": "bbbb2222:FAKEvideo01"}
		}
	}
	h := map[string]string{"origin": a.origin, "x-dock-token": "tok-123"}
	for k, v := range headers {
		h[k] = v
	}
	b, _ := json.Marshal(body)
	return a.request(t, "POST", path, string(b), h)
}

func (a *app) get(t *testing.T, path string) response {
	t.Helper()
	return a.request(t, "GET", path, "", map[string]string{"x-dock-token": "tok-123"})
}

// rig is the source's setup(): a fake Google, a sender, an app, a client.
type rig struct {
	g      *fakeGoogle
	sender *Sender
	app    *app
	file   string
	clock  *clock

	hookMu sync.Mutex
	hook   func()
}

func setup(t *testing.T, signIn bool) *rig {
	t.Helper()
	r := &rig{g: newGoogle(t), file: filepath.Join(t.TempDir(), "secrets.json"), clock: &clock{}}
	r.clock.ms.Store(time.Now().UnixMilli())
	r.sender = New(Options{
		Store: secrets.New(r.file, secrets.Options{}), Endpoints: r.g.endpoints(), Now: r.clock.now,
		OnAuthChange: func() {
			r.hookMu.Lock()
			h := r.hook
			r.hookMu.Unlock()
			if h != nil {
				h()
			}
		},
	})
	r.app = startApp(t, func(o *server.Options) { o.Senders = map[string]server.Sender{"youtube": r.sender} })
	if res := r.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": clientSecret}, nil); res.status != 200 {
		t.Fatalf("client: %d %s", res.status, res.text)
	}
	if signIn {
		r.signIn(t)
	}
	return r
}

// begin is the setup page's sign-in button: the URL the browser goes to.
func (r *rig) begin(t *testing.T) *url.URL {
	t.Helper()
	res := r.app.post(t, "/api/setup/youtube/signin", map[string]any{}, nil)
	if res.status != 200 {
		t.Fatalf("signin: %d %s", res.status, res.text)
	}
	u, err := url.Parse(res.json["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// comeBack is the browser landing on the redirect URI.
func (r *rig) comeBack(t *testing.T, headers map[string]string, params ...string) response {
	t.Helper()
	var ps []oauth.Param
	for i := 0; i+1 < len(params); i += 2 {
		ps = append(ps, oauth.Param{Key: params[i], Value: params[i+1]})
	}
	return r.app.request(t, "GET", "/?"+oauth.Form(ps), "", headers)
}

func (r *rig) code() string { return r.g.get(func() string { return r.g.code }) }

func (r *rig) signIn(t *testing.T) *url.URL {
	t.Helper()
	auth := r.begin(t)
	if res := r.comeBack(t, nil, "state", auth.Query().Get("state"), "code", r.code()); res.status != 302 {
		t.Fatalf("callback: %d %s", res.status, res.text)
	}
	return auth
}

// saved is the youtube section on disk, or nil.
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
	return all["youtube"]
}

func (r *rig) quota(t *testing.T) map[string]any {
	t.Helper()
	return r.setupState(t)["quota"].(map[string]any)
}

func (r *rig) setupState(t *testing.T) map[string]any {
	t.Helper()
	var st map[string]any
	json.Unmarshal(jsjson.Stringify(r.sender.SetupState(r.app.port), ""), &st)
	return st
}
