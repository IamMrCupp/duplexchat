package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/golden"
	"github.com/IamMrCupp/duplexchat/internal/sources"
)

// A whole reader against a fake YouTube: resolve, init, seed, poll — and
// Stop ends it.
func TestReaderRuns(t *testing.T) {
	watch, _ := os.ReadFile(golden.Path("youtube", "pages", "watch-live.html"))
	chat, _ := os.ReadFile(golden.Path("youtube", "pages", "live-chat.html"))
	var polls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/watch":
			w.Write(watch)
		case r.URL.Path == "/live_chat":
			w.Write(chat)
		case r.URL.Path == "/oembed":
			w.Write([]byte(`{"author_name":"Example Channel"}`))
		case strings.HasPrefix(r.URL.Path, "/youtubei/v1/live_chat/get_live_chat"):
			mu.Lock()
			polls++
			mu.Unlock()
			w.Write([]byte(`{"continuationContents":{"liveChatContinuation":{"continuations":[{"timedContinuationData":{"continuation":"N","timeoutMs":1000}}],"actions":[{"addChatItemAction":{"item":{"liveChatTextMessageRenderer":{"id":"P1","message":{"runs":[{"text":"from a poll"}]},"authorName":{"simpleText":"Poller"}}}}}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var got struct {
		sync.Mutex
		msgs, status []string
		meta         string
	}
	h := app.Handlers{
		Message: func(f []app.Field) { got.Lock(); got.msgs = append(got.msgs, string(app.Encode(f))); got.Unlock() },
		Status:  func(s, d string) { got.Lock(); got.status = append(got.status, s+":"+d); got.Unlock() },
		Meta:    func(c string) { got.Lock(); got.meta = c; got.Unlock() },
		Remove:  func(string) {}, RemoveAuthor: func(string) {}, Clear: func() {},
	}
	ad, ch, err := Factory(srv.URL)(sources.Entry{URL: "https://www.youtube.com/watch?v=AAAAAAAAAAA"}, h)
	if err != nil || ch != "" {
		t.Fatalf("factory: %v %q", err, ch)
	}
	r := ad.(*Reader)
	r.Sleep = func(ctx context.Context, d time.Duration) bool {
		select {
		case <-time.After(5 * time.Millisecond):
			return true
		case <-ctx.Done():
			return false
		}
	}
	r.Start()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got.Lock()
		n, meta := len(got.msgs), got.meta
		got.Unlock()
		mu.Lock()
		p := polls
		mu.Unlock()
		if n >= 2 && meta != "" && p >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Stop()
	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("reader didn't stop")
	}
	got.Lock()
	defer got.Unlock()
	if len(got.msgs) != 2 || !strings.Contains(got.msgs[0], "seed message") || !strings.Contains(got.msgs[1], "from a poll") {
		t.Errorf("messages = %v (the second poll's repeat of P1 must be dropped)", got.msgs)
	}
	if got.meta != "Example Channel" {
		t.Errorf("meta = %q", got.meta)
	}
	if strings.Join(got.status, "|") != "connecting:looking for the stream…|live:chat connected" {
		t.Errorf("status = %v", got.status)
	}
	mu.Lock()
	if polls < 2 {
		t.Errorf("polled %d times", polls)
	}
	mu.Unlock()
	if r.OwnerChannelID != "UCfakeownerfakeownerfake" || r.VideoID != "AAAAAAAAAAA" {
		t.Errorf("owner %q video %q", r.OwnerChannelID, r.VideoID)
	}
}

func TestFallbackContinuationWindow(t *testing.T) {
	tail := `"continuations":[{"reloadContinuationData":{"continuation":"TOK"}}]`
	near := `"liveChatRenderer": {` + strings.Repeat("x", 3990) + tail
	far := `"liveChatRenderer": {` + strings.Repeat("x", 4010) + tail
	if got := fallbackContinuation(near); got != "TOK" {
		t.Errorf("within 4000: %q", got)
	}
	if got := fallbackContinuation(far); got != "" {
		t.Errorf("beyond 4000: %q", got)
	}
	if got := fallbackContinuation(far + `"liveChatRenderer":{` + tail); got != "TOK" {
		t.Errorf("a later renderer close enough: %q", got)
	}
}
