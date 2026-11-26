package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/config"
	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/sources"
)

type fakeAdapter struct {
	mu            sync.Mutex
	starts, stops int
	h             Handlers
}

func (f *fakeAdapter) Start() { f.mu.Lock(); f.starts++; f.mu.Unlock() }
func (f *fakeAdapter) Stop()  { f.mu.Lock(); f.stops++; f.mu.Unlock() }

func newApp(t *testing.T, readers map[string]Factory) *App {
	t.Helper()
	h := hub.New(hub.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
	n := 0
	path := filepath.Join(t.TempDir(), "config.json")
	store := sources.NewStore(config.Load(path), path, sources.Options{NewID: func() string { n++; return fmt.Sprintf("%08x", n) }})
	return &App{Store: store, Hub: h, Readers: readers, Now: func() time.Time { return time.UnixMilli(1700000000000) }, Random: func() string { return "r" }}
}

func histJSON(a *App) []map[string]any {
	var out []map[string]any
	for _, m := range a.Hub.History() {
		var v map[string]any
		json.Unmarshal(m.JSON, &v)
		out = append(out, v)
	}
	return out
}

// multichat server.test.js "the test button sends English samples that say
// they are a test": order, platforms, kinds, no Cyrillic, each says "test".
func TestTestMessages(t *testing.T) {
	a := newApp(t, nil)
	a.Test()
	var got []string
	cyr := regexp.MustCompile(`\p{Cyrillic}`)
	test := regexp.MustCompile(`(?i)\btest\b`)
	for _, m := range histJSON(a) {
		got = append(got, fmt.Sprintf("%s/%s", m["platform"], m["kind"]))
		text, _ := m["system"].(string)
		if text == "" {
			for _, p := range m["parts"].([]any) {
				if v, ok := p.(map[string]any)["v"].(string); ok {
					text += v
				}
			}
		}
		if cyr.MatchString(text) || !test.MatchString(text) {
			t.Errorf("%v: text %q", m["id"], text)
		}
		if m["source"] != "test" || m["channel"] != "test" || m["ts"] != float64(1700000000000) {
			t.Errorf("%v: source %v channel %v ts %v", m["id"], m["source"], m["channel"], m["ts"])
		}
	}
	if strings.Join(got, ",") != "twitch/chat,youtube/chat,twitch/chat,youtube/super,twitch/system" {
		t.Errorf("order = %v", got)
	}
	// exact bytes of the Super Chat and the system line, key order included
	h := a.Hub.History()
	if s := string(h[3].JSON); s != `{"type":"msg","source":"test","ts":1700000000000,"id":"test:4:r","platform":"youtube","channel":"test","kind":"super","author":{"name":"Example Supporter","color":null,"roles":[],"key":"UCrich"},"amount":"$5.00","colors":{"header":"#ffb300","body":"#ffca28"},"parts":[{"t":"text","v":"Test Super Chat — no money moved"}]}` {
		t.Errorf("super = %s", s)
	}
	if s := string(h[4].JSON); s != `{"type":"msg","source":"test","ts":1700000000000,"id":"test:5:r","platform":"twitch","channel":"test","kind":"system","author":{"name":"ExampleSub","roles":[],"key":"examplesub"},"parts":[],"system":"Test: ExampleSub subscribed with Prime. 3 months in a row."}` {
		t.Errorf("system = %s", s)
	}
	if h[0].AuthorKey == nil || *h[0].AuthorKey != "exampleviewer" || h[4].AuthorKey == nil || *h[4].AuthorKey != "examplesub" {
		t.Error("author keys not taken for history edits")
	}
	a.Test()
	if id := a.Hub.History()[5].ID; id != "test:6:r" {
		t.Errorf("IDs keep counting: %s", id)
	}
}

func TestSourceLifecycle(t *testing.T) {
	var ad *fakeAdapter
	a := newApp(t, map[string]Factory{"twitch": func(e sources.Entry, h Handlers) (Adapter, string, error) {
		ad = &fakeAdapter{h: h}
		return ad, "examplechannel", nil
	}})
	e, err := a.AddSource("https://www.twitch.tv/examplechannel")
	if err != nil {
		t.Fatal(err)
	}
	if ad == nil || ad.starts != 1 {
		t.Fatal("reader not started")
	}
	if st := a.Store.Status()[0]; st.Channel != "examplechannel" || st.State != "connecting" {
		t.Errorf("status %+v", st)
	}
	ad.h.Status("live", "")
	ad.h.Message([]Field{{"id", "tw:1"}, {"kind", "chat"}, {"author", Author{Name: "A", Roles: []string{}, Key: str("alice")}}, {"parts", []Part{}}})
	ad.h.Message([]Field{{"id", "tw:2"}, {"kind", "chat"}, {"author", Author{Name: "B", Roles: []string{}, Key: str("bob")}}, {"parts", []Part{}}, {"source", "spoofed"}})
	if st := a.Store.Status()[0]; st.State != "live" {
		t.Errorf("state %q", st.State)
	}
	h := a.Hub.History()
	if len(h) != 2 || string(h[0].JSON) != `{"type":"msg","id":"tw:1","kind":"chat","author":{"name":"A","color":null,"roles":[],"key":"alice"},"parts":[],"source":"`+e.ID+`","platform":"twitch","channel":"examplechannel","ts":1700000000000}` {
		t.Fatalf("history %s", h[0].JSON)
	}
	if h[1].Source != e.ID {
		t.Errorf("a reader's own source field must not win: %q", h[1].Source)
	}
	ad.h.RemoveAuthor("alice")
	if len(a.Hub.History()) != 1 {
		t.Error("RemoveAuthor didn't edit history")
	}
	old := ad
	if !a.ReconnectSource(e.ID) || old.stops != 1 || ad == old || ad.starts != 1 {
		t.Error("reconnect didn't restart the reader")
	}
	if !a.RemoveSource(e.ID) || ad.stops != 1 {
		t.Error("remove didn't stop the reader")
	}
	if len(a.Hub.History()) != 0 || len(a.Store.Status()) != 0 {
		t.Error("remove left messages or the source behind")
	}
	if a.RemoveSource(e.ID) || a.ReconnectSource(e.ID) {
		t.Error("unknown IDs should report false")
	}
	ad.h.Status("live", "") // a late event from a stopped reader is ignored
	if len(a.Store.Status()) != 0 {
		t.Error("late status resurrected the source")
	}
}

func TestFactoryErrorBecomesStatus(t *testing.T) {
	a := newApp(t, map[string]Factory{"twitch": func(sources.Entry, Handlers) (Adapter, string, error) {
		return nil, "", errors.New("couldn't get a channel name from that link")
	}})
	a.AddSource("https://www.twitch.tv/")
	st := a.Store.Status()[0]
	if st.State != "error" || st.Detail != "couldn't get a channel name from that link" {
		t.Errorf("status %+v", st)
	}
}

func TestNoReaderStaysConnecting(t *testing.T) {
	a := newApp(t, nil)
	a.AddSource("https://www.youtube.com/@x")
	if st := a.Store.Status()[0]; st.State != "connecting" || st.Channel != "youtube" {
		t.Errorf("status %+v", st)
	}
}
