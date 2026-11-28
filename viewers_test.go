package main

// Viewer counts on the real binary: a page connecting asks for counts at
// once (it doesn't wait for the 30 s tick), Twitch is asked with the saved
// sign-in, and the answer rides the event stream and /api/state.

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestViewerCountsRideTheEventStreamFromTheSavedSignIn(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	tw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if r.URL.Path == "/helix/streams" {
			io.WriteString(w, `{"data":[{"user_login":"examplechannel","type":"live","viewer_count":42}]}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer tw.Close()

	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"port": %d, "sources": [{"id": "s1", "url": "https://www.twitch.tv/examplechannel"}], "twitchClientId": "fakeclientid0123456789abcdefgh"}`, freePort(t))
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	far := time.Now().Add(4 * time.Hour).UnixMilli()
	sec := fmt.Sprintf(`{"twitch": {"accessToken": "FAKE_ACCESS_1", "refreshToken": "FAKE_REFRESH_1", "expiresAt": %d, "scopes": ["user:write:chat"], "userId": "1001", "login": "examplechannel", "displayName": "ExampleChannel"}}`, far)
	if err := os.WriteFile(filepath.Join(dir, "secrets.json"), []byte(sec), 0o600); err != nil {
		t.Fatal(err)
	}
	a := startIn(t, dir, "DUPLEXCHAT_DIR="+dir, "DUPLEXCHAT_TWITCH_HTTP="+tw.URL)

	want := `{"twitch":{"state":"live","count":42,"note":""},"youtube":{"state":"none","count":null,"note":""}}`
	if e := viewersEvent(t, a.port); e != `{"type":"viewers","viewers":`+want+`}` {
		t.Fatalf("event %s", e)
	}
	_, _, state := a.get(t, "/api/state", "")
	if !strings.Contains(state, `"viewers":`+want) {
		t.Errorf("/api/state %s", state)
	}
	mu.Lock()
	if len(asked) != 1 || asked[0] != "GET /helix/streams?user_login=examplechannel Bearer FAKE_ACCESS_1" {
		t.Errorf("Twitch was asked %q", asked)
	}
	mu.Unlock()

	// A second copy on the same folder doesn't own sending, so it doesn't
	// touch the sign-in for counts either, and says why.
	b := startIn(t, dir, "DUPLEXCHAT_DIR="+dir, "DUPLEXCHAT_TWITCH_HTTP="+tw.URL)
	second := `{"twitch":{"state":"unknown","count":null,"note":"Viewer counts for Twitch come from the copy of the server that owns sending."},"youtube":{"state":"none","count":null,"note":""}}`
	if e := viewersEvent(t, b.port); e != `{"type":"viewers","viewers":`+second+`}` {
		t.Fatalf("second copy: %s", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 {
		t.Errorf("the second copy asked Twitch: %q", asked)
	}
}

// viewersEvent connects a page and returns the first viewers event's data.
func viewersEvent(t *testing.T, port int) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/events", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, `data: {"type":"viewers"`) {
				found <- line[6:]
				return
			}
		}
		close(found)
	}()
	select {
	case e, ok := <-found:
		if !ok {
			t.Fatal("event stream ended")
		}
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("no viewers event in 10 s")
	}
	return ""
}
