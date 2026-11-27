package server

// multichat test/sendapi.test.js at 29a9d2c: the cases about the session's
// platform state and choosing a reply target. The send and setup halves of
// those cases arrive with their routes (#10, #13).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// fakeSender is helpers.js fakeSender: signed in as example_user, records
// what it was asked to send and answers however the test says.
type fakeSender struct {
	signedIn  bool
	as, id    string
	maxLength int
	reply     func(text string, t targets.Target) (SendResult, error)

	mu   sync.Mutex
	sent []sent
}

type sent struct {
	text   string
	target targets.Target
}

func (f *fakeSender) Send(_ context.Context, text string, t targets.Target) (SendResult, error) {
	f.mu.Lock()
	f.sent = append(f.sent, sent{text, t})
	n := len(f.sent)
	f.mu.Unlock()
	if f.reply != nil {
		return f.reply(text, t)
	}
	return SendResult{OK: true, ID: fmt.Sprintf("fake-%d", n)}, nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func newFakeSender() *fakeSender { return &fakeSender{signedIn: true, as: "example_user"} }

func (f *fakeSender) Status() SenderStatus {
	st := SenderStatus{Configured: true, SignedIn: f.signedIn}
	if f.signedIn {
		st.As, st.ID = f.as, f.id
	}
	return st
}

func (f *fakeSender) MaxLength() int { return f.maxLength }

func str(s string) *string { return &s }

// TARGETS from helpers.js.
func fixedTargets() map[string]targets.Target {
	return map[string]targets.Target{
		"twitch": {Pinned: true, SourceID: "aaaa1111", Channel: str("examplechannel"), Label: "#examplechannel", Key: "aaaa1111:examplechannel",
			Choices: []targets.Choice{{ID: "aaaa1111", Label: "#examplechannel"}}},
		"youtube": {Pinned: true, SourceID: "bbbb2222", VideoID: str("FAKEvideo01"), Channel: str("Example Channel"), Label: "Example Channel · video FAKEvideo01", Key: "bbbb2222:FAKEvideo01",
			Choices: []targets.Choice{{ID: "bbbb2222", Label: "Example Channel"}}},
	}
}

type api struct {
	srv *Server
}

func startAPI(o Options) *api {
	o.Pages, o.Port, o.Token = fstest.MapFS{}, 3131, "tok-123"
	if o.Targets == nil {
		o.Targets = fixedTargets
	}
	return &api{New(o)}
}

func (a *api) do(method, path string, headers map[string]string, body string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:3131"
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://127.0.0.1:3131")
		r.Header.Set("X-Dock-Token", "tok-123")
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		switch {
		case k == "Host":
			r.Host = v
		case k == "Origin" && v == "":
			r.Header.Del("Origin") // no Origin at all
		default:
			r.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, r)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (a *api) post(path string, body any, headers map[string]string) (int, map[string]any) {
	b, _ := json.Marshal(body)
	return a.do(http.MethodPost, path, headers, string(b))
}

// send posts to /api/send filling in expect as the dock does (the target
// keys on show), unless the body has its own.
func (a *api) send(body map[string]any, headers map[string]string) (int, map[string]any) {
	if _, ok := body["expect"]; !ok {
		expect := map[string]any{}
		for p, t := range a.srv.targets() {
			if t.Pinned {
				expect[p] = t.Key
			}
		}
		body["expect"] = expect
	}
	return a.post("/api/send", body, headers)
}

func (a *api) platform(p string) map[string]any {
	_, s := a.do(http.MethodGet, "/api/dock/session", nil, "")
	return s["platforms"].(map[string]any)[p].(map[string]any)
}

func TestSessionHandsTheDockItsTokenAndPlatformState(t *testing.T) {
	a := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender()}})
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, sessionReq(nil))
	want := `{"available":true,"configured":true,"signedIn":true,"as":"example_user","detail":"","connected":true,"target":"#examplechannel","targetKey":"aaaa1111:examplechannel","choices":[{"id":"aaaa1111","label":"#examplechannel"}],"maxLength":500,"warning":""}`
	if !strings.Contains(rec.Body.String(), `"twitch":`+want) {
		t.Fatalf("body %s", rec.Body)
	}
	y := a.platform("youtube")
	if y["available"] != false || y["signedIn"] != false {
		t.Fatalf("youtube %v", y)
	}
}

func TestReplyTargetChoosingOneIsGatedAndValidated(t *testing.T) {
	var picks [][2]string
	a := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender()}, SetTarget: func(p, id string) bool {
		picks = append(picks, [2]string{p, id})
		return id == "aaaa1111"
	}})
	if code, body := a.post("/api/dock/target", map[string]any{"platform": "twitch", "source": "aaaa1111"}, nil); code != 200 || body["platforms"] == nil {
		t.Fatalf("pick: %d %v", code, body)
	}
	for _, b := range []map[string]any{
		{"platform": "twitch", "source": "nope"},
		{"platform": "discord", "source": "aaaa1111"},
		{"platform": "twitch"},
	} {
		if code, body := a.post("/api/dock/target", b, nil); code != 400 || body["code"] != "bad_target" || body["error"] != "No such source for that platform." {
			t.Errorf("%v: %d %v", b, code, body)
		}
	}
	if want := [][2]string{{"twitch", "aaaa1111"}, {"twitch", "nope"}}; !reflect.DeepEqual(picks, want) {
		t.Fatalf("picks %v", picks)
	}
	if _, body := a.post("/api/dock/target", map[string]any{"platform": "twitch", "source": "aaaa1111"}, map[string]string{"X-Dock-Token": "wrong"}); body["code"] != "bad_token" {
		t.Errorf("wrong token: %v", body)
	}
	if _, body := a.post("/api/dock/target", map[string]any{"platform": "twitch", "source": "aaaa1111"}, map[string]string{"Origin": "https://evil.example"}); body["code"] != "bad_origin" {
		t.Errorf("foreign origin: %v", body)
	}
	if len(picks) != 2 {
		t.Fatalf("a refused request reached setTarget: %v", picks)
	}
}

