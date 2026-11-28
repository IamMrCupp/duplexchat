package main

// The source's server.test.js cases that apply before sending exists, run
// against the real binary as the source runs `node server.js`.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "duplexchat-bin-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "duplexchat")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type instance struct {
	port int
	dir  string
	out  *syncBuf
	cmd  *exec.Cmd
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

var dockLine = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/dock`)

// start runs the binary in a fresh folder holding config (nil: no file) and
// waits for the banner, as the source's startServer helper does.
func start(t *testing.T, config map[string]any, env ...string) *instance {
	t.Helper()
	dir := t.TempDir()
	if config != nil {
		b, _ := json.Marshal(config)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return startIn(t, dir, append([]string{"DUPLEXCHAT_DIR=" + dir}, env...)...)
}

// offline keeps the binary off the internet: Twitch and YouTube go
// to a port nothing listens on.
var offline = []string{"DUPLEXCHAT_TWITCH_IRC=127.0.0.1:1", "DUPLEXCHAT_TWITCH_HTTP=http://127.0.0.1:1", "DUPLEXCHAT_YOUTUBE_ORIGIN=http://127.0.0.1:1"}

func startIn(t *testing.T, dir string, env ...string) *instance {
	t.Helper()
	a := &instance{dir: dir, out: &syncBuf{}}
	a.cmd = exec.Command(binary)
	a.cmd.Env = append(append(os.Environ(), offline...), env...)
	a.cmd.Stdout, a.cmd.Stderr = a.out, a.out
	if err := a.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.cmd.Process.Kill(); a.cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := dockLine.FindStringSubmatch(a.out.String()); m != nil {
			fmt.Sscan(m[1], &a.port)
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no banner in 10 s:\n%s", a.out.String())
	return nil
}

func (a *instance) get(t *testing.T, path string, host string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", a.port, path), nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func (a *instance) state(t *testing.T) map[string]any {
	t.Helper()
	_, _, body := a.get(t, "/api/state", "")
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("/api/state: %v: %s", err, body)
	}
	return v
}

// firstEvent reads /events up to the first data frame.
func (a *instance) firstEvent(t *testing.T) map[string]any {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/events", a.port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			var v map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &v); err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatal("no event")
	return nil
}

// Source case 1: dock and setup pages are served, overlay routes unchanged,
// and reads stay open to a LAN Host.
func TestPagesServed(t *testing.T) {
	a := start(t, map[string]any{"host": "127.0.0.1", "port": freePort(t), "sources": []any{}})
	for path, marker := range map[string]string{"/dock": `id="composer"`, "/setup": "Sending setup", "/overlay": `id="chat"`, "/": `id="rows"`} {
		code, _, body := a.get(t, path, "")
		if code != 200 || !strings.Contains(body, marker) {
			t.Errorf("%s: %d, has %q: %v", path, code, marker, strings.Contains(body, marker))
		}
	}
	if s := a.state(t); fmt.Sprint(s["sources"]) != "[]" {
		t.Errorf("sources = %v", s["sources"])
	}
	if code, _, _ := a.get(t, "/api/state", fmt.Sprintf("some-lan-name:%d", a.port)); code != 200 {
		t.Errorf("/api/state with a LAN Host: %d", code)
	}
	if regexp.MustCompile(`\p{Cyrillic}`).MatchString(a.out.String()) {
		t.Errorf("output has Cyrillic:\n%s", a.out.String())
	}
}

// Source case 3: binds loopback by default.
func TestLoopbackByDefault(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	s := a.state(t)
	if s["host"] != "127.0.0.1" || fmt.Sprint(s["lan"]) != "[]" {
		t.Errorf("host %v, lan %v", s["host"], s["lan"])
	}
	if strings.Contains(a.out.String(), "On the local network") {
		t.Errorf("printed a LAN line:\n%s", a.out.String())
	}
	if s["version"] != "dev" || s["port"] != float64(a.port) {
		t.Errorf("version %v, port %v", s["version"], s["port"])
	}
}

// Source case 10 (the parts before viewer counting): viewers ride the event
// stream as "none", and the bar's files and CSS are served.
func TestViewersAndOverlayFiles(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	hello := a.firstEvent(t)
	none := map[string]any{"state": "none", "count": nil, "note": ""}
	want := map[string]any{"twitch": none, "youtube": none}
	if fmt.Sprint(hello["viewers"]) != fmt.Sprint(want) {
		t.Errorf("hello viewers = %v", hello["viewers"])
	}
	if fmt.Sprint(a.state(t)["viewers"]) != fmt.Sprint(want) {
		t.Errorf("state viewers = %v", a.state(t)["viewers"])
	}
	checks := map[string][]string{
		"/viewer-bar.js":      {"ViewerBar"},
		"/overlay?viewers=1":  {`id="viewers"`, "/viewer-bar.js", "/overlay-options.js"},
		"/overlay-options.js": {"OverlayOptions"},
		"/overlay.css":        {"--bg: rgba(10, 10, 14, var(--bg-opacity))", "--viewers-", ".vchip.inside"},
		"/dock-logic.js":      {"DockLogic"},
		"/info":               {`id="form"`, "/info-logic.js"},
		"/info-logic.js":      {"InfoLogic"},
	}
	for path, wants := range checks {
		code, hdr, body := a.get(t, path, "")
		if code != 200 {
			t.Errorf("%s: %d", path, code)
		}
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s doesn't contain %q", path, w)
			}
		}
		if strings.HasSuffix(path, ".js") && !strings.Contains(hdr.Get("Content-Type"), "javascript") {
			t.Errorf("%s: Content-Type %q", path, hdr.Get("Content-Type"))
		}
	}
	if !regexp.MustCompile(`Stream info:\s+http://127.0.0.1:\d+/info`).MatchString(a.out.String()) {
		t.Errorf("no Stream info line:\n%s", a.out.String())
	}
}

