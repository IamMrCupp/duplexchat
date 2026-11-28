package twitchsend

// test/twitch-send.test.js at 29a9d2c, the sending cases (1–19 and 22),
// 1:1 against the fake Twitch. The viewer (20–21) and stream info (23–29)
// cases come with Phase 3.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/server"
)

func ok1(id string) server.SendResult { return server.SendResult{OK: true, ID: id} }

// 1
func TestDeviceCodeFlow(t *testing.T) {
	f := newFake(t, 2)
	r := makeSender(t, f)
	s := r.sender
	if got := s.Status(); got != (server.SenderStatus{Configured: true}) {
		t.Fatalf("before: %+v", got)
	}
	code, body := setup(t, s, "signin", "")
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if code != 200 || fmt.Sprint(keys) != "[expiresAt userCode verificationUri]" || body["userCode"] != "ABCDEFGH" || body["verificationUri"] != verify {
		t.Fatalf("signin: %d %v", code, body)
	}
	if p, _ := state(t, s)["pending"].(map[string]any); p["userCode"] != "ABCDEFGH" {
		t.Fatalf("pending: %v", state(t, s))
	}
	waitFor(t, "sign-in", func() bool { return s.Status().SignedIn })
	if s.Status().As != "Example_User" || state(t, s)["pending"] != nil || f.count("POST /oauth2/token") != 3 {
		t.Fatalf("after: %+v %v, %d polls", s.Status(), state(t, s), f.count("POST /oauth2/token"))
	}

	// exactly the documented request: public client, one scope, no secret
	device := f.find(func(c call) bool { return c.key == "POST /oauth2/device" })[0]
	if device.raw != "client_id="+clientID+"&scopes=user%3Awrite%3Achat" {
		t.Errorf("device request: %s", device.raw)
	}
	poll := f.find(func(c call) bool { return c.key == "POST /oauth2/token" })[0]
	if poll.raw != "client_id="+clientID+"&scopes=user%3Awrite%3Achat&device_code=FAKE_DEVICE_CODE&grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code" {
		t.Errorf("poll: %s", poll.raw)
	}
	if n := len(f.find(func(c call) bool { _, has := c.body["client_secret"]; return has })); n != 0 {
		t.Errorf("%d requests carried a client secret", n)
	}

	saved := r.saved(t)
	if saved["accessToken"] != f.get(func() string { return f.access }) || saved["refreshToken"] != f.get(func() string { return f.refresh }) || saved["userId"] != "1001" {
		t.Errorf("saved: %v", saved)
	}
	b, _ := os.ReadFile(r.file)
	want := fmt.Sprintf(`{"twitch":{"accessToken":"FAKE_ACCESS_1","refreshToken":"FAKE_REFRESH_1","expiresAt":%d,"scopes":["user:write:chat"],"userId":"1001","login":"example_user","displayName":"Example_User"}}`,
		r.clock.ms.Load()+14400*1000)
	if strings.Join(strings.Fields(string(b)), "") != want {
		t.Errorf("file:\n got %s\nwant %s", b, want)
	}
	if fi, _ := os.Stat(r.file); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	// nothing the pages can see carries a token or the device code
	visible := fmt.Sprint(s.Status(), state(t, s), body)
	for _, secret := range []string{"FAKE_ACCESS", "FAKE_REFRESH", "FAKE_DEVICE_CODE"} {
		if strings.Contains(visible, secret) {
			t.Errorf("%s is visible: %s", secret, visible)
		}
	}
}

// 2
func TestDeviceFlowRefusedAndExpired(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	f.state(func() { f.deny = "invalid device code" })
	setup(t, s, "signin", "")
	waitFor(t, "an error", func() bool { return state(t, s)["pendingError"] != "" })
	if e := state(t, s)["pendingError"]; e != "Twitch refused the sign-in: invalid device code. Start again." || s.Status().SignedIn {
		t.Fatalf("refused: %v", e)
	}

	f.state(func() { f.deny = "authorization_pending" })
	setup(t, s, "signin", "")
	if e := state(t, s)["pendingError"]; e != "" {
		t.Fatalf("a new sign-in keeps the old error: %v", e)
	}
	r.clock.add(1801 * time.Second)
	waitFor(t, "expiry", func() bool { return state(t, s)["pendingError"] != "" })
	if st := state(t, s); st["pendingError"] != "The code expired before it was approved. Start again." || st["pending"] != nil {
		t.Fatalf("expired: %v", st)
	}
}

