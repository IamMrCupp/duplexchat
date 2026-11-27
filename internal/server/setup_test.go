package server

// multichat test/sendapi.test.js at 29a9d2c: the setup cases (9 and 10),
// plus the setup routes' own order of checks and the sign-in callback,
// against fake senders. The real senders' actions arrive with #11 and #12.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

// setupSender is a fakeSender with setup actions, page-safe setup fields
// and, when matches is set, a sign-in callback.
type setupSender struct {
	*fakeSender
	state   func(port int) *jsjson.Object
	setup   func(action string, body *jsjson.Object, port int) (*SetupResult, error)
	matches func(r *http.Request) bool
	answer  func(w http.ResponseWriter, r *http.Request) error

	mu    sync.Mutex
	calls []string
}

func (s *setupSender) Setup(_ context.Context, action string, body *jsjson.Object, port int) (*SetupResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, fmt.Sprintf("%s %s %d", action, jsjson.Stringify(body, ""), port))
	s.mu.Unlock()
	return s.setup(action, body, port)
}

func (s *setupSender) SetupState(port int) *jsjson.Object { return s.state(port) }

func (s *setupSender) CallbackMatches(r *http.Request) bool { return s.matches != nil && s.matches(r) }

func (s *setupSender) Callback(w http.ResponseWriter, r *http.Request) error { return s.answer(w, r) }

func (s *setupSender) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

var withToken = map[string]string{"X-Dock-Token": "tok-123"}

// raw is a request with the dock's headers, answered in full.
func (a *api) raw(method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:3131"
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://127.0.0.1:3131")
		r.Header.Set("X-Dock-Token", "tok-123")
	}
	for k, v := range headers {
		switch k {
		case "Host":
			r.Host = v
		case "RemoteAddr":
			r.RemoteAddr = v
		default:
			r.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, r)
	return rec
}

// sendapi case 9: setup state is gated the same way and carries no secrets.
func TestSetupStateIsGated(t *testing.T) {
	a := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender()}})
	if _, j := a.do("GET", "/api/setup/state", nil, ""); j["code"] != "no_token" {
		t.Errorf("no token: %v", j)
	}
	if _, j := a.do("GET", "/api/setup/state", map[string]string{"X-Dock-Token": "tok-123", "Host": "evil.example"}, ""); j["code"] != "bad_host" {
		t.Errorf("foreign Host: %v", j)
	}
	code, j := a.do("GET", "/api/setup/state", withToken, "")
	if code != 200 || j["platforms"].(map[string]any)["twitch"].(map[string]any)["signedIn"] != true {
		t.Errorf("state: %d %v", code, j)
	}
	// a sender without setup actions
	if code, j := a.post("/api/setup/twitch/signin", map[string]any{}, nil); code != 404 {
		t.Errorf("signin with no setup: %d %v", code, j)
	}
	if code, _ := a.post("/api/setup/twitch/signin", map[string]any{}, map[string]string{"X-Dock-Token": "nope"}); code != 403 {
		t.Errorf("wrong token: %d", code)
	}
}

// sendapi case 10: routes that are not ours fall through.
func TestRoutesNotOursFallThrough(t *testing.T) {
	a := startAPI(Options{})
	if rec := a.raw("GET", "/overlay", nil, ""); rec.Code != 404 {
		t.Errorf("GET /overlay with no pages: %d", rec.Code)
	}
	if rec := a.raw("PUT", "/api/send", nil, ""); rec.Code != 404 {
		t.Errorf("PUT /api/send: %d", rec.Code)
	}
	for _, p := range []string{"/api/setup/twitch/signin", "/api/setup/state"} {
		if rec := a.raw("PUT", p, nil, ""); rec.Code != 404 {
			t.Errorf("PUT %s: %d", p, rec.Code)
		}
	}
	if rec := a.raw("GET", "/api/setup/twitch/signin", withToken, ""); rec.Code != 404 || rec.Body.String() != "not found" {
		t.Errorf("GET of an action: %d %q", rec.Code, rec.Body)
	}
	// not an action name: uppercase, a digit, another platform
	for _, p := range []string{"/api/setup/twitch/Signin", "/api/setup/twitch/sign1n", "/api/setup/kick/signin", "/api/setup/twitch/"} {
		if rec := a.raw("POST", p, nil, "{}"); rec.Code != 404 || rec.Body.String() != "not found" {
			t.Errorf("POST %s: %d %q", p, rec.Code, rec.Body)
		}
	}
}

