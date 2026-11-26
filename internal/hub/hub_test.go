package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func msg(id, source, key string) Msg {
	k := key
	return Msg{ID: id, Source: source, AuthorKey: &k,
		JSON: []byte(fmt.Sprintf(`{"type":"msg","id":%q,"author":{"key":%q},"source":%q}`, id, key, source))}
}

type stream struct {
	t    *testing.T
	sc   *bufio.Scanner
	stop context.CancelFunc
	resp *http.Response
}

// open connects to /events and returns a reader of frames: "data" lines
// decoded as JSON, comment lines as {":": text}.
func open(t *testing.T, srv *httptest.Server) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return &stream{t: t, sc: bufio.NewScanner(resp.Body), stop: cancel, resp: resp}
}

func (s *stream) next() map[string]any {
	s.t.Helper()
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, ":"):
			return map[string]any{":": line[1:]}
		case strings.HasPrefix(line, "data: "):
			var v map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &v); err != nil {
				s.t.Fatalf("bad frame %q: %v", line, err)
			}
			return v
		default:
			s.t.Fatalf("unexpected line %q", line)
		}
	}
	s.t.Fatalf("stream ended: %v", s.sc.Err())
	return nil
}

func newHub(t *testing.T, opts Options) (*Hub, *httptest.Server) {
	t.Helper()
	h := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); cancel() })
	return h, srv
}

func TestHelloAndHeaders(t *testing.T) {
	h, srv := newHub(t, Options{})
	h.Status([]any{map[string]any{"id": "abcd1234"}}) // cached for hello
	h.Publish(msg("tw:1", "abcd1234", "a"))
	s := open(t, srv)
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := s.resp.Header.Get("Cache-Control"); cc != "no-cache, no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if f := s.next(); f[":"] != "ok" {
		t.Fatalf("first frame = %v, want :ok", f)
	}
	hello := s.next()
	if hello["type"] != "hello" {
		t.Fatalf("second frame = %v", hello)
	}
	if hist := hello["history"].([]any); len(hist) != 1 || hist[0].(map[string]any)["id"] != "tw:1" {
		t.Errorf("history = %v", hello["history"])
	}
	if src := hello["sources"].([]any); len(src) != 1 {
		t.Errorf("sources = %v", hello["sources"])
	}
	v, _ := json.Marshal(hello["viewers"])
	if string(v) != `{"twitch":{"count":null,"note":"","state":"none"},"youtube":{"count":null,"note":"","state":"none"}}` { // Go sorts map keys here
		t.Errorf("viewers = %s", v)
	}
}

func TestHelloKeyOrder(t *testing.T) {
	h, srv := newHub(t, Options{})
	_ = h
	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			want := `data: {"type":"hello","history":[],"sources":[],"viewers":{"twitch":{"state":"none","count":null,"note":""},"youtube":{"state":"none","count":null,"note":""}}}`
			if sc.Text() != want {
				t.Errorf("got  %s\nwant %s", sc.Text(), want)
			}
			return
		}
	}
	t.Fatal("no hello")
}

func TestHistoryCap(t *testing.T) {
	h, srv := newHub(t, Options{})
	for i := 0; i < 150; i++ {
		h.Publish(msg(fmt.Sprintf("m%d", i), "s", "k"))
	}
	s := open(t, srv)
	s.next()
	hist := s.next()["history"].([]any)
	if len(hist) != 100 {
		t.Fatalf("history has %d, want 100", len(hist))
	}
	if first := hist[0].(map[string]any)["id"]; first != "m50" {
		t.Errorf("oldest kept = %v, want m50", first)
	}
}

func TestLiveFramesAndEdits(t *testing.T) {
	h, srv := newHub(t, Options{})
	s := open(t, srv)
	s.next()
	s.next()
	h.Publish(msg("a1", "A", "alice"))
	h.Publish(msg("a2", "A", "bob"))
	h.Publish(msg("b1", "B", "alice"))
	h.Publish(msg("b2", "B", "carol"))
	for _, want := range []string{"a1", "a2", "b1", "b2"} {
		if f := s.next(); f["type"] != "msg" || f["id"] != want {
			t.Fatalf("frame = %v, want msg %s", f, want)
		}
	}
	h.Remove("a2")
	h.RemoveAuthor("B", "alice") // only B's alice, not A's
	h.ClearSource("Z")           // unknown source: still broadcast
	checks := []string{
		`{"id":"a2","type":"remove"}`,
		`{"key":"alice","source":"B","type":"removeAuthor"}`,
		`{"source":"Z","type":"clear"}`,
	}
	for _, want := range checks {
		b, _ := json.Marshal(s.next())
		if string(b) != want {
			t.Errorf("frame = %s, want %s", b, want)
		}
	}
	if got := ids(h); got != "a1,b2" {
		t.Errorf("history = %s, want a1,b2", got)
	}
	h.ClearSource("B")
	s.next()
	if got := ids(h); got != "a1" {
		t.Errorf("after clear B: %s", got)
	}
	h.ClearAll()
	if f := s.next(); f["type"] != "clear" || f["source"] != nil {
		t.Errorf("clear all frame = %v", f)
	}
	if got := ids(h); got != "" {
		t.Errorf("after clear all: %s", got)
	}
}