// 3
func TestDeviceFlowCancelAndNoClientID(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	f.state(func() { f.deny = "authorization_pending" })
	setup(t, s, "signin", "")
	setup(t, s, "cancel", "")
	polls := f.count("POST /oauth2/token")
	time.Sleep(40 * time.Millisecond)
	if n := f.count("POST /oauth2/token"); n != polls || state(t, s)["pending"] != nil {
		t.Fatalf("still polling after cancel: %d → %d", polls, n)
	}

	r.cfg.set("")
	before := f.callCount()
	if code, body := setup(t, s, "signin", ""); code != 400 || body["code"] != "no_client_id" || body["error"] != "Paste a Twitch client ID first." {
		t.Errorf("no client ID: %d %v", code, body)
	}
	if f.callCount() != before {
		t.Error("asked Twitch with no client ID")
	}
	if d := s.Status().Detail; d != "Needs a Twitch client ID." {
		t.Errorf("detail: %q", d)
	}
}

// 4
func TestClientIDValidatedAndChangingSignsOut(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	if code, body := setup(t, s, "client", `{"clientId":"has spaces!"}`); code != 400 || body["code"] != "bad_client_id" || body["error"] != "That doesn't look like a Twitch client ID." || !s.Status().SignedIn {
		t.Fatalf("bad ID: %d %v", code, body)
	}
	if code, _ := setup(t, s, "client", `{"clientId":"  `+clientID+`\n"}`); code != 200 || !s.Status().SignedIn {
		t.Fatal("the same ID (trimmed) changed something")
	}
	setup(t, s, "client", `{"clientId":"anotherfakeclientid0123456789"}`)
	if r.cfg.get() != "anotherfakeclientid0123456789" || s.Status().SignedIn {
		t.Fatalf("after change: %q %+v", r.cfg.get(), s.Status())
	}
}

// 5
func TestSendLooksUpOncePostsAsSignedInUser(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	for _, text := range []string{"hello chat", "second"} {
		if got, err := s.Send(t.Context(), text, target); err != nil || got != ok1("fake-msg-1") {
			t.Fatalf("send %q: %+v %v", text, got, err)
		}
	}
	if n := len(f.find(func(c call) bool { return c.key == "GET /helix/users" && c.query.Get("login") != "" })); n != 1 {
		t.Errorf("%d lookups", n)
	}
	posts := f.find(func(c call) bool { return c.key == "POST /helix/chat/messages" })
	if len(posts) != 2 || posts[0].raw != `{"broadcaster_id":"9001","sender_id":"1001","message":"hello chat"}` {
		t.Fatalf("posts: %v", posts)
	}
	if posts[0].header.Get("authorization") != "Bearer "+f.get(func() string { return f.access }) || posts[0].header.Get("client-id") != clientID || posts[0].header.Get("content-type") != "application/json" {
		t.Errorf("headers: %v", posts[0].header)
	}
}

