package twitch

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/sources"
)

// fakeTwitch hands the reader one end of a pipe per connection and the test
// the other.
type fakeTwitch struct {
	conns chan net.Conn
	fail  atomic.Int32 // dials to refuse first
}

func (f *fakeTwitch) dial(ctx context.Context) (io.ReadWriteCloser, error) {
	if f.fail.Load() > 0 {
		f.fail.Add(-1)
		return nil, errors.New("connection refused")
	}
	a, b := net.Pipe()
	select {
	case f.conns <- b:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return a, nil
}

type events struct {
	mu     sync.Mutex
	status []string
	msgs   []string
	sleeps []time.Duration
}

func (e *events) add(list *[]string, s string) { e.mu.Lock(); *list = append(*list, s); e.mu.Unlock() }
func (e *events) get(list *[]string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), *list...)
}

func newReader(t *testing.T, ft *fakeTwitch, ev *events) *Reader {
	r := &Reader{
		Channel: "examplechannel",
		Dial:    ft.dial,
		Rand:    func() float64 { return 0 },
		Sleep: func(ctx context.Context, d time.Duration) bool {
			ev.mu.Lock()
			ev.sleeps = append(ev.sleeps, d)
			ev.mu.Unlock()
			return ctx.Err() == nil
		},
		H: app.Handlers{
			Message:      func(f []app.Field) { ev.add(&ev.msgs, string(app.Encode(f))) },
			Status:       func(s, d string) { ev.add(&ev.status, s+":"+d) },
			Remove:       func(string) {},
			RemoveAuthor: func(string) {},
			Clear:        func() {},
		},
	}
	t.Cleanup(r.Stop)
	return r
}

func readLine(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return line
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLoginPingJoinMessage(t *testing.T) {
	ft := &fakeTwitch{conns: make(chan net.Conn, 4)}
	ev := &events{}
	r := newReader(t, ft, ev)
	r.Rand = func() float64 { return 0.5 }
	r.Start()
	srv := <-ft.conns
	br := bufio.NewReader(srv)
	if got := readLine(t, br) + readLine(t, br) + readLine(t, br); got != "CAP REQ :twitch.tv/tags twitch.tv/commands\r\nNICK justinfan50000\r\nJOIN #examplechannel\r\n" {
		t.Fatalf("login = %q", got)
	}
	io.WriteString(srv, "PING :tmi.twitch.tv\r\n")
	if got := readLine(t, br); got != "PONG :tmi.twitch.tv\r\n" {
		t.Errorf("pong = %q", got)
	}
	// two lines in one write, and a line split across writes
	io.WriteString(srv, ":x 366 justinfan50000 #examplechannel :End\r\n@id=a1;display-name=A :a!a@a PRIVMSG #examplechannel :hel")
	io.WriteString(srv, "lo\r\n")
	eventually(t, "a message", func() bool { return len(ev.get(&ev.msgs)) == 1 })
	if m := ev.get(&ev.msgs)[0]; !strings.Contains(m, `"id":"tw:a1"`) || !strings.Contains(m, `"v":"hello"`) {
		t.Errorf("message = %s", m)
	}
	if s := ev.get(&ev.status); strings.Join(s, "|") != "connecting:connecting to Twitch chat…|live:" {
		t.Errorf("status = %v", s)
	}
}

// RECONNECT and a dropped connection back off 1 s, 2 s, 4 s … up to 30 s
// (plus jitter, zero here); joining resets the count.
func TestBackoff(t *testing.T) {
	ft := &fakeTwitch{conns: make(chan net.Conn, 8)}
	ft.fail.Store(6)
	ev := &events{}
	r := newReader(t, ft, ev)
	r.Start()
	srv := <-ft.conns // after six refused dials
	want := []time.Duration{1, 2, 4, 8, 16, 30}
	got := ev.get2(t)
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Fatalf("sleeps = %v", got)
		}
	}
	br := bufio.NewReader(srv)
	readLine(t, br)
	readLine(t, br)
	readLine(t, br)
	io.WriteString(srv, ":x 366 n #examplechannel :End\r\n")
	eventually(t, "live", func() bool { s := ev.get(&ev.status); return s[len(s)-1] == "live:" })
	io.WriteString(srv, ":tmi.twitch.tv RECONNECT\r\n")
	<-ft.conns
	sl := ev.get2(t)
	if last := sl[len(sl)-1]; last != time.Second {
		t.Errorf("after joining, backoff restarts at 1 s: %v", last)
	}
	if s := ev.get(&ev.status); !strings.Contains(strings.Join(s, "|"), "connecting:reconnecting…") {
		t.Errorf("status = %v", s)
	}
}

