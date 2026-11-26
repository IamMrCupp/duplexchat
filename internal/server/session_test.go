package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func sessionServer() *Server {
	return New(Options{Pages: fstest.MapFS{}, Port: 3131, Token: "tok-123"})
}

func sessionReq(mut func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/dock/session", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:3131"
	if mut != nil {
		mut(r)
	}
	return r
}

// multichat sendapi.test.js: "session hands the dock its token and platform state",
// with no senders — what multichat reports before sending exists.
func TestSessionHandsOutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	sessionServer().ServeHTTP(rec, sessionReq(nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("headers %v", h)
	}
	want := `{"token":"tok-123","maxLength":500,"platforms":{` +
		`"twitch":{"available":false,"configured":false,"signedIn":false,"as":null,"detail":"Sending to Twitch isn't in this build.","connected":false,"target":null,"targetKey":null,"choices":[],"maxLength":500,"warning":""},` +
		`"youtube":{"available":false,"configured":false,"signedIn":false,"as":null,"detail":"Sending to YouTube isn't in this build.","connected":false,"target":null,"targetKey":null,"choices":[],"maxLength":500,"warning":""}}}`
	if rec.Body.String() != want {
		t.Errorf("body\n%s\nwant\n%s", rec.Body, want)
	}
	// typed into the address bar: no Origin, Sec-Fetch-Site none
	rec = httptest.NewRecorder()
	sessionServer().ServeHTTP(rec, sessionReq(func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }))
	if rec.Code != 200 {
		t.Errorf("typed URL: %d", rec.Code)
	}
}

// multichat sendapi.test.js: "session refuses a foreign Host, a foreign Origin and a LAN client".
func TestSessionRefusals(t *testing.T) {
	cases := map[string]struct {
		mut  func(*http.Request)
		code string
	}{
		"foreign Host":   {func(r *http.Request) { r.Host = "evil.example:3131" }, "bad_host"},
		"foreign Origin": {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, "bad_origin"},
		"cross-site":     {func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, "bad_origin"},
		"LAN client":     {func(r *http.Request) { r.RemoteAddr = "192.168.1.20:50000" }, "not_loopback"},
		"wrong port":     {func(r *http.Request) { r.Host = "127.0.0.1:9999" }, "bad_host"},
	}
	for name, c := range cases {
		rec := httptest.NewRecorder()
		sessionServer().ServeHTTP(rec, sessionReq(c.mut))
		var body map[string]string
		json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 403 || body["code"] != c.code || body["token"] != "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if rec.Body.String()[:9] != `{"error":` {
			t.Errorf("%s: body key order %s", name, rec.Body)
		}
	}
	// only GET
	rec := httptest.NewRecorder()
	r := sessionReq(nil)
	r.Method = http.MethodPost
	sessionServer().ServeHTTP(rec, r)
	if rec.Code != 404 {
		t.Errorf("POST session: %d", rec.Code)
	}
}