// 6
func TestSendTwitchsOwnReasonComesBack(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	send := func(text string, tg ...string) server.SendResult {
		t.Helper()
		ch := target
		if len(tg) > 0 {
			ch = channel(tg[0])
		}
		got, err := s.Send(t.Context(), text, ch)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	script := func(fn func(c call) reply) { f.state(func() { f.chat = fn }) }
	dropped := func(code, message string) func(call) reply {
		return func(call) reply {
			return reply{200, map[string]any{"data": []any{map[string]any{"message_id": "", "is_sent": false, "drop_reason": map[string]any{"code": code, "message": message}}}}, nil}
		}
	}
	script(dropped("user_banned", "You are permanently banned from talking in examplechannel."))
	if got := send("x"); got != (server.SendResult{Code: "user_banned", Reason: "You are permanently banned from talking in examplechannel."}) {
		t.Errorf("banned: %+v", got)
	}
	script(dropped("slow_mode", "This room is in slow mode and you are sending messages too quickly."))
	if got := send("x"); got.Code != "slow_mode" {
		t.Errorf("slow mode: %+v", got)
	}
	script(dropped("followers_only", "This room is in followers-only mode."))
	if got := send("x"); got.Reason != "This room is in followers-only mode." {
		t.Errorf("followers only: %+v", got)
	}
	script(func(call) reply {
		return reply{403, map[string]any{"status": 403, "message": "The sender is not permitted to send chat messages to the broadcaster's chat room."}, nil}
	})
	if got := send("x"); got != (server.SendResult{Code: "forbidden", Reason: "The sender is not permitted to send chat messages to the broadcaster's chat room."}) {
		t.Errorf("403: %+v", got)
	}
	script(func(call) reply {
		return reply{422, map[string]any{"status": 422, "message": "The message is too large."}, nil}
	})
	if got := send("x"); got.Code != "too_long" {
		t.Errorf("422: %+v", got)
	}
	script(func(call) reply {
		return reply{400, map[string]any{"status": 400, "message": `The parameter "message" was malformed.`}, nil}
	})
	if got := send("x"); got.Reason != `The parameter "message" was malformed.` || got.Code != "rejected" {
		t.Errorf("400: %+v", got)
	}
	script(func(call) reply { return reply{503, nil, nil} })
	if got := send("x"); got.Reason != "Twitch answered HTTP 503." {
		t.Errorf("503: %+v", got)
	}

	// too long never leaves the machine
	before := f.callCount()
	if got := send(strings.Repeat("x", 501)); got != (server.SendResult{Code: "too_long", Reason: "Twitch messages are 500 characters at most."}) || f.callCount() != before {
		t.Errorf("501 characters: %+v", got)
	}
	// 500 characters of astral plane is 500, not 1000
	script(nil)
	if got := send(strings.Repeat("😀", 500)); !got.OK {
		t.Errorf("500 emoji: %+v", got)
	}

	f.state(func() {
		f.users = func(c call) reply {
			if c.query.Get("login") != "" {
				return reply{200, map[string]any{"data": []any{}}, nil}
			}
			return reply{200, map[string]any{"data": []any{map[string]any{"id": "1001"}}}, nil}
		}
	})
	if got := send("x", "nosuchchannel"); got != (server.SendResult{Code: "no_channel", Reason: "Twitch has no channel called nosuchchannel."}) {
		t.Errorf("no channel: %+v", got)
	}
	f.state(func() {
		f.users = func(c call) reply { return reply{500, map[string]any{"message": "Internal Server Error"}, nil} }
	})
	if got := send("x", "other channel"); got != (server.SendResult{Code: "lookup_failed", Reason: "Twitch couldn't look up other channel: Internal Server Error"}) {
		t.Errorf("lookup failed: %+v", got)
	}
	lookups := f.find(func(c call) bool { return c.key == "GET /helix/users" })
	if q := lookups[len(lookups)-1].rawQuery; q != "login=other%20channel" { // encodeURIComponent
		t.Errorf("lookup query: %s", q)
	}
	if got, _ := s.Send(t.Context(), "x", channel("")); got != (server.SendResult{Code: "no_target", Reason: "No Twitch chat is connected in the panel."}) {
		t.Errorf("no channel at all: %+v", got)
	}
}

// 7
func TestSendRateLimitedBacksOffUntilReset(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	r.clock.ms.Store(r.clock.ms.Load()/1000*1000 + 500) // half way through a second
	reset := r.clock.ms.Load()/1000 + 20
	f.state(func() {
		f.chat = func(call) reply {
			return reply{429, map[string]any{"status": 429, "message": "Too Many Requests"}, map[string]string{"Ratelimit-Reset": fmt.Sprint(reset)}}
		}
	})
	first, _ := s.Send(t.Context(), "x", target)
	if first.Code != "rate_limited" || first.Reason != "Twitch rate limit hit — holding off." || first.RetryAfter != 20 { // ceil(19.5)
		t.Fatalf("first: %+v", first)
	}
	if n := f.count("POST /helix/chat/messages"); n != 1 {
		t.Fatalf("%d attempts: retried blind", n)
	}
	f.state(func() { f.chat = nil })
	r.clock.add(5 * time.Second)
	held, _ := s.Send(t.Context(), "x", target)
	if held.Code != "rate_limited" || held.RetryAfter != 15 || f.count("POST /helix/chat/messages") != 1 {
		t.Fatalf("held: %+v", held)
	}
	r.clock.add(16 * time.Second)
	if got, _ := s.Send(t.Context(), "x", target); !got.OK || f.count("POST /helix/chat/messages") != 2 {
		t.Fatalf("after: %+v", got)
	}
}

// 8
func TestSendRateLimitedWithNoResetWaitsAMinute(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	f.state(func() {
		f.chat = func(call) reply {
			return reply{429, map[string]any{"status": 429, "message": "Too Many Requests"}, nil}
		}
	})
	if got, _ := s.Send(t.Context(), "x", target); got.RetryAfter != 60 {
		t.Fatalf("%+v", got)
	}
}

// 9
func TestRefreshBeforeExpiryOnceForConcurrentSends(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	oldRefresh := f.get(func() string { return f.refresh })
	r.clock.add((14400 - 120) * time.Second) // two minutes of life left
	var wg sync.WaitGroup
	results := make([]server.SendResult, 3)
	for i, text := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], _ = s.Send(context.Background(), text, target) }()
	}
	wg.Wait()
	for _, got := range results {
		if !got.OK {
			t.Fatalf("results: %+v", results)
		}
	}
	refreshes := f.find(func(c call) bool { return c.body["grant_type"] == "refresh_token" })
	if len(refreshes) != 1 || refreshes[0].raw != "client_id="+clientID+"&grant_type=refresh_token&refresh_token="+oldRefresh {
		t.Fatalf("refreshes: %v", refreshes)
	}
	saved := r.saved(t)
	if saved["refreshToken"] != f.get(func() string { return f.refresh }) || saved["refreshToken"] == oldRefresh || saved["userId"] != "1001" {
		t.Fatalf("saved: %v", saved)
	}
	// a healthy token is not refreshed again
	s.Send(t.Context(), "d", target)
	if f.refreshes() != 1 {
		t.Fatalf("%d refreshes", f.refreshes())
	}
}