func (e *events) get2(t *testing.T) []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.sleeps...)
}

func TestKeepalive(t *testing.T) {
	ft := &fakeTwitch{conns: make(chan net.Conn, 4)}
	ev := &events{}
	r := newReader(t, ft, ev)
	r.Keepalive = 30 * time.Millisecond
	r.Start()
	srv := <-ft.conns
	br := bufio.NewReader(srv)
	readLine(t, br)
	readLine(t, br)
	readLine(t, br)
	if got := readLine(t, br); got != "PING :keepalive\r\n" {
		t.Errorf("keepalive = %q", got)
	}
	// nothing heard for longer than Idle: the connection is dropped
	var mu sync.Mutex
	now := time.Now()
	r.mu.Lock()
	r.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r.Idle = time.Minute
	r.mu.Unlock()
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	select {
	case <-ft.conns:
	case <-time.After(3 * time.Second):
		t.Fatal("an idle connection wasn't replaced")
	}
}

func TestEmotesLoadOnceOnRoomState(t *testing.T) {
	ft := &fakeTwitch{conns: make(chan net.Conn, 4)}
	ev := &events{}
	r := newReader(t, ft, ev)
	var loads atomic.Int32
	r.Emotes = func(ctx context.Context, room string) map[string]string {
		loads.Add(1)
		if room != "12345" {
			t.Errorf("room = %q", room)
		}
		return map[string]string{"catJAM": "https://cdn.7tv.app/emote/x/2x.webp"}
	}
	r.Start()
	srv := <-ft.conns
	go io.Copy(io.Discard, srv)
	io.WriteString(srv, "@room-id=12345 :tmi.twitch.tv ROOMSTATE #examplechannel\r\n@room-id=12345 :tmi.twitch.tv ROOMSTATE #examplechannel\r\n")
	eventually(t, "emotes", func() bool { return r.extMap() != nil })
	io.WriteString(srv, "@id=e1 :a!a@a PRIVMSG #examplechannel :catJAM\r\n")
	eventually(t, "a message", func() bool { return len(ev.get(&ev.msgs)) == 1 })
	if m := ev.get(&ev.msgs)[0]; !strings.Contains(m, `"t":"emote"`) {
		t.Errorf("message = %s", m)
	}
	if loads.Load() != 1 {
		t.Errorf("loaded %d times", loads.Load())
	}
}

func TestStop(t *testing.T) {
	ft := &fakeTwitch{conns: make(chan net.Conn, 4)}
	ev := &events{}
	r := newReader(t, ft, ev)
	r.Start()
	<-ft.conns
	r.Stop()
	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("reader didn't stop")
	}
	n := len(ev.get(&ev.status))
	time.Sleep(50 * time.Millisecond)
	if len(ev.get(&ev.status)) != n {
		t.Error("status after stop")
	}
}

func TestFactory(t *testing.T) {
	f := Factory(nil, "")
	if _, _, err := f(sources.Entry{URL: "https://www.twitch.tv/"}, app.Handlers{}); !errors.Is(err, ErrNoChannel) {
		t.Errorf("no channel: %v", err)
	}
	ad, ch, err := f(sources.Entry{URL: "https://www.twitch.tv/ExampleChannel"}, app.Handlers{})
	if err != nil || ch != "examplechannel" || ad.(*Reader).Channel != "examplechannel" {
		t.Errorf("factory: %v %q", err, ch)
	}
}
