// Package viewers is the overlay bar's live viewer counts (multichat
// lib/viewers.js at 29a9d2c). Counts, not names.
//
// Where the numbers come from:
//
//	Twitch   the official Get Streams endpoint, with the sign-in the reply
//	         dock already has (any user token, no extra scope). Not signed
//	         in means "can't tell", never a made-up zero.
//	           https://dev.twitch.tv/docs/api/reference/#get-streams
//	YouTube  the watch page's own interface (InnerTube updated_metadata),
//	         the same free, unofficial path the chat reader uses. It is NOT
//	         the Data API: quota stays for replies only. If YouTube changes
//	         its internals, ParseYouTube is the place to look.
//
// A platform's state is one of:
//
//	none     no source of that platform in the panel (no chip)
//	live     count is the number watching
//	offline  the stream isn't live
//	unknown  can't tell right now; note says why
package viewers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

const (
	// Origin is YouTube.
	Origin = "https://www.youtube.com"
	ua     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	// StaleAfter is how many failed polls in a row drop a known number.
	StaleAfter = 3
	// DefaultClientVersion is the InnerTube client version when the reader
	// hasn't found one.
	DefaultClientVersion = "2.20260708.00.00"
)

// YouTube is one video's answer: live with a count, or not live.
type YouTube struct {
	Live  bool
	Count float64
}

// errShape is a value JavaScript would have thrown a TypeError on.
var errShape = errors.New("viewers: unexpected shape")

// textOf is t.simpleText, or its runs' text joined, or "".
func textOf(t any) (string, error) {
	if !jsjson.Truthy(t) {
		return "", nil
	}
	if st, _ := jsjson.Get(t, "simpleText"); jsjson.Truthy(st) {
		return jsjson.ToString(st), nil
	}
	runs, _ := jsjson.Get(t, "runs")
	if !jsjson.Truthy(runs) {
		return "", nil
	}
	list, ok := runs.([]any)
	if !ok {
		return "", errShape // (runs || []).map: not an array
	}
	var b strings.Builder
	for _, x := range list {
		if x == nil {
			return "", errShape // null.text
		}
		if v, ok := jsjson.Get(x, "text"); ok && v != nil {
			b.WriteString(jsjson.ToString(v)) // undefined and null join as ""
		}
	}
	return b.String(), nil
}