// 10
func TestA401GetsOneRefreshAndOneRetry(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	s.Send(t.Context(), "warm the channel cache", target)
	f.state(func() { f.access = "FAKE_ACCESS_REVOKED_SERVER_SIDE" }) // our stored access token is now dead
	if got, _ := s.Send(t.Context(), "x", target); !got.OK {
		t.Fatalf("%+v", got)
	}
	if f.refreshes() != 1 || f.count("POST /helix/chat/messages") != 3 { // ok, 401, ok
		t.Fatalf("%d refreshes, %d posts", f.refreshes(), f.count("POST /helix/chat/messages"))
	}
}

// 11
func TestADeadRefreshTokenSignsOutAndSaysSo(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	f.state(func() { f.refresh = "FAKE_REFRESH_ROTATED_ELSEWHERE" })
	r.clock.add(14400 * time.Second)
	if got, _ := s.Send(t.Context(), "x", target); got != (server.SendResult{Code: "not_signed_in", Reason: Expired}) {
		t.Fatalf("%+v", got)
	}
	if got := s.Status(); got != (server.SenderStatus{Configured: true, Detail: Expired}) {
		t.Fatalf("status: %+v", got)
	}
	if r.saved(t) != nil || f.count("POST /helix/chat/messages") != 0 {
		t.Fatalf("saved %v", r.saved(t))
	}
}

// 12
func TestSignOutRevokesTheTokenAndForgetsIt(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	access := f.get(func() string { return f.access })
	if code, body := setup(t, s, "signout", ""); code != 200 || body["ok"] != true {
		t.Fatalf("signout: %d %v", code, body)
	}
	if fmt.Sprint(f.revoked) != "["+access+"]" || s.Status().SignedIn || r.saved(t) != nil {
		t.Fatalf("revoked %v, status %+v, saved %v", f.revoked, s.Status(), r.saved(t))
	}
	if got, _ := s.Send(t.Context(), "x", target); got.Code != "not_signed_in" {
		t.Fatalf("%+v", got)
	}
}

// 13
func TestTwitchUnreachableIsAPlainFailureTokensKept(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	f.srv.Close()
	if got, _ := s.Send(t.Context(), "x", target); got != (server.SendResult{Code: "unreachable", Reason: "Couldn't reach Twitch."}) {
		t.Fatalf("%+v", got)
	}
	if !s.Status().SignedIn {
		t.Fatal("signed out by a network failure")
	}
}