// Port busy: the next one is used and the banner shows the real port.
func TestPortFallback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	a := start(t, map[string]any{"port": port, "sources": []any{}})
	if a.port == port {
		t.Fatalf("banner shows the busy port %d", port)
	}
	if !strings.Contains(a.out.String(), fmt.Sprintf("Port %d is busy", port)) {
		t.Errorf("no busy-port line:\n%s", a.out.String())
	}
	if s := a.state(t); s["port"] != float64(a.port) {
		t.Errorf("/api/state port = %v, want %d", s["port"], a.port)
	}
}

// Drop-in: an existing config with sources and settings loads, its sources
// show up, and starting doesn't rewrite the file.
func TestExistingConfigLoadsUntouched(t *testing.T) {
	dir := t.TempDir()
	raw := fmt.Sprintf("\ufeff{\n  \"host\": \"127.0.0.1\",\n  \"port\": %d,\n  \"sources\": [{\"id\": \"old1\", \"url\": \"https://www.twitch.tv/examplechannel\"}, {\"id\": \"old2\", \"url\": \"https://www.youtube.com/@ExampleChannel\"}, {\"url\": \"https://example.com/nope\"}],\n  \"twitchClientId\": \"abc\",\n  \"youtubeViewers\": \"api\",\n  \"infoPresets\": [{\"name\": \"Repair night\"}],\n  \"replyTo\": {\"twitch\": \"https://www.twitch.tv/examplechannel\"}\n}", freePort(t))
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(raw), 0o644)
	a := startIn(t, dir, "MULTICHAT_DIR="+dir) // multichat's variable still works
	var plats []string
	for _, s := range a.state(t)["sources"].([]any) {
		m := s.(map[string]any)
		plats = append(plats, m["platform"].(string)+"/"+m["state"].(string))
		if id := m["id"].(string); !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(id) {
			t.Errorf("source id %q isn't 8 hex characters", id)
		}
	}
	// offline, the readers can't connect: Twitch keeps retrying (connecting),
	// YouTube says it can't reach YouTube (error) between tries
	if len(plats) != 2 || plats[0] != "twitch/connecting" || (plats[1] != "youtube/connecting" && plats[1] != "youtube/error") {
		t.Errorf("sources = %v", plats)
	}
	if !strings.Contains(a.out.String(), "Source https://example.com/nope:") {
		t.Errorf("bad source not reported:\n%s", a.out.String())
	}
	if b, _ := os.ReadFile(path); string(b) != raw {
		t.Errorf("config.json was rewritten at startup:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "fonts")); err != nil {
		t.Errorf("fonts/ not created: %v", err)
	}
}

