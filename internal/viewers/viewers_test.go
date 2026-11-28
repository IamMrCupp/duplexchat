package viewers

// multichat test/viewers.test.js at 29a9d2c, case for case: the YouTube
// parser against the shape captured from the real interface, the fetch
// against a fake YouTube, and the poller's states.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

type m = map[string]any

// js turns a Go literal into what jsjson.Parse gives for its JSON.
func js(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsjson.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func grouped(n int) string { // toLocaleString('en-US')
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// As returned by youtubei/v1/updated_metadata for a live video (2026-11), trimmed.
func liveMeta(n int, text string) m {
	r := m{
		"isLive":                  true,
		"extraShortViewCount":     m{"simpleText": "4.4K"},
		"unlabeledViewCountValue": m{"simpleText": grouped(n)},
	}
	if text == "" {
		r["viewCount"] = m{"simpleText": grouped(n) + " watching now"}
		r["originalViewCount"] = strconv.Itoa(n)
	} else {
		r["viewCount"] = m{"simpleText": text}
	}
	return m{
		"responseContext": m{},
		"continuation":    m{"timedContinuationData": m{"timeoutMs": 5000, "continuation": "FAKE_CONTINUATION"}},
		"actions": []any{
			m{"clickTrackingParams": "x", "updateViewershipAction": m{"viewCount": m{"videoViewCountRenderer": r}}},
			m{"clickTrackingParams": "x", "updateDateTextAction": m{}},
			m{"clickTrackingParams": "x", "updateTitleAction": m{}},
		},
	}
}

func parse(t *testing.T, v any) *YouTube {
	t.Helper()
	r, err := ParseYouTube(js(t, v))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var notLive = &YouTube{}

func live(n float64) *YouTube { return &YouTube{Live: true, Count: n} }

func TestParseLiveCountNotLiveAndShapesItCantRead(t *testing.T) {
	cases := []struct {
		in   any
		want *YouTube
	}{
		{liveMeta(4467, ""), live(4467)},
		{liveMeta(0, ""), live(0)},
		// no originalViewCount: fall back to the text, in whatever grouping the locale used
		{liveMeta(0, "4,467 watching now"), live(4467)},
		{liveMeta(0, "4.467 Zuschauer"), live(4467)},
		{liveMeta(0, "watching now"), nil},
		// a finished stream or an ordinary video: a view count, isLive absent
		{m{"actions": []any{m{"updateViewershipAction": m{"viewCount": m{"videoViewCountRenderer": m{"viewCount": m{"simpleText": "651,847,120 views"}, "originalViewCount": "651847120"}}}}}}, notLive},
	}
	for i, c := range cases {
		if got := parse(t, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: %+v, want %+v", i, got, c.want)
		}
	}
	for _, junk := range []any{nil, m{}, m{"actions": []any{}}, m{"actions": []any{m{"updateTitleAction": m{}}}}, m{"actions": []any{nil, 5}}} {
		if got := parse(t, junk); got != nil {
			t.Errorf("%v: %+v", junk, got)
		}
	}
}

type call struct {
	query  map[string]string
	body   any
	header http.Header
}

func fakeYouTube(t *testing.T, answer func() (int, any)) (*httptest.Server, func() []call) {
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/youtubei/v1/updated_metadata" {
			w.WriteHeader(404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body, _ := jsjson.Parse(b)
		q := map[string]string{}
		for k, v := range r.URL.Query() {
			q[k] = v[0]
		}
		mu.Lock()
		calls = append(calls, call{q, body, r.Header.Clone()})
		mu.Unlock()
		status, v := answer()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []call { mu.Lock(); defer mu.Unlock(); return append([]call(nil), calls...) }
}

func TestFetchAsksTheWatchPagesInterfaceForOneVideoAndNothingOfficial(t *testing.T) {
	status := 200
	srv, calls := fakeYouTube(t, func() (int, any) { return status, liveMeta(152, "") })
	r, err := FetchYouTube(context.Background(), FetchOptions{VideoID: "FAKEvideo01", APIKey: "FAKE_INNERTUBE_KEY", ClientVersion: "2.20261119.07.00", Origin: srv.URL})
	if err != nil || !reflect.DeepEqual(r, live(152)) {
		t.Fatalf("%+v %v", r, err)
	}
	c := calls()
	if len(c) != 1 {
		t.Fatalf("%d calls", len(c))
	}
	if !reflect.DeepEqual(c[0].query, map[string]string{"key": "FAKE_INNERTUBE_KEY", "prettyPrint": "false"}) {
		t.Errorf("query %v", c[0].query)
	}
	if v, _ := jsjson.Get(c[0].body, "videoId"); v != "FAKEvideo01" {
		t.Errorf("videoId %v", v)
	}
	ctx, _ := jsjson.Get(c[0].body, "context")
	client, _ := jsjson.Get(ctx, "client")
	if v, _ := jsjson.Get(client, "clientVersion"); v != "2.20261119.07.00" {
		t.Errorf("clientVersion %v", v)
	}
	if _, ok := c[0].header["Authorization"]; ok { // anonymous: no token goes near it
		t.Error("authorization header sent")
	}
	status = 429
	if r, err := FetchYouTube(context.Background(), FetchOptions{VideoID: "FAKEvideo01", Origin: srv.URL}); r != nil || err != nil {
		t.Fatalf("429: %+v %v", r, err)
	}
}

// The exact request: the body's bytes, every header the source sets, and no
// key when the reader has none.
func TestFetchRequestIsExactly(t *testing.T) {
	var raw []byte
	var h http.Header
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		h, q = r.Header.Clone(), r.URL.RawQuery
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	if r, err := FetchYouTube(context.Background(), FetchOptions{VideoID: "FAKEvideo01", Origin: srv.URL}); r != nil || err != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if string(raw) != `{"context":{"client":{"clientName":"WEB","clientVersion":"2.20260708.00.00","hl":"en","gl":"US"}},"videoId":"FAKEvideo01"}` {
		t.Errorf("body %s", raw)
	}
	if q != "prettyPrint=false" {
		t.Errorf("query %s", q)
	}
	for k, v := range map[string]string{
		"User-Agent": ua, "Accept-Language": "en-US,en;q=0.9", "Cookie": "SOCS=CAI",
		"Content-Type": "application/json", "Origin": "https://www.youtube.com",
	} {
		if h.Get(k) != v {
			t.Errorf("%s: %q", k, h.Get(k))
		}
	}
	if FetchOptions := (FetchOptions{APIKey: "a b&c/é"}); encodeURIComponent(FetchOptions.APIKey) != "a%20b%26c%2F%C3%A9" {
		t.Error(encodeURIComponent(FetchOptions.APIKey))
	}
}

type world struct {
	mu      sync.Mutex
	sources []Source
	changes []hub.ViewerCounts
	twCalls [][]string
	ytCalls []string
	vc      *Counts
}

func newWorld(twitch func([]string) (Twitch, error), youtube func(Source) (*YouTube, error)) *world {
	w := &world{}
	w.vc = New(Options{
		ListSources: func() []Source { w.mu.Lock(); defer w.mu.Unlock(); return append([]Source(nil), w.sources...) },
		Twitch: func(_ context.Context, logins []string) (Twitch, error) {
			w.mu.Lock()
			w.twCalls = append(w.twCalls, logins)
			w.mu.Unlock()
			if twitch == nil {
				return Twitch{State: "ok", Live: map[string]float64{}}, nil
			}
			return twitch(logins)
		},
		YouTube: func(_ context.Context, s Source) (*YouTube, error) {
			w.mu.Lock()
			w.ytCalls = append(w.ytCalls, s.VideoID)
			w.mu.Unlock()
			if youtube == nil {
				return live(1), nil
			}
			return youtube(s)
		},
		OnChange: func(c hub.ViewerCounts) { w.mu.Lock(); w.changes = append(w.changes, c); w.mu.Unlock() },
	})
	return w
}

func (w *world) add(s ...Source) { w.mu.Lock(); w.sources = append(w.sources, s...); w.mu.Unlock() }

func tw(channel string) Source { return Source{Platform: "twitch", Channel: channel} }
func yt(videoID string) Source { return Source{Platform: "youtube", VideoID: videoID} }

var none = hub.Count{State: "none"}

func liveCount(n float64) hub.Count { return hub.Count{State: "live", Count: &n} }

var bg = context.Background()

func TestPollerNoSourcesNoChipsNoRequests(t *testing.T) {
	w := newWorld(nil, nil)
	if got := w.vc.Refresh(bg); !reflect.DeepEqual(got, hub.ViewerCounts{Twitch: none, YouTube: none}) {
		t.Fatalf("%+v", got)
	}
	if len(w.twCalls)+len(w.ytCalls)+len(w.changes) != 0 {
		t.Fatalf("%v %v %v", w.twCalls, w.ytCalls, w.changes)
	}
}

func TestPollerLiveCountsAndAChangeIsAnnouncedOnce(t *testing.T) {
	var n atomic.Int64
	n.Store(42)
	w := newWorld(
		func([]string) (Twitch, error) {
			return Twitch{State: "ok", Live: map[string]float64{"examplechannel": float64(n.Load())}}, nil
		},
		func(Source) (*YouTube, error) { return live(17), nil })
	w.add(tw("examplechannel"), yt("FAKEvideo01"))
	if got := w.vc.Refresh(bg); !reflect.DeepEqual(got, hub.ViewerCounts{Twitch: liveCount(42), YouTube: liveCount(17)}) {
		t.Fatalf("%+v", got)
	}
	w.vc.Refresh(bg)
	if len(w.changes) != 1 { // same numbers: nothing broadcast
		t.Fatalf("%d changes", len(w.changes))
	}
	n.Store(43)
	w.vc.Refresh(bg)
	if len(w.changes) != 2 || *w.changes[1].Twitch.Count != 43 {
		t.Fatalf("%+v", w.changes)
	}
	if !reflect.DeepEqual(w.twCalls[0], []string{"examplechannel"}) || !reflect.DeepEqual(w.vc.Get(), w.changes[1]) {
		t.Fatalf("%v %+v", w.twCalls[0], w.vc.Get())
	}
}

func TestPollerOfflineIsOfflineNotZero(t *testing.T) {
	w := newWorld(
		func([]string) (Twitch, error) { return Twitch{State: "ok", Live: map[string]float64{}}, nil },
		func(Source) (*YouTube, error) { return notLive, nil })
	w.add(tw("examplechannel"), yt("FAKEvideo01"))
	off := hub.Count{State: "offline"}
	if got := w.vc.Refresh(bg); !reflect.DeepEqual(got, hub.ViewerCounts{Twitch: off, YouTube: off}) {
		t.Fatalf("%+v", got)
	}
	// a YouTube source whose reader hasn't found a broadcast: offline, and nothing is asked
	w2 := newWorld(nil, nil)
	w2.add(yt(""))
	if got := w2.vc.Refresh(bg).YouTube.State; got != "offline" || len(w2.ytCalls) != 0 {
		t.Fatalf("%s %v", got, w2.ytCalls)
	}
}

func TestPollerTwitchNotSignedInSaysSoInsteadOfGuessing(t *testing.T) {
	w := newWorld(func([]string) (Twitch, error) {
		return Twitch{State: "signed_out", Note: "Sign in to Twitch on /setup to show viewers."}, nil
	}, nil)
	w.add(tw("examplechannel"))
	if got := w.vc.Refresh(bg).Twitch; !reflect.DeepEqual(got, hub.Count{State: "unknown", Note: "Sign in to Twitch on /setup to show viewers."}) {
		t.Fatalf("%+v", got)
	}
}

func TestPollerAFailedPollKeepsTheLastNumberBrieflyThenAdmitsItDoesntKnow(t *testing.T) {
	var ok atomic.Bool
	ok.Store(true)
	w := newWorld(
		func([]string) (Twitch, error) {
			if ok.Load() {
				return Twitch{State: "ok", Live: map[string]float64{"examplechannel": 42}}, nil
			}
			return Twitch{State: "error", Note: "Couldn't reach Twitch."}, nil
		},
		func(Source) (*YouTube, error) {
			if !ok.Load() {
				return nil, errors.New("network")
			}
			return live(17), nil
		})
	w.add(tw("examplechannel"), yt("FAKEvideo01"))
	w.vc.Refresh(bg)
	ok.Store(false)
	for i := 0; i < 2; i++ {
		c := w.vc.Refresh(bg)
		if c.Twitch.Count == nil || *c.Twitch.Count != 42 || c.YouTube.Count == nil || *c.YouTube.Count != 17 {
			t.Fatalf("poll %d: %+v", i, c)
		}
	}
	gone := w.vc.Refresh(bg)
	if !reflect.DeepEqual(gone.Twitch, hub.Count{State: "unknown", Note: "Couldn't reach Twitch."}) ||
		!reflect.DeepEqual(gone.YouTube, hub.Count{State: "unknown", Note: "Couldn't read the count from YouTube."}) {
		t.Fatalf("%+v", gone)
	}
	ok.Store(true)
	if c := w.vc.Refresh(bg).Twitch.Count; c == nil || *c != 42 {
		t.Fatal(c)
	}
}

func TestPollerSeveralSourcesOfOnePlatformAddUpDuplicatesAreAskedOnce(t *testing.T) {
	w := newWorld(
		func([]string) (Twitch, error) {
			return Twitch{State: "ok", Live: map[string]float64{"examplechannel": 40, "second": 2}}, nil
		},
		func(s Source) (*YouTube, error) {
			if s.VideoID == "FAKEvideo01" {
				return live(10), nil
			}
			return notLive, nil
		})
	w.add(tw("examplechannel"), tw("second"), tw("examplechannel"), tw("third"), yt("FAKEvideo01"), yt("FAKEvideo02"))
	c := w.vc.Refresh(bg)
	if *c.Twitch.Count != 42 || *c.YouTube.Count != 10 {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(w.twCalls[0], []string{"examplechannel", "second", "third"}) {
		t.Fatal(w.twCalls[0])
	}
}

func TestPollerTheTimerOnlyPollsWhileAPageIsConnected(t *testing.T) {
	var looking atomic.Bool
	var polls atomic.Int64
	vc := New(Options{
		ListSources: func() []Source { return []Source{tw("examplechannel")} },
		Twitch: func(context.Context, []string) (Twitch, error) {
			polls.Add(1)
			return Twitch{State: "ok", Live: map[string]float64{}}, nil
		},
		YouTube:  func(context.Context, Source) (*YouTube, error) { return nil, nil },
		Active:   looking.Load,
		Interval: 5 * time.Millisecond,
	})
	vc.Start()
	time.Sleep(40 * time.Millisecond)
	if polls.Load() != 0 {
		t.Fatalf("%d polls while nobody looked", polls.Load())
	}
	looking.Store(true)
	time.Sleep(40 * time.Millisecond)
	if polls.Load() < 1 {
		t.Fatal("no poll while a page was connected")
	}
	vc.Stop()
	time.Sleep(10 * time.Millisecond) // a tick already fired may still be polling
	after := polls.Load()
	time.Sleep(30 * time.Millisecond)
	if polls.Load() != after {
		t.Fatalf("polled after stop: %d then %d", after, polls.Load())
	}
}

func TestPollerAfterASignInItAsksAgainEvenIfAPollWasAlreadyOnTheWire(t *testing.T) {
	// The poll in flight asked before the sign-in and will say "signed out".
	// RefreshNow must not hand that stale answer back as the fresh one.
	var signedIn atomic.Bool
	held, entered := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	w := newWorld(func([]string) (Twitch, error) {
		answer := Twitch{State: "signed_out", Note: "Sign in to Twitch on /setup to show viewers."}
		if signedIn.Load() {
			answer = Twitch{State: "ok", Live: map[string]float64{"examplechannel": 42}}
		}
		if first.CompareAndSwap(true, false) {
			close(entered)
			<-held
		}
		return answer, nil
	}, nil)
	w.add(tw("examplechannel"))
	inFlight := make(chan hub.ViewerCounts, 1)
	go func() { inFlight <- w.vc.Refresh(bg) }()
	<-entered
	signedIn.Store(true)
	after := make(chan hub.ViewerCounts, 1)
	go func() { after <- w.vc.RefreshNow(bg) }()
	time.Sleep(10 * time.Millisecond) // RefreshNow is waiting on the poll in flight
	close(held)
	if got := (<-inFlight).Twitch.State; got != "unknown" {
		t.Fatalf("in flight: %s", got)
	}
	if got := (<-after).Twitch; !reflect.DeepEqual(got, liveCount(42)) {
		t.Fatalf("after: %+v", got)
	}
	w.mu.Lock()
	n, states := len(w.twCalls), []string{}
	for _, c := range w.changes {
		states = append(states, c.Twitch.State)
	}
	w.mu.Unlock()
	if n != 2 || !reflect.DeepEqual(states, []string{"unknown", "live"}) {
		t.Fatalf("%d calls, changes %v", n, states)
	}
	// with nothing in flight it's just a poll
	w.vc.RefreshNow(bg)
	if len(w.twCalls) != 3 {
		t.Fatalf("%d calls", len(w.twCalls))
	}
}

// As returned on 2026-11-23 for a broadcast that was scheduled and not yet
// started, with one browser on its page. Trimmed.
func waitingMeta(n int, text string, date *string) m {
	d := "Scheduled for Nov 22, 2026"
	if date != nil {
		d = *date
	}
	if text == "" {
		text = strconv.Itoa(n) + " waiting"
	}
	return m{"actions": []any{
		m{"updateViewershipAction": m{"viewCount": m{"videoViewCountRenderer": m{
			"viewCount": m{"simpleText": text}, "isLive": true,
			"extraShortViewCount": m{"simpleText": strconv.Itoa(n)}, "unlabeledViewCountValue": m{"simpleText": strconv.Itoa(n)}, "originalViewCount": strconv.Itoa(n),
		}}}},
		m{"updateDateTextAction": m{"dateText": m{"simpleText": d}}},
		m{"updateTitleAction": m{}},
	}}
}

func ptr(s string) *string { return &s }

func TestParsePeopleWaitingForABroadcastToStartAreNotPeopleWatchingIt(t *testing.T) {
	cases := []struct {
		in   any
		want *YouTube
	}{
		{waitingMeta(1, "", nil), notLive},
		{waitingMeta(250, "", nil), notLive},
		// either sign is enough on its own
		{waitingMeta(1, "", ptr("")), notLive},
		{waitingMeta(1, "1 watching now", nil), notLive},
		{waitingMeta(1, "Waiting", ptr("")), notLive},
		// once it has started, the same shape counts again
		{waitingMeta(7, "7 watching now", ptr("Started streaming 4 minutes ago")), live(7)},
		// wording this doesn't recognise is counted, as it was before
		{waitingMeta(7, "7 Zuschauer", ptr("Livestream vom 22.11.2026")), live(7)},
	}
	for i, c := range cases {
		if got := parse(t, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: %+v, want %+v", i, got, c.want)
		}
	}
	// a title with the word in it is not a sign of anything
	l := liveMeta(12, "")
	l["actions"] = append(l["actions"].([]any), m{"updateTitleAction": m{"title": m{"simpleText": "Scheduled maintenance, waiting on parts"}}})
	if got := parse(t, l); !reflect.DeepEqual(got, live(12)) {
		t.Fatalf("%+v", got)
	}
}

func TestTheBarShowsNoYouTubeNumberWhileTheBroadcastIsOnlyScheduled(t *testing.T) {
	var mu sync.Mutex
	answer := parse(t, waitingMeta(1, "", nil))
	counts := New(Options{
		ListSources: func() []Source { return []Source{yt("FAKEvideo01")} },
		Twitch: func(context.Context, []string) (Twitch, error) {
			return Twitch{State: "ok", Live: map[string]float64{}}, nil
		},
		YouTube: func(context.Context, Source) (*YouTube, error) { mu.Lock(); defer mu.Unlock(); return answer, nil },
	})
	if got := counts.Refresh(bg).YouTube; !reflect.DeepEqual(got, hub.Count{State: "offline"}) {
		t.Fatalf("%+v", got)
	}
	mu.Lock()
	answer = parse(t, liveMeta(3, ""))
	mu.Unlock()
	if got := counts.Refresh(bg).YouTube; !reflect.DeepEqual(got, liveCount(3)) {
		t.Fatalf("%+v", got)
	}
}