// 15
func TestARefreshInFlightWhenISignOutDoesNotSignMeBackIn(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	hold, seen := make(chan struct{}), make(chan struct{})
	f.state(func() { f.hold, f.seen = hold, seen })
	r.clock.add((14400 - 60) * time.Second) // close to expiry: the next send refreshes first
	sending := make(chan server.SendResult)
	go func() { got, _ := s.Send(context.Background(), "x", target); sending <- got }()
	<-seen // the refresh is on the wire
	setup(t, s, "signout", "")
	close(hold)
	if got := <-sending; got.Code != "not_signed_in" {
		t.Fatalf("%+v", got)
	}
	if s.Status().SignedIn || r.saved(t) != nil || f.count("POST /helix/chat/messages") != 0 {
		t.Fatalf("the late answer was written back: %v", r.saved(t))
	}
	if f.refreshes() != 1 || f.count("GET /helix/users") != 1 {
		t.Fatalf("%d refreshes, %d user calls after the sign-out", f.refreshes(), f.count("GET /helix/users"))
	}
}

// 16
func TestARefreshInFlightWhenTheClientIDChangesDoesNotRestoreTheOldSignIn(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	hold, seen := make(chan struct{}), make(chan struct{})
	f.state(func() { f.hold, f.seen = hold, seen })
	r.clock.add((14400 - 60) * time.Second)
	sending := make(chan server.SendResult)
	go func() { got, _ := s.Send(context.Background(), "x", target); sending <- got }()
	<-seen
	setup(t, s, "client", `{"clientId":"anotherfakeclientid0123456789"}`)
	close(hold)
	if got := <-sending; got.OK {
		t.Fatalf("%+v", got)
	}
	if r.cfg.get() != "anotherfakeclientid0123456789" || s.Status().SignedIn || r.saved(t) != nil {
		t.Fatalf("old sign-in restored: %v", r.saved(t))
	}
	// the late answer was dropped there and then: nothing more reached Twitch
	if f.refreshes() != 1 || f.count("GET /helix/users") != 1 {
		t.Fatalf("%d refreshes, %d user calls after the change", f.refreshes(), f.count("GET /helix/users"))
	}
}

// 17
func TestTwoCopiesUseTheRotatedRefreshToken(t *testing.T) {
	f := newFake(t, 1)
	a := makeSender(t, f)
	signIn(t, a.sender)
	// a second process: its own store object, same file
	b := New(Options{
		Store: secrets.New(a.file, secrets.Options{}), ClientID: func() string { return clientID }, SetClientID: func(string) {},
		IDBase: f.base(), APIBase: f.base(), Now: a.clock.now,
	})
	if !b.Status().SignedIn {
		t.Fatal("the second copy doesn't see the sign-in")
	}
	a.clock.add((14400 - 60) * time.Second)
	if got, _ := a.sender.Send(t.Context(), "from a", target); !got.OK { // a refreshes: the old refresh token is spent
		t.Fatalf("a: %+v", got)
	}
	rotated := f.get(func() string { return f.refresh })
	if got, _ := b.Send(t.Context(), "from b", target); !got.OK { // b reads the file and sees the new tokens
		t.Fatalf("b: %+v", got)
	}
	if f.refreshes() != 1 || a.saved(t)["refreshToken"] != rotated {
		t.Fatalf("%d refreshes, saved %v", f.refreshes(), a.saved(t))
	}

	// the worst case: both start a refresh with the same token; the loser gets
	// a 400 once the winner's token is on disk, and carries on with it
	f.state(func() {
		f.beforeRefusal = func() {
			waitFor(t, "the winner's save", func() bool {
				return a.saved(t)["refreshToken"] == f.get(func() string { return f.refresh })
			})
		}
	})
	a.clock.add(14400 * time.Second)
	results := make([]server.SendResult, 2)
	var wg sync.WaitGroup
	for i, snd := range []*Sender{a.sender, b} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], _ = snd.Send(context.Background(), "race", target) }()
	}
	wg.Wait()
	if !results[0].OK || !results[1].OK || !a.sender.Status().SignedIn || !b.Status().SignedIn {
		t.Fatalf("race: %+v", results)
	}
	if a.saved(t)["refreshToken"] != f.get(func() string { return f.refresh }) {
		t.Fatalf("saved %v", a.saved(t))
	}
}

// failing is a transport that drops the first n device-flow polls, the way
// a dropped connection looks.
type failing struct {
	mu       sync.Mutex
	left     int
	attempts int
}

