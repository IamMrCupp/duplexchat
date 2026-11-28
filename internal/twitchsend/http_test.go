package twitchsend

// The over-HTTP cases of test/twitch-send.test.js (14 and 19): the real
// sender behind the server's routes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

type app struct {
	h    http.Handler
	seen []string
}

func startApp(s *Sender, edit func(*server.Options)) *app {
	ch := "examplechannel"
	o := server.Options{
		Pages: fstest.MapFS{}, Port: 3131, Token: "tok-123",
		Senders: map[string]server.Sender{"twitch": s},
		Targets: func() map[string]targets.Target {
			return map[string]targets.Target{"twitch": {Pinned: true, SourceID: "aaaa1111", Channel: &ch, Label: "#examplechannel", Key: "aaaa1111:examplechannel",
				Choices: []targets.Choice{{ID: "aaaa1111", Label: "#examplechannel"}}}}
		},
	}
	if edit != nil {
		edit(&o)
	}
	return &app{h: server.New(o)}
}

// do is a well-formed request from the dock page, with headers overridden.
func (a *app) do(method, path, body string, headers map[string]string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:3131"
	r.Header.Set("X-Dock-Token", "tok-123")
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://127.0.0.1:3131")
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if k == "Host" {
			r.Host = v
			continue
		}
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, r)
	a.seen = append(a.seen, rec.Body.String())
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}

// 14
func TestOverHTTPSignInFromSetupSendFromTheDock(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	a := startApp(s, nil)

	// gated like sending
	for want, h := range map[string]map[string]string{
		"bad_token":  {"X-Dock-Token": "wrong"},
		"bad_origin": {"Origin": "https://evil.example"},
		"bad_host":   {"Host": "evil.example:3131"},
	} {
		if _, j := a.do("POST", "/api/setup/twitch/signin", "{}", h); j["code"] != want {
			t.Errorf("%s: %v", want, j)
		}
	}
	if f.callCount() != 0 {
		t.Fatal("a refused request reached Twitch")
	}

	if _, j := a.do("POST", "/api/setup/twitch/signin", "{}", nil); j["userCode"] != "ABCDEFGH" {
		t.Fatalf("signin: %v", j)
	}
	if _, j := a.do("GET", "/api/setup/state", "", nil); dig(j, "platforms", "twitch", "clientId") != clientID {
		t.Fatalf("state: %v", j)
	}
	waitFor(t, "sign-in", func() bool { return s.Status().SignedIn })
	_, j := a.do("GET", "/api/setup/state", "", nil)
	if dig(j, "platforms", "twitch", "signedIn") != true || dig(j, "platforms", "twitch", "as") != "Example_User" {
		t.Fatalf("state: %v", j)
	}
	a.do("GET", "/api/dock/session", "", nil)

	code, j := a.do("POST", "/api/send", `{"target":"twitch","text":"hello from the dock","expect":{"twitch":"aaaa1111:examplechannel"}}`, nil)
	if b, _ := json.Marshal(j); code != 200 || string(b) != `{"results":{"twitch":{"id":"fake-msg-1","ok":true}}}` {
		t.Fatalf("send: %d %s", code, b)
	}
	posts := f.find(func(c call) bool { return c.key == "POST /helix/chat/messages" })
	if posts[len(posts)-1].body["message"] != "hello from the dock" {
		t.Fatalf("posted %v", posts[len(posts)-1].body)
	}

	a.do("POST", "/api/setup/twitch/signout", "{}", nil)
	if _, j := a.do("POST", "/api/send", `{"target":"twitch","text":"x","expect":{"twitch":"aaaa1111:examplechannel"}}`, nil); dig(j, "results", "twitch", "code") != "not_signed_in" {
		t.Fatalf("after signout: %v", j)
	}
	if code, _ := a.do("POST", "/api/setup/twitch/nonsense", "{}", nil); code != 404 {
		t.Fatalf("nonsense: %d", code)
	}

	all := strings.Join(a.seen, "\n")
	for _, secret := range []string{"FAKE_ACCESS_", "FAKE_REFRESH_", "FAKE_DEVICE_CODE"} {
		if strings.Contains(all, secret) {
			t.Errorf("%s in a response", secret)
		}
	}
	if len(f.revoked) != 1 {
		t.Errorf("revoked %v", f.revoked)
	}
}

// 19
func TestASecondCopyDoesNotSendAndSaysWhy(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	why := "Sending is off in this copy: another copy of the server (process 1001) is already running from this folder and owns sending."
	a := startApp(s, func(o *server.Options) {
		o.Disabled = func() string { return why }
		o.Notices = func() []string {
			return []string{"secrets.json couldn't be parsed, so it was moved to secrets.json.broken-20261120T101500Z and sign-ins start fresh."}
		}
	})
	calls := f.callCount()
	_, j := a.do("GET", "/api/dock/session", "", nil)
	if tw := dig(j, "platforms", "twitch"); dig(tw, "available") != false || dig(tw, "signedIn") != false || dig(tw, "detail") != why {
		t.Fatalf("session: %v", tw)
	}
	_, j = a.do("POST", "/api/send", `{"target":"twitch","text":"x","expect":{}}`, nil)
	if b, _ := json.Marshal(dig(j, "results", "twitch")); string(b) != `{"code":"sending_disabled","ok":false,"reason":"`+why+`"}` {
		t.Fatalf("send: %s", b)
	}
	if code, j := a.do("POST", "/api/setup/twitch/signin", "{}", nil); code != 409 || j["code"] != "sending_disabled" {
		t.Fatalf("signin: %d %v", code, j)
	}
	_, j = a.do("GET", "/api/setup/state", "", nil)
	notices, _ := j["notices"].([]any)
	if len(notices) != 2 || notices[0] != why || !strings.Contains(notices[1].(string), "moved to secrets.json.broken-") {
		t.Fatalf("notices: %v", notices)
	}
	if _, has := dig(j, "platforms", "twitch").(map[string]any)["clientId"]; has {
		t.Fatal("a copy that doesn't own sending shows the sender's fields")
	}
	if f.callCount() != calls {
		t.Fatal("something reached Twitch")
	}
}
