package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestState(t *testing.T) {
	s := New(Options{Pages: fstest.MapFS{}, State: func() State {
		return State{Version: "1.2.3", Host: "127.0.0.1", Port: 3131, LAN: []string{}, Sources: []any{}, Viewers: map[string]any{}}
	}})
	rec := get(t, s, "/api/state")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	want := `{"version":"1.2.3","host":"127.0.0.1","port":3131,"lan":[],"sources":[],"viewers":{}}`
	if rec.Body.String() != want {
		t.Errorf("body = %s\nwant   %s", rec.Body.String(), want)
	}
	// reads aren't gated: a LAN Host still gets an answer
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.Host = "some-lan-name:3131"
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("LAN Host: status %d", rec.Code)
	}
}

func TestEventsRoute(t *testing.T) {
	called := false
	s := New(Options{Pages: fstest.MapFS{}, Events: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })})
	get(t, s, "/events")
	if !called {
		t.Error("/events didn't reach the hub")
	}
	if rec := get(t, New(Options{Pages: fstest.MapFS{}}), "/events"); rec.Code != 404 {
		t.Errorf("no hub: status %d, want 404", rec.Code)
	}
}

func TestFonts(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"Zed.woff2", "abc.TTF", "b.otf", "c.woff", "notes.txt", "x.ttf.bak"} {
		os.WriteFile(filepath.Join(dir, f), []byte("font:"+f), 0o644)
	}
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "in.ttf"), []byte("x"), 0o644)
	s := New(Options{Pages: fstest.MapFS{}, FontsDir: dir})

	rec := get(t, s, "/api/fonts")
	if rec.Body.String() != `["Zed.woff2","abc.TTF","b.otf","c.woff"]` {
		t.Errorf("/api/fonts = %s", rec.Body.String())
	}
	rec = get(t, s, "/fonts/abc.TTF")
	if rec.Code != 200 || rec.Body.String() != "font:abc.TTF" || rec.Header().Get("Content-Type") != "font/ttf" || rec.Header().Get("Cache-Control") != "max-age=3600" {
		t.Errorf("/fonts/abc.TTF: %d %q %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	if rec := get(t, s, "/fonts/Zed.woff2"); rec.Header().Get("Content-Type") != "font/woff2" {
		t.Errorf("woff2 type = %q", rec.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/fonts/notes.txt", "/fonts/missing.ttf", "/fonts/sub%2Fin.ttf", "/fonts/..%2F..%2Fetc.ttf", "/fonts/a%5Cb.ttf", "/fonts/x..ttf"} {
		if rec := get(t, s, p); rec.Code != 404 || rec.Body.String() != "not found" {
			t.Errorf("%s: %d %q, want 404", p, rec.Code, rec.Body.String())
		}
	}
	if rec := get(t, New(Options{Pages: fstest.MapFS{}}), "/api/fonts"); rec.Body.String() != "[]" {
		t.Errorf("no fonts dir: %s", rec.Body.String())
	}
	if rec := get(t, New(Options{Pages: fstest.MapFS{}, FontsDir: filepath.Join(dir, "missing")}), "/api/fonts"); rec.Body.String() != "[]" {
		t.Errorf("missing fonts dir: %s", rec.Body.String())
	}
}

func TestPanicBecomes500AndIsLogged(t *testing.T) {
	var logged []string
	s := New(Options{Pages: fstest.MapFS{}, Log: func(kind string, v any) { logged = append(logged, kind) },
		State: func() State { panic("boom") }})
	rec := get(t, s, "/api/state")
	if rec.Code != 500 || rec.Body.String() != `{"error":"boom"}` {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(logged) != 1 || logged[0] != "http" {
		t.Errorf("logged = %v", logged)
	}
}

func TestPort(t *testing.T) {
	good := map[any]int{float64(3131): 3131, "4000": 4000, " 80 ": 80, float64(0): 0}
	for in, want := range good {
		if got, err := Port(in); err != nil || got != want {
			t.Errorf("Port(%#v) = %d, %v", in, got, err)
		}
	}
	for _, in := range []any{nil, "abc", float64(70000), float64(1.5), float64(-1), true} {
		if _, err := Port(in); err == nil {
			t.Errorf("Port(%#v): want an error", in)
		}
	}
}

func TestListenFallsBack(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	var said []string
	l, got, err := Listen("127.0.0.1", port, 20, func(s string) { said = append(said, s) })
	if err != nil {
		t.Fatalf("Listen: %v (port %d)", err, port)
	}
	defer l.Close()
	if got <= port || got > port+20 {
		t.Errorf("listened on %d, want after %d", got, port)
	}
	if len(said) == 0 || !strings.HasPrefix(said[0], "Port ") || !strings.Contains(said[0], "is busy") {
		t.Errorf("said %q", said)
	}
	if _, _, err := Listen("127.0.0.1", port, 0, func(string) {}); err == nil {
		t.Error("no tries left: want the error")
	}
	if _, _, err := Listen("256.0.0.1", 3131, 20, func(string) { t.Error("retried a non-busy error") }); err == nil {
		t.Error("bad host: want an error")
	}
}

func TestLANOrigins(t *testing.T) {
	old := Interfaces
	defer func() { Interfaces = old }()
	Interfaces = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
			&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(8, 32)},
		}, nil
	}
	cases := []struct {
		host any
		want string
	}{
		{"127.0.0.1", ""},
		{"localhost", ""},
		{"192.168.1.99", "http://192.168.1.99:3131"},
		{"0.0.0.0", "http://192.168.1.20:3131,http://10.0.0.5:3131"},
		{"::", "http://192.168.1.20:3131,http://10.0.0.5:3131"},
		{"", "http://192.168.1.20:3131,http://10.0.0.5:3131"},
		{nil, "http://192.168.1.20:3131,http://10.0.0.5:3131"},
	}
	for _, c := range cases {
		if got := strings.Join(LANOrigins(c.host, 3131), ","); got != c.want {
			t.Errorf("LANOrigins(%#v) = %q, want %q", c.host, got, c.want)
		}
	}
	Interfaces = func() ([]net.Addr, error) { return nil, errors.New("no") }
	if got := LANOrigins("0.0.0.0", 1); got == nil || len(got) != 0 {
		t.Errorf("interface error: %#v, want []", got)
	}
	b, _ := json.Marshal(LANOrigins("127.0.0.1", 1))
	if string(b) != "[]" {
		t.Errorf("loopback marshals as %s, want []", b)
	}
}

func TestBanner(t *testing.T) {
	b := Banner(4321, []string{"http://192.168.1.20:4321"}, []string{"a note"})
	for _, re := range []string{`http://127\.0\.0\.1:(\d+)/dock`, `Stream info:\s+http://127.0.0.1:\d+/info`, `On the local network: http://192\.168\.1\.20:4321/`, `a note`} {
		if !regexp.MustCompile(re).MatchString(b) {
			t.Errorf("banner doesn't match %s:\n%s", re, b)
		}
	}
	if regexp.MustCompile(`\p{Cyrillic}`).MatchString(b) {
		t.Error("banner has Cyrillic")
	}
	if strings.Contains(Banner(1, nil, nil), "local network") {
		t.Error("no LAN line expected")
	}
}