// A port that can't be bound is logged to error.log and the process exits 1.
func TestBadPortExits(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"port":"not a port"}`), 0o644)
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "DUPLEXCHAT_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("exit: %v\n%s", err, out)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "error.log"))
	if !regexp.MustCompile(`^\[\d{4}-\d\d-\d\dT[\d:.]+Z\] listen: `).Match(log) {
		t.Errorf("error.log = %q", log)
	}
}

func TestWindowFlagAccepted(t *testing.T) {
	// --window with --version: the flag parses, nothing opens
	out, err := exec.Command(binary, "--window", "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "dev" {
		t.Errorf("--window --version: %q, %v", out, err)
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := exec.Command(binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "dev" {
		t.Errorf("--version: %q, %v", out, err)
	}
}

// Source case 2 (the part before sending): the session hands out a token to
// this server's own pages and refuses a foreign Host.
func TestSessionFromBinary(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	code, hdr, body := a.get(t, "/api/dock/session", "")
	var s struct {
		Token     string                    `json:"token"`
		Platforms map[string]map[string]any `json:"platforms"`
	}
	json.Unmarshal([]byte(body), &s)
	if code != 200 || len(s.Token) < 43 || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("session: %d %q %s", code, hdr.Get("Cache-Control"), body)
	}
	// Twitch sending is in the build (#11); YouTube's comes with #12
	for p, available := range map[string]bool{"twitch": true, "youtube": false} {
		if s.Platforms[p]["signedIn"] != false || s.Platforms[p]["available"] != available {
			t.Errorf("%s: %v", p, s.Platforms[p])
		}
	}
	if code, _, body := a.get(t, "/api/dock/session", fmt.Sprintf("evil.example:%d", a.port)); code != 403 || !strings.Contains(body, "bad_host") {
		t.Errorf("evil Host: %d %s", code, body)
	}
	_, _, again := a.get(t, "/api/dock/session", "")
	if !strings.Contains(again, s.Token) {
		t.Error("the token changed within one run")
	}
}

func (a *instance) token(t *testing.T) string {
	t.Helper()
	_, _, body := a.get(t, "/api/dock/session", "")
	var s struct{ Token string }
	json.Unmarshal([]byte(body), &s)
	if s.Token == "" {
		t.Fatalf("no token: %s", body)
	}
	return s.Token
}

// do sends a request with exactly these headers (Host defaults to the
// server's own), as the source's request() helper does.
func (a *instance) do(t *testing.T, method, path string, headers map[string]string, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", a.port, path), strings.NewReader(body))
	for k, v := range headers {
		if k == "host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	if body != "" && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Source case 4: "the panel's controls are gated like sending".
func TestPanelGated(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	token := a.token(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", a.port)
	good := map[string]string{"origin": origin, "x-dock-token": token}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range good {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	writes := []struct{ method, path, body string }{
		{"POST", "/api/sources", `{"url":"https://www.twitch.tv/someoneelse"}`},
		{"DELETE", "/api/sources/abcd1234", ""},
		{"POST", "/api/sources/abcd1234/reconnect", ""},
		{"POST", "/api/clear", ""},
		{"POST", "/api/test", ""},
	}
	refusals := []struct {
		headers map[string]string
		code    string
	}{
		{map[string]string{}, "bad_origin"},
		{map[string]string{"origin": "https://evil.example", "content-type": "text/plain"}, "bad_origin"},
		{with(map[string]string{"sec-fetch-site": "cross-site"}), "bad_origin"},
		{map[string]string{"origin": origin}, "no_token"},
		{with(map[string]string{"x-dock-token": "wrong"}), "bad_token"},
		{with(map[string]string{"host": fmt.Sprintf("192.168.1.20:%d", a.port)}), "bad_host"},
		{with(map[string]string{"x-forwarded-for": "203.0.113.9"}), "proxied"},
	}
	for _, w := range writes {
		for _, r := range refusals {
			code, body := a.do(t, w.method, w.path, r.headers, w.body)
			var v map[string]string
			json.Unmarshal([]byte(body), &v)
			if code != 403 || v["code"] != r.code {
				t.Errorf("%s %s with %v: %d %s, want 403 %s", w.method, w.path, r.headers, code, body, r.code)
			}
		}
	}
	if s := a.state(t); fmt.Sprint(s["sources"]) != "[]" {
		t.Errorf("a refused write changed the sources: %v", s["sources"])
	}
	if hist := a.firstEvent(t)["history"].([]any); len(hist) != 0 {
		t.Errorf("a refused test button still posted: %v", hist)
	}

	code, body := a.do(t, "POST", "/api/sources", good, `{"url":"https://example.com/not-a-stream"}`)
	if code != 400 || !strings.Contains(body, `"error"`) || strings.Contains(body, `"code"`) {
		t.Errorf("not a stream: %d %s", code, body)
	}
	if code, _ := a.do(t, "POST", "/api/clear", good, ""); code != 200 {
		t.Errorf("clear: %d", code)
	}
	if code, body := a.do(t, "DELETE", "/api/sources/abcd1234", good, ""); code != 404 || body != `{"ok":true}` {
		t.Errorf("delete unknown: %d %s", code, body)
	}
	if code, _, _ := a.get(t, "/overlay", fmt.Sprintf("192.168.1.20:%d", a.port)); code != 200 {
		t.Errorf("/overlay with a LAN Host: %d", code)
	}
	if code, _ := a.do(t, "DELETE", "/api/sources/NOT-HEX", good, ""); code != 404 {
		t.Errorf("non-hex id: %d", code)
	}
}

// Source case 5: "the test button sends English samples that say they are a test".
func TestTestButton(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	good := map[string]string{"origin": fmt.Sprintf("http://127.0.0.1:%d", a.port), "x-dock-token": a.token(t)}
	if code, body := a.do(t, "POST", "/api/test", good, ""); code != 200 || body != `{"ok":true}` {
		t.Fatalf("test: %d %s", code, body)
	}
	var kinds []string
	for _, m := range a.firstEvent(t)["history"].([]any) {
		mm := m.(map[string]any)
		kinds = append(kinds, fmt.Sprintf("%s/%s", mm["platform"], mm["kind"]))
	}
	if strings.Join(kinds, ",") != "twitch/chat,youtube/chat,twitch/chat,youtube/super,twitch/system" {
		t.Errorf("history = %v", kinds)
	}
}

// Adding and removing from the panel: the source shows up, config.json is
// saved, and removing it saves again.
func TestPanelAddRemoveSaves(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	good := map[string]string{"origin": fmt.Sprintf("http://127.0.0.1:%d", a.port), "x-dock-token": a.token(t)}
	code, body := a.do(t, "POST", "/api/sources", good, `{"url":"  https://www.twitch.tv/examplechannel  "}`)
	var added struct {
		Source struct{ ID, URL, Platform string }
	}
	json.Unmarshal([]byte(body), &added)
	if code != 200 || added.Source.URL != "https://www.twitch.tv/examplechannel" || added.Source.Platform != "twitch" {
		t.Fatalf("add: %d %s", code, body)
	}
	saved, _ := os.ReadFile(filepath.Join(a.dir, "config.json"))
	if !strings.Contains(string(saved), `"url": "https://www.twitch.tv/examplechannel"`) || !strings.Contains(string(saved), `"replyTo"`) {
		t.Errorf("config.json after add:\n%s", saved)
	}
	if code, _ := a.do(t, "POST", "/api/sources/"+added.Source.ID+"/reconnect", good, ""); code != 200 {
		t.Errorf("reconnect: %d", code)
	}
	if code, _ := a.do(t, "DELETE", "/api/sources/"+added.Source.ID, good, ""); code != 200 {
		t.Errorf("delete: %d", code)
	}
	saved, _ = os.ReadFile(filepath.Join(a.dir, "config.json"))
	if strings.Contains(string(saved), "examplechannel") {
		t.Errorf("config.json after delete:\n%s", saved)
	}
}

// Two copies on one folder: the second says sending is off and names the
// first; the first gives the lock back when it's stopped.
func TestSecondCopyDoesNotSend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs SIGINT")
	}
	dir := t.TempDir()
	first := startIn(t, dir, "DUPLEXCHAT_DIR="+dir)
	if strings.Contains(first.out.String(), "Sending is off") {
		t.Fatalf("first copy can't send:\n%s", first.out.String())
	}
	second := startIn(t, dir, "DUPLEXCHAT_DIR="+dir)
	want := fmt.Sprintf("  Sending is off in this copy: another copy of the server (process %d) is already running from this folder and owns sending. Use that one's dock, or stop it.\n", first.cmd.Process.Pid)
	if !strings.Contains(second.out.String(), want) {
		t.Fatalf("second banner:\n%s", second.out.String())
	}
	second.cmd.Process.Signal(os.Interrupt)
	second.cmd.Wait()
	lock := filepath.Join(dir, "sending.lock")
	if b, _ := os.ReadFile(lock); string(b) != fmt.Sprint(first.cmd.Process.Pid) {
		t.Fatalf("the second copy touched the lock: %q", b)
	}
	first.cmd.Process.Signal(os.Interrupt)
	first.cmd.Wait()
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock left behind after a clean stop")
	}
}

// The dock picks a reply target: the session shows it, and config.json
// keeps it by URL for the next start (multichat's onChange saves).
func TestDockTargetPickIsShownAndSaved(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{
		map[string]any{"id": "x", "url": "https://twitch.tv/examplechannel"},
		map[string]any{"id": "y", "url": "https://twitch.tv/second"},
	}})
	var s struct {
		Platforms map[string]struct {
			Connected bool
			Target    *string
			TargetKey *string
			Choices   []struct{ ID, Label string }
		}
	}
	_, _, body := a.get(t, "/api/dock/session", "")
	json.Unmarshal([]byte(body), &s)
	tw := s.Platforms["twitch"]
	// no replyTo saved: the first source was the only one when it was added,
	// so it's pinned; both are offered
	if !tw.Connected || *tw.Target != "#examplechannel" || len(tw.Choices) != 2 || tw.Choices[1].Label != "#second" {
		t.Fatalf("before: %s", body)
	}
	headers := map[string]string{"origin": fmt.Sprintf("http://127.0.0.1:%d", a.port), "x-dock-token": a.token(t)}
	code, out := a.do(t, "POST", "/api/dock/target", headers, fmt.Sprintf(`{"platform":"twitch","source":%q}`, tw.Choices[1].ID))
	if code != 200 || !strings.Contains(out, `"target":"#second"`) || !strings.Contains(out, `"targetKey":"`+tw.Choices[1].ID+`:second"`) {
		t.Fatalf("pick: %d %s", code, out)
	}
	cfg, _ := os.ReadFile(filepath.Join(a.dir, "config.json"))
	if !strings.Contains(string(cfg), "\"replyTo\": {\n    \"twitch\": \"https://twitch.tv/second\"\n  }") {
		t.Fatalf("config.json:\n%s", cfg)
	}
	if code, out := a.do(t, "POST", "/api/dock/target", headers, `{"platform":"twitch","source":"nope"}`); code != 400 || !strings.Contains(out, "bad_target") {
		t.Errorf("bad pick: %d %s", code, out)
	}
}

// The second copy's session says sending is off, and why, on each platform.
func TestSecondCopySessionSaysWhy(t *testing.T) {
	dir := t.TempDir()
	first := startIn(t, dir, "DUPLEXCHAT_DIR="+dir)
	second := startIn(t, dir, "DUPLEXCHAT_DIR="+dir)
	_, _, body := second.get(t, "/api/dock/session", "")
	want := fmt.Sprintf(`"detail":"Sending is off in this copy: another copy of the server (process %d) is already running`, first.cmd.Process.Pid)
	if strings.Count(body, want) != 2 || strings.Contains(body, `"available":true`) {
		t.Fatalf("second session: %s", body)
	}
	if _, _, body := first.get(t, "/api/dock/session", ""); strings.Contains(body, "Sending is off") {
		t.Fatalf("first session: %s", body)
	}
}