func isWord(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// hasWord is /\bword\b/i for an ASCII word, as JavaScript matches it
// without the u flag: ASCII case folding and ASCII word boundaries. A match
// can only be ASCII bytes, and a byte of a multi-byte character is never a
// word character, so scanning bytes is exact.
func hasWord(s, word string) bool {
	for at := 0; at+len(word) <= len(s); at++ {
		end := at + len(word)
		if asciiFold(s[at:end], word) && (at == 0 || !isWord(s[at-1])) && (end == len(s) || !isWord(s[end])) {
			return true
		}
	}
	return false
}

// startsWithWord is /^\s*word\b/i (JavaScript \s, ASCII folding).
func startsWithWord(s, word string) bool {
	s = strings.TrimLeftFunc(s, jsjson.IsSpace)
	if len(s) < len(word) || !asciiFold(s[:len(word)], word) {
		return false
	}
	return len(s) == len(word) || !isWord(s[len(word)])
}

func asciiFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// A broadcast that is scheduled but hasn't started answers like a live one,
// except the number is people with the page open ("1 waiting", and a date
// action "Scheduled for …"). Nobody is watching a stream that isn't running.
// The request pins hl=en, which is why English words can be matched; only a
// positive sign of waiting hides the count.
func notStarted(actions []any, renderer any) (bool, error) {
	vc, _ := jsjson.Get(renderer, "viewCount")
	text, err := textOf(vc)
	if err != nil {
		return false, err
	}
	if hasWord(text, "waiting") {
		return true, nil
	}
	for _, a := range actions {
		var dt any
		if jsjson.Truthy(a) {
			if u, _ := jsjson.Get(a, "updateDateTextAction"); jsjson.Truthy(u) {
				dt, _ = jsjson.Get(u, "dateText")
			}
		}
		t, err := textOf(dt)
		if err != nil {
			return false, err
		}
		if startsWithWord(t, "scheduled") {
			return true, nil
		}
	}
	return false, nil
}

// jsRound is Math.round.
func jsRound(x float64) float64 {
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return r
}

// ParseYouTube reads an updated_metadata answer: nil when it can't tell.
// The shape, as of 2026-11:
//
//	actions[].updateViewershipAction.viewCount.videoViewCountRenderer
//	  { isLive: true, originalViewCount: "4467", viewCount: { simpleText: "4,467 watching now" } }
//
// An error is where the source would have thrown.
func ParseYouTube(v any) (*YouTube, error) {
	acts, _ := jsjson.Get(v, "actions")
	if !jsjson.Truthy(v) || !jsjson.Truthy(acts) {
		return nil, nil
	}
	actions, ok := acts.([]any)
	if !ok {
		if _, isString := acts.(string); isString {
			return nil, nil // for-of over a string: characters have no actions
		}
		return nil, errShape
	}
	for _, a := range actions {
		if !jsjson.Truthy(a) {
			continue
		}
		u, _ := jsjson.Get(a, "updateViewershipAction")
		if !jsjson.Truthy(u) {
			continue
		}
		vc, _ := jsjson.Get(u, "viewCount")
		if !jsjson.Truthy(vc) {
			continue
		}
		r, _ := jsjson.Get(vc, "videoViewCountRenderer")
		if !jsjson.Truthy(r) {
			continue
		}
		live, _ := jsjson.Get(r, "isLive")
		if !jsjson.Truthy(live) {
			return &YouTube{}, nil
		}
		waiting, err := notStarted(actions, r)
		if err != nil {
			return nil, err
		}
		if waiting {
			return &YouTube{}, nil
		}
		o, has := jsjson.Get(r, "originalViewCount")
		n := jsjson.ToNumber(o, has)
		if math.IsNaN(n) || math.IsInf(n, 0) {
			view, _ := jsjson.Get(r, "viewCount")
			text, err := textOf(view)
			if err != nil {
				return nil, err
			}
			if st, _ := jsjson.Get(view, "simpleText"); jsjson.Truthy(st) {
				if _, isString := st.(string); !isString {
					return nil, errShape // a number has no .replace
				}
			}
			text = strings.Map(func(c rune) rune {
				if c == ',' || c == '.' || jsjson.IsSpace(c) {
					return -1
				}
				return c
			}, text)
			n = math.NaN()
			if i := strings.IndexFunc(text, func(c rune) bool { return '0' <= c && c <= '9' }); i >= 0 {
				j := i
				for j < len(text) && '0' <= text[j] && text[j] <= '9' {
					j++
				}
				n, _ = strconv.ParseFloat(text[i:j], 64) // too long is +Inf, as Number() gives
			}
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, nil
		}
		return &YouTube{Live: true, Count: math.Max(0, jsRound(n))}, nil
	}
	return nil, nil // no viewership block at all: can't tell
}

// FetchOptions is one video to ask about.
type FetchOptions struct {
	VideoID, APIKey, ClientVersion string
	Origin                         string // "" is YouTube
	Client                         *http.Client
	Timeout                        time.Duration // 0 is 10 s
}

// FetchYouTube asks the watch page's interface for one video's count:
// anonymous, no token, nothing official. A refusal is nil; a network error
// or an answer that isn't JSON is an error.
func FetchYouTube(ctx context.Context, o FetchOptions) (*YouTube, error) {
	origin := o.Origin
	if origin == "" {
		origin = Origin
	}
	cv := o.ClientVersion
	if cv == "" {
		cv = DefaultClientVersion
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	qs := "prettyPrint=false"
	if o.APIKey != "" {
		qs = "key=" + encodeURIComponent(o.APIKey) + "&" + qs
	}
	body := jsjson.NewObject().
		Set("context", jsjson.NewObject().Set("client", jsjson.NewObject().
			Set("clientName", "WEB").Set("clientVersion", cv).Set("hl", "en").Set("gl", "US"))).
		Set("videoId", o.VideoID)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/youtubei/v1/updated_metadata?"+qs, bytes.NewReader(jsjson.Stringify(body, "")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("user-agent", ua)
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("cookie", "SOCS=CAI")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", Origin)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, nil
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	v, err := jsjson.Parse(b)
	if err != nil {
		return nil, err
	}
	return ParseYouTube(v)
}

// encodeURIComponent, as JavaScript has it.
func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isWord(c) && c != '_' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Source is a panel source as the poller sees it.
type Source struct {
	Platform      string
	Channel       string // Twitch: the login its reader joined
	VideoID       string // YouTube: the broadcast its reader found
	APIKey        string
	ClientVersion string
}

// Twitch is Get Streams' answer: state "ok" with live counts by login, or
// "signed_out" / "error" with a note.
type Twitch struct {
	State string
	Live  map[string]float64
	Note  string
}

// Options wires the poller.
type Options struct {
	ListSources func() []Source
	Twitch      func(ctx context.Context, logins []string) (Twitch, error)
	YouTube     func(ctx context.Context, s Source) (*YouTube, error)
	OnChange    func(hub.ViewerCounts) // when the public picture changes
	Active      func() bool            // false skips a tick: nobody is looking
	Interval    time.Duration          // 0 is 30 s
}

// Counts polls both platforms.
type Counts struct {
	o Options

	mu      sync.Mutex
	counts  hub.ViewerCounts
	fails   [2]int // twitch, youtube
	running chan struct{}
	stop    chan struct{}
}

// New is a poller that hasn't started. Until its first poll every platform
// is "none".
func New(o Options) *Counts {
	if o.Interval == 0 {
		o.Interval = 30 * time.Second
	}
	if o.Active == nil {
		o.Active = func() bool { return true }
	}
	if o.OnChange == nil {
		o.OnChange = func(hub.ViewerCounts) {}
	}
	return &Counts{o: o, counts: hub.NoViewers}
}

// Get is the last picture.
func (c *Counts) Get() hub.ViewerCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts
}

func count(n float64) *float64 { return &n }

// unsure: can't tell this time. Keep a recent number for a few polls rather
// than flicker, then admit it.
func (c *Counts) unsure(p int, note string) hub.Count {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fails[p]++
	prev := c.counts.Twitch
	if p == 1 {
		prev = c.counts.YouTube
	}
	if prev.State == "live" && c.fails[p] < StaleAfter {
		return prev
	}
	return hub.Count{State: "unknown", Note: note}
}

func (c *Counts) setFails(p, n int) {
	c.mu.Lock()
	c.fails[p] = n
	c.mu.Unlock()
}

func (c *Counts) pollTwitch(ctx context.Context, list []Source) hub.Count {
	var logins []string
	seen := map[string]bool{}
	for _, s := range list {
		if s.Channel != "" && !seen[s.Channel] {
			seen[s.Channel] = true
			logins = append(logins, s.Channel)
		}
	}
	if len(logins) == 0 {
		return hub.Count{State: "unknown"}
	}
	r, err := safeTwitch(ctx, c.o.Twitch, logins)
	if err != nil {
		r = Twitch{State: "error"}
	}
	if r.State == "error" {
		note := r.Note
		if note == "" {
			note = "Couldn't reach Twitch."
		}
		return c.unsure(0, note)
	}
	if r.State != "ok" {
		c.setFails(0, 0)
		note := r.Note
		if note == "" {
			note = "Sign in to Twitch on /setup to show viewers."
		}
		return hub.Count{State: "unknown", Note: note}
	}
	c.setFails(0, 0)
	total, anyLive := 0.0, false
	for _, l := range logins {
		if n, ok := r.Live[l]; ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
			total += n
			anyLive = true
		}
	}
	if !anyLive {
		return hub.Count{State: "offline"}
	}
	return hub.Count{State: "live", Count: count(total)}
}

