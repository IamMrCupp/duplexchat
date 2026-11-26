package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// One file per page, each with distinct content, so a route that serves the
// wrong file fails.
var testPages = fstest.MapFS{
	"panel.html":         {Data: []byte("panel")},
	"dock.html":          {Data: []byte("dock")},
	"setup.html":         {Data: []byte("setup")},
	"info.html":          {Data: []byte("info")},
	"overlay.html":       {Data: []byte("overlay")},
	"overlay.css":        {Data: []byte("overlay-css")},
	"info-logic.js":      {Data: []byte("info-logic")},
	"dock-logic.js":      {Data: []byte("dock-logic")},
	"viewer-bar.js":      {Data: []byte("viewer-bar")},
	"overlay-options.js": {Data: []byte("overlay-options")},
	"favicon.svg":        {Data: []byte("<svg/>")},
	"secret.txt":         {Data: []byte("never served")},
}

func TestStaticRoutes(t *testing.T) {
	const (
		html = "text/html; charset=utf-8"
		css  = "text/css; charset=utf-8"
		js   = "text/javascript; charset=utf-8"
		svg  = "image/svg+xml"
	)
	cases := []struct {
		path, body, ctype string
	}{
		{"/", "panel", html},
		{"/dock", "dock", html},
		{"/setup", "setup", html},
		{"/info", "info", html},
		{"/overlay", "overlay", html},
		{"/overlay.css", "overlay-css", css},
		{"/info-logic.js", "info-logic", js},
		{"/dock-logic.js", "dock-logic", js},
		{"/viewer-bar.js", "viewer-bar", js},
		{"/overlay-options.js", "overlay-options", js},
		{"/favicon.svg", "<svg/>", svg},
	}
	h := New(Options{Pages: testPages})
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			res := rec.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			if got := res.Header.Get("Content-Type"); got != c.ctype {
				t.Errorf("Content-Type = %q, want %q", got, c.ctype)
			}
			if got := res.Header.Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
			body, _ := io.ReadAll(res.Body)
			if string(body) != c.body {
				t.Errorf("body = %q, want %q", body, c.body)
			}
		})
	}
}

func TestFaviconICO(t *testing.T) {
	rec := httptest.NewRecorder()
	New(Options{Pages: testPages}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestNotFound(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodGet, "/panel.html"},    // files are reachable only by their route
		{http.MethodGet, "/secret.txt"},    // nothing outside the route table
		{http.MethodGet, "/dock/"},         // exact paths, as in the source
		{http.MethodGet, "/../panel.html"}, // no traversal
		{http.MethodGet, "/nope"},
		{http.MethodPost, "/"}, // static routes are GET only
		{http.MethodHead, "/dock"},
	}
	h := New(Options{Pages: testPages})
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if c.method != http.MethodHead && rec.Body.String() != "not found" {
				t.Errorf("body = %q, want %q", rec.Body.String(), "not found")
			}
		})
	}
}

func TestMissingFileIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	New(Options{Pages: fstest.MapFS{}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dock", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