func (f *failing) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
		f.mu.Lock()
		f.attempts++
		drop := f.left > 0
		if drop {
			f.left--
		}
		f.mu.Unlock()
		if drop {
			return nil, errors.New("fetch failed")
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (f *failing) set(left int) { f.mu.Lock(); f.left = left; f.mu.Unlock() }
func (f *failing) count() int   { f.mu.Lock(); defer f.mu.Unlock(); return f.attempts }

// 18
func TestDeviceFlowKeepsPollingThroughNetworkErrorsUntilTheCodeExpires(t *testing.T) {
	f := newFake(t, 1)
	tr := &failing{left: 3}
	r := makeSender(t, f, func(o *Options) { o.Client = &http.Client{Transport: tr} })
	s := r.sender
	setup(t, s, "signin", "")
	waitFor(t, "sign-in", func() bool { return s.Status().SignedIn })
	if n := tr.count(); n < 5 { // 3 failures, 1 pending, 1 success
		t.Fatalf("%d attempts", n)
	}
	if e := state(t, s)["pendingError"]; e != "" {
		t.Fatalf("pendingError %v", e)
	}

	// and it does stop: errors all the way to expiry end the flow with the expiry message
	setup(t, s, "signout", "")
	tr.set(math.MaxInt)
	f.state(func() { f.pendingLeft = 0 })
	setup(t, s, "signin", "")
	time.Sleep(40 * time.Millisecond)
	if state(t, s)["pending"] == nil {
		t.Fatal("not pending after repeated errors")
	}
	r.clock.add(1801 * time.Second)
	waitFor(t, "expiry", func() bool { return state(t, s)["pendingError"] != "" })
	if e := state(t, s)["pendingError"]; e != "The code expired before it was approved. Start again." {
		t.Fatalf("%v", e)
	}
}

// 22
func TestAuthChangeIsAnnounced(t *testing.T) {
	f := newFake(t, 2)
	var mu sync.Mutex
	n := 0
	count := func() int { mu.Lock(); defer mu.Unlock(); return n }
	r := makeSender(t, f, func(o *Options) {
		o.OnAuthChange = func() { mu.Lock(); n++; mu.Unlock(); panic("listener bug") }
	})
	s := r.sender
	setup(t, s, "signin", "")
	if count() != 0 { // a code on screen isn't a sign-in
		t.Fatal("announced a code")
	}
	waitFor(t, "sign-in", func() bool { return s.Status().SignedIn })
	waitFor(t, "the announcement", func() bool { return count() == 1 })
	if s.Status().As != "Example_User" { // the panicking listener didn't undo it
		t.Fatalf("%+v", s.Status())
	}
	if code, _ := setup(t, s, "signout", ""); code != 200 || count() != 2 || s.Status().SignedIn {
		t.Fatalf("signout: %d, %d announcements", code, count())
	}
	setup(t, s, "client", `{"clientId":"`+clientID+`"}`)
	if count() != 2 { // same client: nothing changed
		t.Fatal("same client announced")
	}
	setup(t, s, "client", `{"clientId":"otherfakeclientid0123456789abcd"}`)
	if count() != 3 {
		t.Fatalf("%d announcements", count())
	}
}

func TestUnknownActionAndScopes(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	if code, _ := setup(t, s, "nonsense", ""); code != 0 {
		t.Fatal("an unknown action got an answer")
	}
	st := state(t, s)
	want := map[string]any{"clientId": clientID, "scopes": "user:write:chat", "infoScope": "channel:manage:broadcast", "hasInfoScope": false, "pending": nil, "pendingError": ""}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("setup state: %v", st)
	}
	// the stream info panel's sign-in asks for both, and later sign-ins keep asking
	setup(t, s, "signin", `{"info":true}`)
	waitFor(t, "sign-in", func() bool { return s.Status().SignedIn })
	if !s.hasInfoScope() {
		t.Fatal("no info scope")
	}
	setup(t, s, "signin", "")
	devices := f.find(func(c call) bool { return c.key == "POST /oauth2/device" })
	if len(devices) != 2 || devices[1].body["scopes"] != "user:write:chat channel:manage:broadcast" {
		t.Fatalf("devices: %v", devices)
	}
	if s.MaxLength() != 500 {
		t.Fatal("MaxLength")
	}
}
