// Package youtube reads YouTube live chat through InnerTube, the interface
// the chat page itself uses — no API key, no quota (multichat
// lib/youtube.js at 29a9d2c), ported step for step and checked against
// goldens generated from that file (testdata/youtube).
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/sources"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// What the requests say they are (multichat's values).
const (
	UA            = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	Cookie        = "SOCS=CAI"
	DefaultOrigin = "https://www.youtube.com"
)

var offlineStatus = map[string]string{
	"LIVE_STREAM_OFFLINE": "the stream hasn't started yet",
	"LOGIN_REQUIRED":      "the video is private or age-restricted",
	"UNPLAYABLE":          "the video isn't available (members only?)",
	"ERROR":               "the video isn't available",
}

// step is one stage of the reader: it returns the next stage and how long
// to wait first, or nil to stop.
type step func(ctx context.Context) (next string, after time.Duration)

// Reader is one YouTube source's chat.
type Reader struct {
	URL    string
	H      app.Handlers
	Origin string // "" → https://www.youtube.com
	Client *http.Client
	Rand   func() float64
	Sleep  func(ctx context.Context, d time.Duration) bool

	mu             sync.Mutex
	VideoID        string
	OwnerChannelID string
	APIKey         string
	ClientVersion  string
	Continuation   string
	ErrStreak      int
	seen           map[string]bool
	seenQ          []string
	cancel         context.CancelFunc
	done           chan struct{}
}

// Factory builds YouTube readers for the app. origin "" means YouTube.
func Factory(origin string) app.Factory {
	return func(e sources.Entry, h app.Handlers) (app.Adapter, string, error) {
		return &Reader{URL: e.URL, H: h, Origin: origin}, "", nil
	}
}

