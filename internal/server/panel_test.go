package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/IamMrCupp/duplexchat/internal/sources"
)

type fakePanel struct{ added []string }

func (f *fakePanel) AddSource(link string) (sources.Entry, error) {
	f.added = append(f.added, link)
	if sources.DetectPlatform(link) == "" {
		return sources.Entry{}, sources.ErrNotAStream
	}
	return sources.Entry{ID: "abcd1234", URL: link, Platform: sources.DetectPlatform(link)}, nil
}
func (f *fakePanel) RemoveSource(string) bool    { return false }
func (f *fakePanel) ReconnectSource(string) bool { return false }
func (f *fakePanel) Test()                       {}
func (f *fakePanel) ClearAll()                   {}

func postSource(t *testing.T, body string) (*httptest.ResponseRecorder, *fakePanel) {
	t.Helper()
	p := &fakePanel{}
	s := New(Options{Pages: fstest.MapFS{}, Port: 3131, Token: "tok", Panel: p})
	r := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:3131"
	r.Header.Set("Origin", "http://127.0.0.1:3131")
	r.Header.Set("X-Dock-Token", "tok")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	return rec, p
}

func TestAddSourceBodies(t *testing.T) {
	cases := []struct {
		body string
		code int
		want string
	}{
		{`{"url":"twitch.tv/x"}`, 200, `{"source":{"id":"abcd1234","url":"twitch.tv/x","platform":"twitch"}}`},
		{`{"url":"  twitch.tv/x\n"}`, 200, `"url":"twitch.tv/x"`},
		{``, 400, `{"error":"That link is empty"}`},
		{`{}`, 400, `{"error":"That link is empty"}`},
		{`{"url":"   "}`, 400, `{"error":"That link is empty"}`},
		{`null`, 400, `{"error":"That link is empty"}`},
		{`[1,2]`, 400, `{"error":"That link is empty"}`},
		{`{not json`, 400, `{"error":"bad json"}`},
		{`{"url":42}`, 400, `{"error":"That doesn't look like a Twitch or YouTube link"}`},
		{`{"url":"https://example.com"}`, 400, `{"error":"That doesn't look like a Twitch or YouTube link"}`},
		{`{"url":"` + strings.Repeat("x", 70000) + `"}`, 400, `{"error":"body too large"}`},
	}
	for _, c := range cases {
		rec, _ := postSource(t, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			name := c.body
			if len(name) > 40 {
				name = name[:40]
			}
			t.Errorf("%q: %d %s", name, rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%q: Content-Type %q", c.body, ct)
		}
	}
}

func TestPanelRoutesNeedAPanel(t *testing.T) {
	s := New(Options{Pages: fstest.MapFS{}, Port: 3131, Token: "tok"})
	r := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:3131"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if rec.Code != 404 {
		t.Errorf("no panel: %d", rec.Code)
	}
}
