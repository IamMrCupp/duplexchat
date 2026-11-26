package twitch

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/sources"
)

// Where Twitch chat is.
const (
	IRCHost = "irc.chat.twitch.tv"
	IRCPort = 6697
)

// ErrNoChannel is a source whose link doesn't name a channel.
var ErrNoChannel = errors.New("couldn't get a channel name from that link")

// Reader is one channel's chat, read anonymously.
type Reader struct {
	Channel string // lowercase login
	H       app.Handlers
	Addr    string // host:port; "" → irc.chat.twitch.tv:6697

	// Seams; zero values mean the real thing.
	Dial      func(ctx context.Context) (io.ReadWriteCloser, error)
	Emotes    func(ctx context.Context, roomID string) map[string]string // third-party emotes; nil → none
	Sleep     func(ctx context.Context, d time.Duration) bool            // backoff wait; false when stopped
	Now       func() time.Time
	Rand      func() float64
	Keepalive time.Duration // 240 s
	Idle      time.Duration // 300 s

	mu        sync.Mutex
	conn      io.ReadWriteCloser
	ext       map[string]string
	extLoaded bool
	attempts  int
	lastData  time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
}

// Factory builds Twitch readers for the app. emotes may be nil; addr ""
// means Twitch itself.
func Factory(emotes func(ctx context.Context, roomID string) map[string]string, addr string) app.Factory {
	return func(e sources.Entry, h app.Handlers) (app.Adapter, string, error) {
		ch := ChannelFromURL(e.URL)
		if ch == "" {
			return nil, "", ErrNoChannel
		}
		return &Reader{Channel: ch, H: h, Emotes: emotes, Addr: addr}, ch, nil
	}
}

func (r *Reader) defaults() {
	if r.Addr == "" {
		r.Addr = net.JoinHostPort(IRCHost, strconv.Itoa(IRCPort))
	}
	if r.Dial == nil {
		addr := r.Addr
		host, _, _ := net.SplitHostPort(addr)
		r.Dial = func(ctx context.Context) (io.ReadWriteCloser, error) {
			d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 20 * time.Second}, Config: &tls.Config{ServerName: host}}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	if r.Sleep == nil {
		r.Sleep = func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return true
			case <-ctx.Done():
				return false
			}
		}
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Rand == nil {
		r.Rand = rand.Float64
	}
	if r.Keepalive == 0 {
		r.Keepalive = 240 * time.Second
	}
	if r.Idle == 0 {
		r.Idle = 300 * time.Second
	}
}

// Start connects and keeps reconnecting until Stop.
func (r *Reader) Start() {
	r.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.ctx, r.cancel, r.done = ctx, cancel, make(chan struct{})
	r.mu.Unlock()
	go r.run(ctx)
}

// Stop disconnects for good.
func (r *Reader) Stop() {
	r.mu.Lock()
	cancel, conn := r.cancel, r.conn
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close()
	}
}

// Done is closed once the reader has stopped (for tests).
func (r *Reader) Done() <-chan struct{} { return r.done }

func (r *Reader) run(ctx context.Context) {
	defer close(r.done)
	defer func() {
		if v := recover(); v != nil {
			r.H.Status("error", fmt.Sprint("Twitch reader crashed: ", v))
		}
	}()
	for ctx.Err() == nil {
		r.H.Status("connecting", "connecting to Twitch chat…")
		if conn, err := r.Dial(ctx); err == nil {
			r.session(ctx, conn)
		}
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		delay := time.Duration(math.Min(30000, 1000*math.Pow(2, math.Min(float64(r.attempts), 5)))+r.Rand()*500) * time.Millisecond
		r.attempts++
		r.mu.Unlock()
		r.H.Status("connecting", "reconnecting…")
		if !r.Sleep(ctx, delay) {
			return
		}
	}
}

// session runs one connection until it ends.
func (r *Reader) session(ctx context.Context, conn io.ReadWriteCloser) {
	r.mu.Lock()
	r.conn, r.lastData = conn, r.Now()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.conn == conn {
			r.conn = nil
		}
		r.mu.Unlock()
		conn.Close()
	}()
	if ctx.Err() != nil {
		return
	}
	nick := "justinfan" + strconv.Itoa(int(math.Floor(10000+r.Rand()*80000)))
	r.write(fmt.Sprintf("CAP REQ :twitch.tv/tags twitch.tv/commands\r\nNICK %s\r\nJOIN #%s\r\n", nick, r.Channel))

	stopKeepalive := make(chan struct{})
	defer close(stopKeepalive)
	go r.keepalive(conn, stopKeepalive)

	br := bufio.NewReaderSize(conn, 64<<10)
	var buf strings.Builder
	for {
		chunk, err := br.ReadString('\n')
		if chunk != "" {
			r.mu.Lock()
			r.lastData = r.Now()
			r.mu.Unlock()
			buf.WriteString(chunk)
			// lines end in \r\n; a bare \n stays part of the line, as in multichat
			s := buf.String()
			for {
				i := strings.Index(s, "\r\n")
				if i == -1 {
					break
				}
				if line := s[:i]; line != "" {
					r.safeHandle(line)
				}
				s = s[i+2:]
			}
			buf.Reset()
			buf.WriteString(s)
		}
		if err != nil {
			return
		}
	}
}