// The sender's fields are added after the entry's own, as Object.assign
// adds them: a key the entry already has keeps its place. Notices are the
// lock's reason and the store's problems, without empties.
func TestSetupStateMergesSenderFields(t *testing.T) {
	tw := &setupSender{fakeSender: newFakeSender(), state: func(port int) *jsjson.Object {
		return jsjson.NewObject().Set("clientId", "").Set("detail", fmt.Sprintf("port %d", port)).Set("scopes", []any{"user:write:chat"})
	}}
	notices := []string{"", "secrets.json couldn't be parsed", ""}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": tw}, Notices: func() []string { return notices }})
	rec := a.raw("GET", "/api/setup/state", withToken, "")
	want := `{"platforms":{` +
		`"twitch":{"available":true,"configured":true,"signedIn":true,"as":"example_user","detail":"port 3131","connected":true,"target":"#examplechannel","targetKey":"aaaa1111:examplechannel","choices":[{"id":"aaaa1111","label":"#examplechannel"}],"maxLength":500,"warning":"","clientId":"","scopes":["user:write:chat"]},` +
		`"youtube":{"available":false,"configured":false,"signedIn":false,"as":null,"detail":"Sending to YouTube isn't in this build.","connected":true,"target":"Example Channel · video FAKEvideo01","targetKey":"bbbb2222:FAKEvideo01","choices":[{"id":"bbbb2222","label":"Example Channel"}],"maxLength":500,"warning":""}` +
		`},"notices":["secrets.json couldn't be parsed"]}`
	if rec.Code != 200 || rec.Body.String() != want {
		t.Errorf("state:\n got %d %s\nwant %s", rec.Code, rec.Body, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", rec.Header())
	}

	// a copy that doesn't own sending: no sender fields, the reason first
	off := startAPI(Options{Senders: map[string]Sender{"twitch": tw}, Notices: func() []string { return notices },
		Disabled: func() string { return "Sending is off in this copy." }})
	_, j := off.do("GET", "/api/setup/state", withToken, "")
	tj := j["platforms"].(map[string]any)["twitch"].(map[string]any)
	if _, ok := tj["clientId"]; ok || tj["available"] != false {
		t.Errorf("disabled copy shows sender fields: %v", tj)
	}
	if fmt.Sprint(j["notices"]) != "[Sending is off in this copy. secrets.json couldn't be parsed]" {
		t.Errorf("notices: %v", j["notices"])
	}

	// nothing to say is an empty list, not null
	if rec := startAPI(Options{}).raw("GET", "/api/setup/state", withToken, ""); !strings.HasSuffix(rec.Body.String(), `"notices":[]}`) {
		t.Errorf("no notices: %s", rec.Body)
	}
}

func TestSetupActions(t *testing.T) {
	var logged []string
	log := func(kind string, v any) { logged = append(logged, fmt.Sprintf("%s: %v", kind, v)) }
	tw := &setupSender{fakeSender: newFakeSender(), setup: func(action string, body *jsjson.Object, port int) (*SetupResult, error) {
		switch action {
		case "client":
			return &SetupResult{Status: 200, Body: jsjson.NewObject().Set("ok", true).Set("clientId", body.String("clientId"))}, nil
		case "refused":
			return &SetupResult{Status: 400, Body: jsjson.NewObject().Set("error", "That isn't a client ID.").Set("code", "bad_client_id")}, nil
		case "quiet":
			return &SetupResult{Status: 204}, nil
		case "bad-status":
			return &SetupResult{}, nil
		case "broken":
			return nil, errors.New("dial tcp: FAKE_SECRET_in_error")
		case "panics":
			panic("boom")
		}
		return nil, nil
	}}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": tw}, Log: log})

	rec := a.raw("POST", "/api/setup/twitch/client", nil, `{"clientId":"fakeclientid0123456789abcdefgh"," ":1}`)
	if rec.Code != 200 || rec.Body.String() != `{"ok":true,"clientId":"fakeclientid0123456789abcdefgh"}` {
		t.Errorf("client: %d %s", rec.Code, rec.Body)
	}
	if got := tw.called(); len(got) != 1 || got[0] != "client {\"clientId\":\"fakeclientid0123456789abcdefgh\",\" \":1} 3131" {
		t.Errorf("Setup saw %q", got)
	}
	if rec := a.raw("POST", "/api/setup/twitch/refused", nil, ""); rec.Code != 400 || rec.Body.String() != `{"error":"That isn't a client ID.","code":"bad_client_id"}` {
		t.Errorf("refused: %d %s", rec.Code, rec.Body)
	}
	if rec := a.raw("POST", "/api/setup/twitch/quiet", nil, ""); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Errorf("quiet: %d %q", rec.Code, rec.Body)
	}
	if code, j := a.post("/api/setup/twitch/no-such-action", map[string]any{}, nil); code != 404 || j["code"] != "not_found" || j["error"] != "not found" {
		t.Errorf("unknown action: %d %v", code, j)
	}
	for _, action := range []string{"broken", "panics", "bad-status"} {
		logged = nil
		code, j := a.post("/api/setup/twitch/"+action, map[string]any{}, nil)
		if code != 502 || j["code"] != "error" || j["error"] != "Couldn't reach Twitch." {
			t.Errorf("%s: %d %v", action, code, j)
		}
		if len(logged) != 1 || !strings.HasPrefix(logged[0], "setup:twitch: ") {
			t.Errorf("%s logged %q", action, logged)
		}
	}

	// bodies: checked after the gate, the lock and the sender, before the action
	before := len(tw.called())
	for body, want := range map[string]string{"{": "bad json", "[]": "body must be a JSON object", "null": "body must be a JSON object", `"x"`: "body must be a JSON object"} {
		if code, j := a.do("POST", "/api/setup/twitch/client", nil, body); code != 400 || j["code"] != "bad_request" || j["error"] != want {
			t.Errorf("body %s: %d %v", body, code, j)
		}
	}
	if code, j := a.do("POST", "/api/setup/twitch/client", nil, `{"x":"`+strings.Repeat("a", maxAPIBody)+`"}`); code != 400 || j["error"] != "body too large" {
		t.Errorf("big body: %d %v", code, j)
	}
	if n := len(tw.called()); n != before {
		t.Errorf("a bad body reached Setup (%d calls)", n-before)
	}

	// a sender without actions, and a platform with no sender at all
	b := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender()}})
	for p, l := range map[string]string{"twitch": "Twitch", "youtube": "YouTube"} {
		code, j := b.do("POST", "/api/setup/"+p+"/signin", nil, "{")
		if code != 404 || j["code"] != "unavailable" || j["error"] != "Sending to "+l+" isn't in this build." {
			t.Errorf("%s with no setup: %d %v", p, code, j)
		}
	}
}