// Source case 2: a gated send that reports honest state. With no sign-in
// both platforms answer, neither sends; the gate refuses the rest.
func TestGatedSendReportsHonestState(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	origin := fmt.Sprintf("http://127.0.0.1:%d", a.port)
	token := a.token(t)
	send := func(headers map[string]string) (int, string) {
		return a.do(t, "POST", "/api/send", headers, `{"target":"both","text":"hello"}`)
	}
	code, body := send(map[string]string{"origin": origin, "x-dock-token": token})
	var out struct{ Results map[string]struct{ OK bool } }
	json.Unmarshal([]byte(body), &out)
	if code != 200 || len(out.Results) != 2 || out.Results["twitch"].OK || out.Results["youtube"].OK {
		t.Fatalf("send: %d %s", code, body)
	}
	for want, h := range map[string]map[string]string{
		"no_token":   {"origin": origin},
		"bad_token":  {"origin": origin, "x-dock-token": "wrong"},
		"bad_origin": {"origin": "https://evil.example", "x-dock-token": token},
		"bad_host":   {"origin": origin, "x-dock-token": token, "host": fmt.Sprintf("evil.example:%d", a.port)},
	} {
		if code, body := send(h); code != 403 || !strings.Contains(body, `"code":"`+want+`"`) {
			t.Errorf("%s: %d %s", want, code, body)
		}
	}
}