func TestWrongChannelIsSaidNotRefused(t *testing.T) {
	const mine, theirs = "UCfakefakefakefakefakeAA", "UCfakefakefakefakefakeBB"
	target := func(owner *string) map[string]targets.Target {
		all := fixedTargets()
		y := all["youtube"]
		y.OwnerID = owner
		all["youtube"] = y
		return all
	}
	youtube := &fakeSender{signedIn: true, as: "Example Channel (@example2960)", id: mine}
	current := target(str(theirs))
	a := startAPI(Options{
		Senders: map[string]Sender{"twitch": newFakeSender(), "youtube": youtube},
		Targets: func() map[string]targets.Target { return current },
	})
	w, _ := a.platform("youtube")["warning"].(string)
	if want := "Signed in as Example Channel (@example2960) — not the channel this YouTube chat belongs to. Replies show under that name. Wrong channel? Sign out and in again on /setup."; w != want {
		t.Fatalf("warning %q", w)
	}
	if a.platform("twitch")["warning"] != "" {
		t.Fatal("twitch warned")
	}
	for name, c := range map[string]map[string]targets.Target{
		"the right channel": target(str(mine)),
		"owner unknown":     target(nil),
		"nothing pinned":    {"twitch": fixedTargets()["twitch"], "youtube": {Choices: []targets.Choice{}}},
	} {
		current = c
		if w := a.platform("youtube")["warning"]; w != "" {
			t.Errorf("%s: warned %q", name, w)
		}
	}
	current = target(str(theirs))
	youtube.signedIn = false // nobody signed in: nothing to warn about
	if w := a.platform("youtube")["warning"]; w != "" {
		t.Errorf("signed out: warned %q", w)
	}
}

// A second copy on the folder: nothing is available, and every platform
// says why in its detail.
func TestDisabledSaysWhyOnEveryPlatform(t *testing.T) {
	const why = "Sending is off in this copy: another copy of the server (process 1001) is already running from this folder and owns sending. Use that one's dock, or stop it."
	a := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender()}, Disabled: func() string { return why }})
	for _, p := range []string{"twitch", "youtube"} {
		st := a.platform(p)
		if st["available"] != false || st["signedIn"] != false || st["detail"] != why {
			t.Errorf("%s: %v", p, st)
		}
	}
	// the target still shows: where a reply would go is not a secret
	if a.platform("twitch")["target"] != "#examplechannel" {
		t.Error("target hidden")
	}
}

func TestPlatformLimits(t *testing.T) {
	a := startAPI(Options{Senders: map[string]Sender{"twitch": &fakeSender{maxLength: 500}, "youtube": &fakeSender{maxLength: 200}}})
	if a.platform("twitch")["maxLength"] != float64(500) || a.platform("youtube")["maxLength"] != float64(200) {
		t.Fatal("limits")
	}
	big := startAPI(Options{Senders: map[string]Sender{"twitch": &fakeSender{maxLength: 9000}}})
	if big.platform("twitch")["maxLength"] != float64(500) {
		t.Fatal("a sender's limit above 500 isn't capped")
	}
}

// readJson's refusals, through the dock-target route.
func TestReadJSONBadBodies(t *testing.T) {
	a := startAPI(Options{SetTarget: func(string, string) bool { return true }})
	for body, want := range map[string]string{
		"{nope":    "bad json",
		"null":     "body must be a JSON object",
		"[1]":      "body must be a JSON object",
		"42":       "body must be a JSON object",
		`"x"`:      "body must be a JSON object",
		"\ufeff{}": "bad json",
		`{"x":"` + strings.Repeat("a", 16400) + `"}`: "body too large",
		`{"x":"` + strings.Repeat("я", 16400) + `"}`: "body too large",
	} {
		code, out := a.do(http.MethodPost, "/api/dock/target", nil, body)
		if code != 400 || out["error"] != want || out["code"] != "bad_request" {
			t.Errorf("%.20q: %d %v", body, code, out)
		}
	}
	// an empty body is {}, which has no platform; under the limit in UTF-16
	// units though over it in bytes is fine
	if _, out := a.do(http.MethodPost, "/api/dock/target", nil, ""); out["code"] != "bad_target" {
		t.Errorf("empty: %v", out)
	}
	long := fmt.Sprintf(`{"platform":"twitch","source":"aaaa1111","x":%q}`, strings.Repeat("я", 16000))
	if code, out := a.do(http.MethodPost, "/api/dock/target", nil, long); code != 200 {
		t.Errorf("16000 Cyrillic letters: %d %v", code, out)
	}
}