// The gate comes first, then the lock: a refused request learns nothing,
// and a copy that doesn't own sending changes nothing.
func TestSetupActionOrderOfChecks(t *testing.T) {
	tw := &setupSender{fakeSender: newFakeSender(), setup: func(string, *jsjson.Object, int) (*SetupResult, error) {
		return &SetupResult{Status: 200, Body: jsjson.NewObject()}, nil
	}}
	off := startAPI(Options{Senders: map[string]Sender{"twitch": tw}, Disabled: func() string { return "Sending is off in this copy." }})
	if code, j := off.do("POST", "/api/setup/twitch/client", map[string]string{"X-Dock-Token": "nope"}, "{"); code != 403 || j["code"] != "bad_token" {
		t.Errorf("wrong token on a disabled copy: %d %v", code, j)
	}
	if code, j := off.do("POST", "/api/setup/twitch/client", map[string]string{"Origin": "http://evil.example"}, "{}"); code != 403 || j["code"] != "bad_origin" {
		t.Errorf("cross-origin: %d %v", code, j)
	}
	// a write needs an Origin, as sending does
	if code, j := off.do("POST", "/api/setup/twitch/client", map[string]string{"Origin": ""}, "{}"); code != 403 || j["error"] != "Missing Origin header." {
		t.Errorf("no Origin: %d %v", code, j)
	}
	code, j := off.do("POST", "/api/setup/twitch/client", nil, "{")
	if code != 409 || j["code"] != "sending_disabled" || j["error"] != "Sending is off in this copy." {
		t.Errorf("disabled copy: %d %v", code, j)
	}
	if code, j := off.do("POST", "/api/setup/youtube/client", nil, "{}"); code != 409 || j["code"] != "sending_disabled" {
		t.Errorf("disabled copy, no sender: %d %v", code, j)
	}
	if n := len(tw.called()); n != 0 {
		t.Errorf("Setup ran %d times in a copy that doesn't own sending", n)
	}
}