// Source case 6: a null body is a 400, not a crash.
func TestSendNullBodyIs400(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	code, body := a.do(t, "POST", "/api/send", map[string]string{"origin": fmt.Sprintf("http://127.0.0.1:%d", a.port), "x-dock-token": a.token(t)}, "null")
	if code != 400 || !strings.Contains(body, `"code":"bad_request"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, hdr, js := a.get(t, "/dock-logic.js", ""); code != 200 || !strings.Contains(hdr.Get("Content-Type"), "javascript") || !strings.Contains(js, "DockLogic") {
		t.Errorf("dock-logic.js: %d", code)
	}
}

// Source cases 7–9, the parts that don't need a platform's sender (those
// come with #11 and #12): /setup's state is gated and says why a second copy
// won't send, a second copy changes nothing, nothing writes secrets.json
// before a sign-in, and a copy that doesn't own sending leaves / to the panel
// even for a redirect-shaped visit.
func TestSetupFromBinary(t *testing.T) {
	dir := t.TempDir()
	first := startIn(t, dir, "DUPLEXCHAT_DIR="+dir)
	second := startIn(t, dir, "DUPLEXCHAT_DIR="+dir)
	tok1, tok2 := first.token(t), second.token(t)

	code, body := first.do(t, "GET", "/api/setup/state", map[string]string{"x-dock-token": tok1}, "")
	var st struct {
		Platforms map[string]map[string]any
		Notices   []string
	}
	json.Unmarshal([]byte(body), &st)
	if code != 200 || len(st.Platforms) != 2 || st.Notices == nil || len(st.Notices) != 0 {
		t.Fatalf("first copy's state: %d %s", code, body)
	}
	if code, body := first.do(t, "GET", "/api/setup/state", nil, ""); code != 403 || !strings.Contains(body, `"no_token"`) {
		t.Errorf("state without a token: %d %s", code, body)
	}
	origin1 := fmt.Sprintf("http://127.0.0.1:%d", first.port)
	if code, _ := first.do(t, "POST", "/api/setup/twitch/client", map[string]string{"origin": origin1, "x-dock-token": "wrong"}, `{"clientId":"x"}`); code != 403 {
		t.Errorf("wrong token: %d", code)
	}

	code, body = second.do(t, "GET", "/api/setup/state", map[string]string{"x-dock-token": tok2}, "")
	json.Unmarshal([]byte(body), &st)
	if code != 200 || len(st.Notices) != 1 || !strings.HasPrefix(st.Notices[0], "Sending is off in this copy: another copy of the server") {
		t.Fatalf("second copy's state: %d %s", code, body)
	}
	origin2 := fmt.Sprintf("http://127.0.0.1:%d", second.port)
	for _, p := range []string{"twitch", "youtube"} {
		code, body := second.do(t, "POST", "/api/setup/"+p+"/client", map[string]string{"origin": origin2, "x-dock-token": tok2}, `{"clientId":"fakeclientid0123456789abcdefgh"}`)
		if code != 409 || !strings.Contains(body, `"code":"sending_disabled"`) {
			t.Errorf("second copy, %s client: %d %s", p, code, body)
		}
	}
	if code, _, body := second.get(t, "/?state=made-up&code=made-up", ""); code != 200 || !strings.Contains(body, `id="rows"`) {
		t.Errorf("second copy's / isn't the panel: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.json")); !os.IsNotExist(err) {
		t.Errorf("secrets.json exists before any sign-in: %v", err)
	}
}

// Source case 7: the Twitch client ID is saved to config.json and survives
// the panel's own saves; there's no secrets file until a sign-in.
func TestTwitchClientIDSavedAndSurvives(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	token := a.token(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", a.port)
	headers := map[string]string{"origin": origin, "x-dock-token": token}
	var st struct{ Platforms map[string]map[string]any }
	_, body := a.do(t, "GET", "/api/setup/state", map[string]string{"x-dock-token": token}, "")
	json.Unmarshal([]byte(body), &st)
	if tw := st.Platforms["twitch"]; tw["available"] != true || tw["configured"] != false || tw["clientId"] != "" {
		t.Fatalf("state: %s", body)
	}

	if code, body := a.do(t, "POST", "/api/setup/twitch/client", headers, `{"clientId":"fakeclientid0123456789abcdefgh"}`); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	cfg := func() map[string]any {
		var c map[string]any
		b, _ := os.ReadFile(filepath.Join(a.dir, "config.json"))
		json.Unmarshal(b, &c)
		return c
	}
	if cfg()["twitchClientId"] != "fakeclientid0123456789abcdefgh" {
		t.Fatalf("config: %v", cfg())
	}
	// the panel's own save path keeps it
	if code, _ := a.do(t, "POST", "/api/clear", headers, ""); code != 200 {
		t.Fatalf("clear: %d", code)
	}
	if code, _ := a.do(t, "DELETE", "/api/sources/abcd1234", headers, ""); code != 404 {
		t.Fatalf("delete: %d", code)
	}
	if code, body := a.do(t, "POST", "/api/sources", headers, `{"url":"https://www.twitch.tv/examplechannel"}`); code != 200 {
		t.Fatalf("add: %d %s", code, body)
	}
	if cfg()["twitchClientId"] != "fakeclientid0123456789abcdefgh" {
		t.Fatalf("lost on a panel save: %v", cfg())
	}
	if _, err := os.Stat(filepath.Join(a.dir, "secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("secrets.json before a sign-in: %v", err)
	}

	if code, _ := a.do(t, "POST", "/api/setup/twitch/client", map[string]string{"origin": origin, "x-dock-token": "wrong"}, `{"clientId":"x"}`); code != 403 {
		t.Fatalf("wrong token: %d", code)
	}
	_, body = a.do(t, "POST", "/api/send", headers, `{"target":"twitch","text":"hi"}`)
	if !strings.Contains(body, `"code":"not_signed_in"`) {
		t.Fatalf("send: %s", body)
	}
}

// Source case 8, the broken-file half: a secrets file that can't be parsed
// is set aside and reported on /setup, and its contents reach neither the
// output nor error.log.
func TestBrokenSecretsFileIsSetAsideAndReported(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	broken := `{"twitch": {"refreshToken": "FAKE_REFRESH_in_a_broken_file"`
	if err := os.WriteFile(filepath.Join(a.dir, "secrets.json"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := a.do(t, "GET", "/api/setup/state", map[string]string{"x-dock-token": a.token(t)}, "")
	var st struct {
		Platforms map[string]map[string]any
		Notices   []string
	}
	json.Unmarshal([]byte(body), &st)
	if st.Platforms["twitch"]["available"] != true || len(st.Notices) != 1 ||
		!regexp.MustCompile(`couldn't be parsed, so it was moved to secrets\.json\.broken-`).MatchString(st.Notices[0]) {
		t.Fatalf("state: %s", body)
	}
	entries, _ := os.ReadDir(a.dir)
	var aside []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "secrets.json.broken-") {
			aside = append(aside, e.Name())
		}
	}
	if len(aside) != 1 {
		t.Fatalf("set aside: %v", aside)
	}
	if b, _ := os.ReadFile(filepath.Join(a.dir, aside[0])); string(b) != broken {
		t.Fatalf("aside holds %q", b)
	}
	if strings.Contains(a.out.String(), "FAKE_REFRESH") {
		t.Fatalf("output: %s", a.out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(a.dir, "error.log")); strings.Contains(string(b), "FAKE_REFRESH") {
		t.Fatalf("error.log: %s", b)
	}
}