func ids(h *Hub) string {
	var out []string
	for _, m := range h.History() {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

func TestNilAuthorKeyNeverMatches(t *testing.T) {
	h, _ := newHub(t, Options{})
	h.Publish(Msg{ID: "y1", Source: "Y", JSON: []byte(`{"id":"y1"}`)})
	h.RemoveAuthor("Y", "")
	if got := ids(h); got != "y1" {
		t.Errorf("a message without an author key was removed: %s", got)
	}
}

func TestStatusAndViewers(t *testing.T) {
	h, srv := newHub(t, Options{})
	s := open(t, srv)
	s.next()
	s.next()
	h.Status([]any{map[string]any{"id": "x", "state": "live"}})
	h.Viewers(map[string]any{"twitch": map[string]any{"state": "live", "count": 3}})
	if f := s.next(); f["type"] != "status" || len(f["sources"].([]any)) != 1 {
		t.Errorf("status frame = %v", f)
	}
	if f := s.next(); f["type"] != "viewers" || f["viewers"] == nil {
		t.Errorf("viewers frame = %v", f)
	}
}

func TestHeartbeat(t *testing.T) {
	_, srv := newHub(t, Options{Heartbeat: 20 * time.Millisecond})
	s := open(t, srv)
	s.next()
	s.next()
	if f := s.next(); f[":"] != "hb" {
		t.Errorf("frame = %v, want :hb", f)
	}
}

func TestFirstClientCallback(t *testing.T) {
	calls := make(chan int, 4)
	_, srv := newHub(t, Options{OnFirstClient: func() { calls <- 1 }})
	a := open(t, srv)
	a.next()
	b := open(t, srv)
	b.next()
	time.Sleep(20 * time.Millisecond)
	if n := len(calls); n != 1 {
		t.Errorf("OnFirstClient ran %d times, want 1", n)
	}
}

// A client that stops reading is dropped; a client that keeps up gets every
// frame, and publishing never waits on either. The fast client confirms each
// batch before the next is sent, so it's never more than a batch behind its
// queue; the slow client never reads, so once the socket buffers fill its
// queue does too.
func TestSlowClientDropped(t *testing.T) {
	const batch, total = 16, 6000
	h, srv := newHub(t, Options{Buffer: 64})
	fast := open(t, srv)
	fast.next()
	fast.next()
	slow := open(t, srv)
	slow.next()
	slow.next()
	waitClients(t, h, 2)

	got := make(chan string, batch)
	go func() {
		for i := 0; i < total; i++ {
			f := fast.next()
			id, _ := f["id"].(string)
			got <- id
		}
	}()
	for i := 0; i < total; i += batch {
		start := time.Now()
		for j := i; j < i+batch; j++ {
			h.Publish(padded(fmt.Sprintf("m%d", j)))
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("publishing a batch took %v", d)
		}
		for j := i; j < i+batch; j++ {
			select {
			case id := <-got:
				if id != fmt.Sprintf("m%d", j) {
					t.Fatalf("fast client got %q, want m%d", id, j)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("fast client stalled at m%d", j)
			}
		}
	}
	waitClients(t, h, 1)
}

func TestDisconnectCleansUp(t *testing.T) {
	h, srv := newHub(t, Options{})
	s := open(t, srv)
	s.next()
	waitClients(t, h, 1)
	s.stop()
	waitClients(t, h, 0)
}

func waitClients(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.Clients() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("clients = %d, want %d", h.Clients(), n)
}

// padded is a 4 KiB message, so a client that stops reading fills the
// kernel's socket buffers quickly.
func padded(id string) Msg {
	return Msg{ID: id, Source: "s", JSON: []byte(fmt.Sprintf(`{"id":%q,"pad":%q}`, id, strings.Repeat("x", 4096)))}
}