// A sender that takes a sign-in redirect claims it before the panel gets
// /. The redirect comes from the platform's site, so there's no Origin or
// token to check, but the request must still be this machine and this
// server's Host.
func TestSignInCallback(t *testing.T) {
	var logged []string
	log := func(kind string, v any) { logged = append(logged, fmt.Sprintf("%s: %v", kind, v)) }
	yt := &setupSender{fakeSender: newFakeSender(),
		matches: func(r *http.Request) bool {
			q := r.URL.Query()
			return r.Method == "GET" && r.URL.Path == "/" && q.Get("state") != "" && (q.Has("code") || q.Has("error"))
		},
		answer: func(w http.ResponseWriter, r *http.Request) error {
			switch r.URL.Query().Get("state") {
			case "broken":
				return errors.New("token exchange failed")
			case "late":
				w.WriteHeader(400)
				w.Write([]byte("<p>half a page"))
				return errors.New("cut off")
			case "panics":
				panic("boom")
			}
			w.Header().Set("Location", "/setup")
			w.WriteHeader(302)
			return nil
		}}
	opts := Options{Senders: map[string]Sender{"youtube": yt}, Log: log}
	a := startAPI(opts)
	a.srv.opts.Pages = fstest.MapFS{"panel.html": {Data: []byte("PANEL")}}

	cross := map[string]string{"Origin": "https://accounts.google.com", "X-Dock-Token": ""}
	if rec := a.raw("GET", "/?state=good&code=abc", cross, ""); rec.Code != 302 || rec.Header().Get("Location") != "/setup" {
		t.Errorf("callback: %d %v", rec.Code, rec.Header())
	}
	if rec := a.raw("GET", "/?state=good&error=access_denied", nil, ""); rec.Code != 302 {
		t.Errorf("callback with an error: %d", rec.Code)
	}
	for _, path := range []string{"/", "/?code=abc", "/?state=good"} {
		if rec := a.raw("GET", path, nil, ""); rec.Code != 200 || rec.Body.String() != "PANEL" {
			t.Errorf("%s isn't the panel: %d %q", path, rec.Code, rec.Body)
		}
	}
	if rec := a.raw("GET", "/?state=good&code=abc", map[string]string{"Host": "evil.example"}, ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "bad_host") {
		t.Errorf("foreign Host: %d %s", rec.Code, rec.Body)
	}
	if rec := a.raw("GET", "/?state=good&code=abc", map[string]string{"RemoteAddr": "192.168.1.20:50000"}, ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "not_loopback") {
		t.Errorf("LAN client: %d %s", rec.Code, rec.Body)
	}

	for _, state := range []string{"broken", "panics"} {
		logged = nil
		rec := a.raw("GET", "/?state="+state+"&code=abc", nil, "")
		if rec.Code != 502 || rec.Body.String() != `{"error":"Couldn't reach YouTube.","code":"error"}` {
			t.Errorf("%s: %d %s", state, rec.Code, rec.Body)
		}
		if len(logged) != 1 || !strings.HasPrefix(logged[0], "callback:youtube: ") {
			t.Errorf("%s logged %q", state, logged)
		}
	}
	// an error after the answer started is logged; what went out stays
	logged = nil
	if rec := a.raw("GET", "/?state=late&code=abc", nil, ""); rec.Code != 400 || rec.Body.String() != "<p>half a page" || len(logged) != 1 {
		t.Errorf("late error: %d %q, logged %q", rec.Code, rec.Body, logged)
	}

	// a copy that doesn't own sending claims nothing: / is the panel
	opts.Disabled = func() string { return "Sending is off in this copy." }
	off := startAPI(opts)
	off.srv.opts.Pages = a.srv.opts.Pages
	if rec := off.raw("GET", "/?state=good&code=abc", nil, ""); rec.Code != 200 || rec.Body.String() != "PANEL" {
		t.Errorf("disabled copy: %d %q", rec.Code, rec.Body)
	}
}