func (r *Reader) keepalive(conn io.ReadWriteCloser, stop <-chan struct{}) {
	t := time.NewTicker(r.Keepalive)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.mu.Lock()
			idle := r.Now().Sub(r.lastData)
			r.mu.Unlock()
			if idle > r.Idle {
				conn.Close()
				return
			}
			r.write("PING :keepalive\r\n")
		}
	}
}

func (r *Reader) write(s string) {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()
	if conn != nil {
		conn.Write([]byte(s))
	}
}

func (r *Reader) destroy() {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// safeHandle keeps the stream alive on a bad line.
func (r *Reader) safeHandle(line string) {
	defer func() { recover() }()
	r.handle(line)
}

var loginRE = regexp.MustCompile(`(?i)login`)

// handle is multichat's _handle: one line, its effect.
func (r *Reader) handle(line string) {
	m := parseLine(line)
	switch m.Cmd {
	case "PING":
		t := "tmi.twitch.tv"
		if m.Trailing != nil && *m.Trailing != "" {
			t = *m.Trailing
		}
		r.write("PONG :" + t + "\r\n")
	case "366": // end of NAMES: joined
		r.mu.Lock()
		r.attempts = 0
		r.mu.Unlock()
		r.H.Status("live", "")
	case "ROOMSTATE":
		r.loadEmotes(m.Tags["room-id"])
	case "PRIVMSG":
		r.privmsg(m)
	case "USERNOTICE":
		r.usernotice(m)
	case "CLEARMSG":
		if id := m.Tags["target-msg-id"]; id != "" {
			r.H.Remove("tw:" + id)
		}
	case "CLEARCHAT":
		if m.Trailing != nil && *m.Trailing != "" {
			r.H.RemoveAuthor(strings.ToLower(*m.Trailing))
		} else {
			r.H.Clear()
		}
	case "RECONNECT":
		r.destroy()
	case "NOTICE":
		if m.Trailing != nil && loginRE.MatchString(*m.Trailing) {
			r.H.Status("error", *m.Trailing)
		}
	}
}

func (r *Reader) loadEmotes(roomID string) {
	r.mu.Lock()
	if r.extLoaded || roomID == "" || r.Emotes == nil {
		r.mu.Unlock()
		return
	}
	r.extLoaded = true
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Unlock()
	go func() {
		m := r.Emotes(ctx, roomID)
		if ctx.Err() != nil {
			return // stopped meanwhile
		}
		r.mu.Lock()
		r.ext = m
		r.mu.Unlock()
	}()
}

func (r *Reader) extMap() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ext
}

// msgID prefers source-id, so a Shared Chat message mirrored into two
// joined channels collapses into one.
func (r *Reader) msgID(m Line, suffix string) string {
	id := m.Tags["source-id"]
	if id == "" {
		id = m.Tags["id"]
	}
	if id == "" {
		id = strconv.FormatInt(rand.Int64N(1<<52), 36)
	}
	return "tw:" + id + suffix
}

func (r *Reader) author(m Line) app.Author {
	login := m.Tags["login"]
	if login == "" && m.Prefix != nil && strings.Contains(*m.Prefix, "!") {
		login = strings.SplitN(*m.Prefix, "!", 2)[0]
	}
	name := m.Tags["display-name"]
	if name == "" {
		name = login
	}
	var color *string
	if c := m.Tags["color"]; c != "" {
		color = &c
	}
	return app.Author{Name: name, Color: color, Roles: rolesFromBadges(m.Tags["badges"]), Key: &login}
}

func (r *Reader) privmsg(m Line) {
	text := ""
	if m.Trailing != nil {
		text = *m.Trailing
	}
	action := false
	if strings.HasPrefix(text, "\x01ACTION ") && strings.HasSuffix(text, "\x01") && len(text) >= 9 {
		action = true
		text = text[8 : len(text)-1]
	}
	r.H.Message([]app.Field{
		{Key: "id", Value: r.msgID(m, "")},
		{Key: "kind", Value: "chat"},
		{Key: "author", Value: r.author(m)},
		{Key: "parts", Value: buildParts(text, m.Tags, r.extMap())},
		{Key: "action", Value: action},
		{Key: "firstMsg", Value: m.Tags["first-msg"] == "1"},
	})
}

func (r *Reader) usernotice(m Line) {
	msgID := m.Tags["msg-id"]
	if msgID == "announcement" {
		if m.Trailing != nil && *m.Trailing != "" {
			r.privmsg(m)
		}
		return
	}
	if !usernoticeSystem[msgID] {
		return
	}
	if sys := m.Tags["system-msg"]; sys != "" {
		r.H.Message([]app.Field{
			{Key: "id", Value: r.msgID(m, "")},
			{Key: "kind", Value: "system"},
			{Key: "author", Value: r.author(m)},
			{Key: "parts", Value: []app.Part{}},
			{Key: "system", Value: sys},
		})
	}
	if m.Trailing != nil && *m.Trailing != "" {
		r.H.Message([]app.Field{
			{Key: "id", Value: r.msgID(m, ":msg")},
			{Key: "kind", Value: "chat"},
			{Key: "author", Value: r.author(m)},
			{Key: "parts", Value: buildParts(*m.Trailing, m.Tags, r.extMap())},
		})
	}
}