func (r *Reader) defaults() {
	if r.Origin == "" {
		r.Origin = DefaultOrigin
	}
	if r.Client == nil {
		r.Client = &http.Client{}
	}
	if r.Rand == nil {
		r.Rand = rand.Float64
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
	if r.ClientVersion == "" {
		r.ClientVersion = "2.20260708.00.00"
	}
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
}

// Start resolves the link and reads until Stop.
func (r *Reader) Start() {
	r.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancel, r.done = cancel, make(chan struct{})
	r.mu.Unlock()
	go r.run(ctx)
}

// Stop ends the reader and any request in flight.
func (r *Reader) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Done is closed once the reader has stopped.
func (r *Reader) Done() <-chan struct{} { return r.done }

func (r *Reader) run(ctx context.Context) {
	defer close(r.done)
	defer func() {
		if v := recover(); v != nil {
			r.H.Status("error", fmt.Sprint("YouTube reader crashed: ", v))
		}
	}()
	next, after := "resolve", time.Duration(0)
	for ctx.Err() == nil && next != "" {
		if after > 0 && !r.Sleep(ctx, after) {
			return
		}
		next, after = r.Step(ctx, next)
	}
}

// Step runs one named stage — resolve, initChat or poll — and returns the
// next one and its delay. Exported for the golden tests.
func (r *Reader) Step(ctx context.Context, name string) (string, time.Duration) {
	r.defaults()
	switch name {
	case "resolve":
		return r.resolve(ctx)
	case "initChat":
		return r.initChat(ctx)
	case "poll":
		return r.poll(ctx)
	}
	return "", 0
}

func ms(n float64) time.Duration { return time.Duration(n * float64(time.Millisecond)) }

func (r *Reader) fetch(ctx context.Context, method, u string, body []byte, extra [][2]string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("user-agent", UA)
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("cookie", Cookie)
	for _, h := range extra {
		req.Header.Set(h[0], h[1])
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error { err := c.ReadCloser.Close(); c.cancel(); return err }

func readAll(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

var (
	schemeRE    = regexp.MustCompile(`(?i)^https?:`)
	trailSlash  = regexp.MustCompile(`/+$`)
	tabRE       = regexp.MustCompile(`/(streams|videos|featured|community|about)$`)
	liveRE      = regexp.MustCompile(`/live$`)
	canonicalRE = regexp.MustCompile(`<link rel="canonical" href="https://www\.youtube\.com/watch\?v=([A-Za-z0-9_-]{11})"`)
)

// errMsg is how an error reads in a status line: the reason, without Go's
// request plumbing around it.
func errMsg(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func (r *Reader) resolve(ctx context.Context) (string, time.Duration) {
	r.H.Status("connecting", "looking for the stream…")
	id := directVideoID(r.URL)
	if id == "" {
		link := r.URL
		if !schemeRE.MatchString(link) {
			link = "https://" + link
		}
		u, err := url.Parse(link)
		if err != nil {
			return r.resolveFailed(ctx, err)
		}
		path := tabRE.ReplaceAllString(trailSlash.ReplaceAllString(u.EscapedPath(), ""), "")
		if !liveRE.MatchString(path) {
			path += "/live"
		}
		resp, err := r.fetch(ctx, http.MethodGet, r.Origin+path, nil, nil)
		if err != nil {
			return r.resolveFailed(ctx, err)
		}
		m := canonicalRE.FindStringSubmatch(readAll(resp))
		if m == nil {
			r.H.Status("offline", "the channel isn't live — checking once a minute")
			return "resolve", 60 * time.Second
		}
		id = m[1]
	}
	r.mu.Lock()
	r.VideoID = id
	r.mu.Unlock()
	return "initChat", 0
}

func (r *Reader) resolveFailed(ctx context.Context, err error) (string, time.Duration) {
	if ctx.Err() != nil {
		return "", 0
	}
	r.H.Status("error", "can't reach YouTube: "+errMsg(err))
	return "resolve", 30 * time.Second
}

var (
	apiKeyRE     = regexp.MustCompile(`"INNERTUBE_API_KEY"\s*:\s*"([^"]+)"`)
	cvRE         = regexp.MustCompile(`"INNERTUBE_CONTEXT_CLIENT_VERSION"\s*:\s*"([^"]+)"`)
	cvFallbackRE = regexp.MustCompile(`"clientVersion"\s*:\s*"([\d.]+)"`)
	replayRE     = regexp.MustCompile(`"isReplay"\s*:\s*true`)
	playRE       = regexp.MustCompile(`"playabilityStatus"\s*:\s*\{\s*"status"\s*:\s*"(\w+)"`)
	lcrHeadRE    = regexp.MustCompile(`"liveChatRenderer"\s*:\s*\{`)
	lcrTailRE    = regexp.MustCompile(`"continuations"\s*:\s*\[\s*\{\s*"reloadContinuationData"\s*:\s*\{\s*"continuation"\s*:\s*"([^"]+)"`)
)

// fallbackContinuation is multichat's regex
//
//	"liveChatRenderer"\s*:\s*\{[\s\S]{0,4000}?"continuations"…"continuation"\s*:\s*"([^"]+)"
//
// which Go can't compile (RE2 stops at 1000 repeats): the first
// "liveChatRenderer": { followed within 4000 characters by the tail.
func fallbackContinuation(html string) string {
	for off := 0; off < len(html); {
		loc := lcrHeadRE.FindStringIndex(html[off:])
		if loc == nil {
			return ""
		}
		start := off + loc[1]
		window := html[start:]
		if m := lcrTailRE.FindStringSubmatchIndex(window); m != nil && len([]rune(window[:m[0]])) <= 4000 {
			return window[m[2]:m[3]]
		}
		off += loc[0] + 1
	}
	return ""
}

func (r *Reader) initChat(ctx context.Context) (string, time.Duration) {
	r.mu.Lock()
	videoID := r.VideoID
	r.mu.Unlock()
	fail := func(err error) (string, time.Duration) {
		if ctx.Err() != nil {
			return "", 0
		}
		r.H.Status("error", "couldn't start the chat: "+errMsg(err))
		return "resolve", 30 * time.Second
	}
	resp, err := r.fetch(ctx, http.MethodGet, r.Origin+"/watch?v="+videoID, nil, nil)
	if err != nil {
		return fail(err)
	}
	html := readAll(resp)
	r.mu.Lock()
	if m := apiKeyRE.FindStringSubmatch(html); m != nil {
		r.APIKey = m[1]
	}
	if m := cvRE.FindStringSubmatch(html); m != nil {
		r.ClientVersion = m[1]
	} else if m := cvFallbackRE.FindStringSubmatch(html); m != nil {
		r.ClientVersion = m[1]
	}
	r.OwnerChannelID = ownerChannelID(html)
	r.mu.Unlock()

	if replayRE.MatchString(html) {
		r.H.Status("offline", "this stream has already ended")
		return "resolve", 60 * time.Second
	}

	data := extractJSON(html, "ytInitialData")
	bar := get(data, "contents", "twoColumnWatchNextResults", "conversationBar")
	base := str(get(bar, "liveChatRenderer", "continuations", 0, "reloadContinuationData", "continuation"))
	if base == "" {
		base = fallbackContinuation(html)
	}
	if base == "" {
		why := simpleOrRuns(get(bar, "conversationBarRenderer", "availabilityMessage", "messageRenderer", "text"))
		if why == "" {
			if m := playRE.FindStringSubmatch(html); m != nil {
				why = offlineStatus[m[1]]
			}
		}
		if why == "" {
			why = "the chat isn't available"
		}
		r.H.Status("offline", why+" — checking once a minute")
		return "resolve", 60 * time.Second
	}

	token := base
	var seed []any
	if resp, err := r.fetch(ctx, http.MethodGet, r.Origin+"/live_chat?continuation="+url.QueryEscape(base), nil, nil); err == nil {
		cdata := extractJSON(readAll(resp), "ytInitialData")
		lcc := get(cdata, "continuationContents", "liveChatContinuation")
		if lcc == nil {
			lcc = get(cdata, "contents", "liveChatRenderer") // legacy shape
		}
		if lcc != nil {
			items := get(lcc, "header", "liveChatHeaderRenderer", "viewSelector", "sortFilterSubMenuRenderer", "subMenuItems")
			if all := str(get(items, 1, "continuation", "reloadContinuationData", "continuation")); all != "" {
				token = all // 0 = Top chat, 1 = Live chat
			} else if t, _, ok := contData(get(lcc, "continuations")); ok {
				token = t
			}
			seed = list(get(lcc, "actions"))
		}
	} // the seed page is best-effort; polling with the base token still works
	if ctx.Err() != nil {
		return "", 0
	}
	r.mu.Lock()
	r.Continuation, r.ErrStreak = token, 0
	r.mu.Unlock()
	r.H.Status("live", "chat connected")
	go r.meta(ctx, videoID)
	r.handleActions(seed)
	return "poll", time.Second
}

// meta is the channel title via oEmbed: nice to have, never fatal.
func (r *Reader) meta(ctx context.Context, videoID string) {
	u := r.Origin + "/oembed?url=" + url.QueryEscape(r.Origin+"/watch?v="+videoID) + "&format=json"
	resp, err := r.fetch(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return
	}
	body := readAll(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return
	}
	var j struct {
		AuthorName string `json:"author_name"`
	}
	if json.Unmarshal([]byte(body), &j) == nil && j.AuthorName != "" && ctx.Err() == nil {
		r.H.Meta(j.AuthorName)
	}
}

// encodeURIComponent, as the referer header needs it.
func encodeURIComponent(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	for _, c := range []string{"!", "'", "(", ")", "*"} {
		e = strings.ReplaceAll(e, url.QueryEscape(c), c)
	}
	return e
}

func (r *Reader) poll(ctx context.Context) (string, time.Duration) {
	r.mu.Lock()
	key, cv, cont := r.APIKey, r.ClientVersion, r.Continuation
	r.mu.Unlock()
	qs := "prettyPrint=false"
	if key != "" {
		qs = "key=" + encodeURIComponent(key) + "&" + qs
	}
	ref := encodeURIComponent(cont)
	if len(ref) > 512 {
		ref = ref[:512]
	}
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "WEB", "clientVersion": cv, "hl": "en", "gl": "US", "timeZone": "UTC", "utcOffsetMinutes": 0,
		}},
		"continuation": cont,
	})
	resp, err := r.fetch(ctx, http.MethodPost, r.Origin+"/youtubei/v1/live_chat/get_live_chat?"+qs, body, [][2]string{
		{"content-type", "application/json"},
		{"origin", r.Origin},
		{"x-origin", r.Origin},
		{"x-youtube-client-name", "1"},
		{"x-youtube-client-version", cv},
		{"referer", r.Origin + "/live_chat?continuation=" + ref},
	})
	if err != nil {
		return r.pollFailed(ctx, err)
	}
	text := readAll(resp)
	switch {
	case resp.StatusCode == 400 || resp.StatusCode == 404:
		r.H.Status("connecting", "chat token went stale, reconnecting…")
		return "initChat", 3 * time.Second
	case resp.StatusCode == 429 || resp.StatusCode == 403:
		retry, _ := strconv.ParseFloat(strings.TrimSpace(resp.Header.Get("retry-after")), 64)
		if math.IsNaN(retry) {
			retry = 0
		}
		r.mu.Lock()
		r.ErrStreak++
		r.mu.Unlock()
		return "poll", ms(math.Max(retry*1000, 15000))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return r.pollFailed(ctx, fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	j, err := parse([]byte(text))
	if err != nil {
		return r.pollFailed(ctx, err)
	}
	lcc := get(j, "continuationContents", "liveChatContinuation")
	if lcc == nil {
		r.H.Status("offline", "the stream has ended — waiting for a new one")
		return "resolve", 60 * time.Second
	}
	t, timeout, ok := contData(get(lcc, "continuations"))
	if ok && t != "" {
		r.mu.Lock()
		r.Continuation = t
		r.mu.Unlock()
	}
	r.handleActions(list(get(lcc, "actions")))
	r.mu.Lock()
	r.ErrStreak = 0
	r.mu.Unlock()
	wait := 5000.0
	if timeout != nil && *timeout != 0 {
		wait = *timeout
	}
	return "poll", ms(math.Max(1000, math.Min(8000, wait)))
}

func (r *Reader) pollFailed(ctx context.Context, err error) (string, time.Duration) {
	if ctx.Err() != nil {
		return "", 0
	}
	r.mu.Lock()
	r.ErrStreak++
	streak := r.ErrStreak
	if streak >= 5 {
		r.ErrStreak = 0
	}
	r.mu.Unlock()
	if streak >= 5 {
		r.H.Status("error", "lost the chat, reconnecting: "+errMsg(err))
		return "initChat", 5 * time.Second
	}
	return "poll", ms(math.Min(60000, 2000*math.Pow(2, float64(streak))) + r.Rand()*1000)
}

func (r *Reader) remember(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[id] {
		return false
	}
	r.seen[id] = true
	r.seenQ = append(r.seenQ, id)
	for len(r.seenQ) > 3000 {
		delete(r.seen, r.seenQ[0])
		r.seenQ = r.seenQ[1:]
	}
	return true
}

func (r *Reader) handleActions(actions []any) {
	for _, a := range actions {
		switch {
		case get(a, "replayChatItemAction") != nil:
			r.handleActions(list(get(a, "replayChatItemAction", "actions")))
		case get(a, "addChatItemAction") != nil:
			r.emit(get(a, "addChatItemAction", "item"))
		case get(a, "replaceChatItemAction") != nil:
			r.emit(get(a, "replaceChatItemAction", "replacementItem"))
		case get(a, "markChatItemAsDeletedAction") != nil:
			r.H.Remove("yt:" + jsText(get(a, "markChatItemAsDeletedAction", "targetItemId")))
		case get(a, "removeChatItemAction") != nil:
			r.H.Remove("yt:" + jsText(get(a, "removeChatItemAction", "targetItemId")))
		case get(a, "markChatItemsByAuthorAsDeletedAction") != nil:
			r.H.RemoveAuthor(jsText(get(a, "markChatItemsByAuthorAsDeletedAction", "externalChannelId")))
		}
		// ticker, tooltip, poll and banner actions are ignored
	}
}

// jsText is how JavaScript writes a value into a string: undefined for
// missing.
func jsText(v any) string {
	if v == nil {
		return "undefined"
	}
	return fmt.Sprint(v)
}

func (r *Reader) emit(item any) {
	fields, id := parseItem(item)
	if fields != nil && r.remember(id) {
		r.H.Message(fields)
	}
}

// parseItem turns one chat item into a message's fields, or nil for kinds
// that aren't shown (placeholders, engagement messages, mode changes).
func parseItem(item any) ([]app.Field, string) {
	o, _ := item.(*Obj)
	if o == nil {
		return nil, ""
	}
	idOf := func(r any) string { return "yt:" + jsText(get(r, "id")) }
	if r := get(o, "liveChatTextMessageRenderer"); r != nil {
		id := idOf(r)
		return []app.Field{{Key: "id", Value: id}, {Key: "kind", Value: "chat"}, {Key: "author", Value: authorFrom(r)}, {Key: "parts", Value: runsToParts(get(r, "message", "runs"))}}, id
	}
	if r := get(o, "liveChatPaidMessageRenderer"); r != nil {
		id := idOf(r)
		return []app.Field{{Key: "id", Value: id}, {Key: "kind", Value: "super"}, {Key: "author", Value: authorFrom(r)},
			{Key: "parts", Value: runsToParts(get(r, "message", "runs"))},
			{Key: "amount", Value: simpleOrRuns(get(r, "purchaseAmountText"))},
			{Key: "colors", Value: []app.Field{{Key: "header", Value: intColor(get(r, "headerBackgroundColor"))}, {Key: "body", Value: intColor(get(r, "bodyBackgroundColor"))}}}}, id
	}
	if r := get(o, "liveChatPaidStickerRenderer"); r != nil {
		id := idOf(r)
		return []app.Field{{Key: "id", Value: id}, {Key: "kind", Value: "super"}, {Key: "author", Value: authorFrom(r)},
			{Key: "parts", Value: []app.Part{}},
			{Key: "amount", Value: simpleOrRuns(get(r, "purchaseAmountText"))},
			{Key: "sticker", Value: thumbURL(get(r, "sticker"))},
			{Key: "colors", Value: []app.Field{{Key: "header", Value: intColor(get(r, "moneyChipBackgroundColor"))}, {Key: "body", Value: intColor(get(r, "backgroundColor"))}}}}, id
	}
	if r := get(o, "liveChatMembershipItemRenderer"); r != nil {
		id := idOf(r)
		sys := runsToText(get(r, "headerPrimaryText", "runs"))
		if sys == "" {
			sys = runsToText(get(r, "headerSubtext", "runs"))
		}
		if sys == "" {
			sys = simpleOrRuns(get(r, "headerSubtext"))
		}
		return []app.Field{{Key: "id", Value: id}, {Key: "kind", Value: "member"}, {Key: "author", Value: authorFrom(r)},
			{Key: "parts", Value: runsToParts(get(r, "message", "runs"))}, {Key: "system", Value: sys}}, id
	}
	if r := get(o, "liveChatSponsorshipsGiftPurchaseAnnouncementRenderer"); r != nil {
		hr := get(r, "header", "liveChatSponsorshipsHeaderRenderer")
		if hr == nil {
			return nil, ""
		}
		raw := str(get(r, "id"))
		if raw == "" {
			raw = strconv.FormatInt(rand.Int64N(1<<52), 36)
		}
		id := "yt:" + raw
		author := &Obj{Keys: []string{"authorName", "authorPhoto", "authorBadges", "authorExternalChannelId"}, M: map[string]any{
			"authorName": get(hr, "authorName"), "authorPhoto": get(hr, "authorPhoto"),
			"authorBadges": get(hr, "authorBadges"), "authorExternalChannelId": get(r, "authorExternalChannelId"),
		}}
		sys := runsToText(get(hr, "primaryText", "runs"))
		if sys == "" {
			sys = simpleOrRuns(get(hr, "primaryText"))
		}
		return []app.Field{{Key: "id", Value: id}, {Key: "kind", Value: "member"}, {Key: "author", Value: authorFrom(author)},
			{Key: "parts", Value: []app.Part{}}, {Key: "system", Value: sys}}, id
	}
	if r := get(o, "liveChatSponsorshipsGiftRedemptionAnnouncementRenderer"); r != nil {
		id := idOf(r)
		return []app.Field{{Key: "id", Value: id}, {Key: "kind", Value: "member"}, {Key: "author", Value: authorFrom(r)},
			{Key: "parts", Value: []app.Part{}}, {Key: "system", Value: runsToText(get(r, "message", "runs"))}}, id
	}
	return nil, ""
}

// InnerTube is the interface key and client version the watch page gave,
// which the free viewer count reuses.
func (r *Reader) InnerTube() (apiKey, clientVersion string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.APIKey, r.ClientVersion
}

// Target is the broadcast this chat is on and whose it is, for reply
// targets. Both are empty until the reader has found them.
func (r *Reader) Target() targets.Reader {
	r.mu.Lock()
	defer r.mu.Unlock()
	return targets.Reader{VideoID: r.VideoID, OwnerChannelID: r.OwnerChannelID}
}