func (c *Counts) pollYouTube(ctx context.Context, list []Source) hub.Count {
	var watching []Source
	for _, s := range list {
		if s.VideoID != "" {
			watching = append(watching, s)
		}
	}
	if len(watching) == 0 { // the reader hasn't found a broadcast
		c.setFails(1, 0)
		return hub.Count{State: "offline"}
	}
	results := make([]*YouTube, len(watching))
	var wg sync.WaitGroup
	for i, s := range watching {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := safeYouTube(ctx, c.o.YouTube, s)
			if err == nil {
				results[i] = r
			}
		}()
	}
	wg.Wait()
	total, live, answered := 0.0, false, false
	for _, r := range results {
		if r != nil {
			answered = true
			if r.Live {
				live = true
				total += r.Count
			}
		}
	}
	if !answered {
		return c.unsure(1, "Couldn't read the count from YouTube.")
	}
	c.setFails(1, 0)
	if !live {
		return hub.Count{State: "offline"}
	}
	return hub.Count{State: "live", Count: count(total)}
}

func safeTwitch(ctx context.Context, f func(context.Context, []string) (Twitch, error), logins []string) (r Twitch, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("twitch viewers: %v", p)
		}
	}()
	return f(ctx, logins)
}

func safeYouTube(ctx context.Context, f func(context.Context, Source) (*YouTube, error), s Source) (r *YouTube, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("youtube viewers: %v", p)
		}
	}()
	return f(ctx, s)
}

