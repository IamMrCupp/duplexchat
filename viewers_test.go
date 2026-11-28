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

// The official YouTube count on the real binary: switched on in config.json
// and signed in, the count comes from videos.list; a second copy on the same
// folder doesn't own the sign-in, so it reads the free count instead and
// spends no quota.
func TestOfficialYouTubeCountOnlyFromTheCopyThatOwnsSending(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/youtube/v3/videos": // Google: the official count
			if r.Header.Get("Authorization") != "Bearer FAKE_YT_ACCESS_1" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, `{"items":[{"id":"FAKEvideo01","liveStreamingDetails":{"actualStartTime":"2026-11-20T10:00:00Z","concurrentViewers":"152"}}]}`)
		case "/youtubei/v1/updated_metadata": // the watch page's interface: the free count
			io.WriteString(w, `{"actions":[{"updateViewershipAction":{"viewCount":{"videoViewCountRenderer":{"isLive":true,"originalViewCount":"7"}}}}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	count := func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, a := range asked {
			if a == key {
				n++
			}
		}
		return n
	}

	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"port": %d, "sources": [{"id": "y1", "url": "https://www.youtube.com/watch?v=FAKEvideo01"}], "youtubeViewers": "api"}`, freePort(t))
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	sec := fmt.Sprintf(`{"youtube": {"clientId": "000000000000-fakefakefakefake.apps.googleusercontent.com", "clientSecret": "FAKE_CLIENT_SECRET_do_not_use", "accessToken": "FAKE_YT_ACCESS_1", "refreshToken": "FAKE_YT_REFRESH_1", "expiresAt": %d, "channelId": "UCfake", "channelTitle": "Example Channel"}}`, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, "secrets.json"), []byte(sec), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"DUPLEXCHAT_DIR=" + dir, "DUPLEXCHAT_GOOGLE_HTTP=" + up.URL, "DUPLEXCHAT_YOUTUBE_ORIGIN=" + up.URL}
	a := startIn(t, dir, env...)
	official := `{"type":"viewers","viewers":{"twitch":{"state":"none","count":null,"note":""},"youtube":{"state":"live","count":152,"note":""}}}`
	if e := viewersEvent(t, a.port); e != official {
		t.Fatalf("first copy: %s", e)
	}
	if count("GET /youtube/v3/videos") != 1 || count("POST /youtubei/v1/updated_metadata") != 0 {
		t.Fatalf("asked %q", asked)
	}

	b := startIn(t, dir, env...)
	free := `{"type":"viewers","viewers":{"twitch":{"state":"none","count":null,"note":""},"youtube":{"state":"live","count":7,"note":""}}}`
	if e := viewersEvent(t, b.port); e != free {
		t.Fatalf("second copy: %s", e)
	}
	if count("GET /youtube/v3/videos") != 1 || count("POST /youtubei/v1/updated_metadata") != 1 {
		t.Fatalf("asked %q", asked)
	}

	// Signed in but not switched on: free, and no quota spent.
	dir2 := t.TempDir()
	cfg2 := fmt.Sprintf(`{"port": %d, "sources": [{"id": "y1", "url": "https://www.youtube.com/watch?v=FAKEvideo01"}]}`, freePort(t))
	os.WriteFile(filepath.Join(dir2, "config.json"), []byte(cfg2), 0o644)
	os.WriteFile(filepath.Join(dir2, "secrets.json"), []byte(sec), 0o600)
	c := startIn(t, dir2, "DUPLEXCHAT_DIR="+dir2, "DUPLEXCHAT_GOOGLE_HTTP="+up.URL, "DUPLEXCHAT_YOUTUBE_ORIGIN="+up.URL)
	if e := viewersEvent(t, c.port); e != free {
		t.Fatalf("switched off: %s", e)
	}
	if count("GET /youtube/v3/videos") != 1 {
		t.Fatalf("asked %q", asked)
	}
}