func same(a, b hub.Count) bool {
	if a.State != b.State || a.Note != b.Note || (a.Count == nil) != (b.Count == nil) {
		return false
	}
	return a.Count == nil || *a.Count == *b.Count
}

// Refresh polls now, or joins the poll already running, and returns the
// picture it ends with.
func (c *Counts) Refresh(ctx context.Context) hub.ViewerCounts {
	c.mu.Lock()
	done := c.running
	if done == nil {
		done = make(chan struct{})
		c.running = done
		go c.poll(done)
	}
	c.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return c.Get()
}

func (c *Counts) poll(done chan struct{}) {
	defer func() {
		c.mu.Lock()
		c.running = nil
		c.mu.Unlock()
		close(done)
	}()
	ctx := context.Background()
	var tw, yt []Source
	for _, s := range c.o.ListSources() {
		switch s.Platform {
		case "twitch":
			tw = append(tw, s)
		case "youtube":
			yt = append(yt, s)
		}
	}
	var next hub.ViewerCounts
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if len(tw) == 0 {
			c.setFails(0, 0)
			next.Twitch = hub.Count{State: "none"}
			return
		}
		next.Twitch = c.pollTwitch(ctx, tw)
	}()
	go func() {
		defer wg.Done()
		if len(yt) == 0 {
			c.setFails(1, 0)
			next.YouTube = hub.Count{State: "none"}
			return
		}
		next.YouTube = c.pollYouTube(ctx, yt)
	}()
	wg.Wait()
	c.mu.Lock()
	changed := !same(next.Twitch, c.counts.Twitch) || !same(next.YouTube, c.counts.YouTube)
	if changed {
		c.counts = next
	}
	c.mu.Unlock()
	if changed {
		func() {
			defer func() { recover() }() // a listener that panics breaks nothing
			c.o.OnChange(next)
		}()
	}
}

// RefreshNow is for something that just changed what a poll would say (a
// sign-in, a sign-out). A poll already on the wire may have asked before the
// change, so wait it out and ask again.
func (c *Counts) RefreshNow(ctx context.Context) hub.ViewerCounts {
	c.mu.Lock()
	done := c.running
	c.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return c.Get()
		}
	}
	return c.Refresh(ctx)
}

// Start polls every Interval while Active says a page is connected.
func (c *Counts) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		return
	}
	stop := make(chan struct{})
	c.stop = stop
	go func() {
		t := time.NewTicker(c.o.Interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if c.o.Active() {
					go c.Refresh(context.Background())
				}
			}
		}
	}()
}

// Stop ends the timer.
func (c *Counts) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
}
