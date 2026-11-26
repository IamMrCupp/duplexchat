package youtube

// testdata/youtube through the Go port, compared with what multichat's own
// lib/youtube.js did with the same inputs (scripts/golden/youtube.js).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/golden"
)

// ru → en for multichat's status text; longest first.
var translate = [][2]string{
	{"канал сейчас не в эфире — проверяю раз в минуту", "the channel isn't live — checking once a minute"},
	{"видео приватное или с ограничением по возрасту", "the video is private or age-restricted"},
	{"видео недоступно (только для спонсоров?)", "the video isn't available (members only?)"},
	{"токен чата устарел, переподключаюсь…", "chat token went stale, reconnecting…"},
	{"трансляция завершилась — жду новую", "the stream has ended — waiting for a new one"},
	{"трансляция ещё не началась", "the stream hasn't started yet"},
	{"эта трансляция уже завершена", "this stream has already ended"},
	{"ошибка инициализации чата: ", "couldn't start the chat: "},
	{"потерял чат, переподключаюсь: ", "lost the chat, reconnecting: "},
	{" — проверяю раз в минуту", " — checking once a minute"},
	{"нет доступа к YouTube: ", "can't reach YouTube: "},
	{"ищу трансляцию…", "looking for the stream…"},
	{"видео недоступно", "the video isn't available"},
	{"чат недоступен", "the chat isn't available"},
	{"чат подключен", "chat connected"},
}

func english(s string) string {
	for _, t := range translate {
		s = strings.ReplaceAll(s, t[0], t[1])
	}
	return s
}

type event struct {
	Event   string            `json:"event"`
	Call    string            `json:"call,omitempty"`
	Args    []json.RawMessage `json:"args,omitempty"`
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	MS      float64           `json:"ms,omitempty"`
	Next    string            `json:"next,omitempty"`
	Step    string            `json:"step,omitempty"`
}

type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) add(e event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (r *recorder) handlers() app.Handlers {
	return app.Handlers{
		Message: func(f []app.Field) {
			r.add(event{Event: "call", Call: "onMessage", Args: []json.RawMessage{app.Encode(f)}})
		},
		Remove:       func(id string) { r.add(event{Event: "call", Call: "onRemove", Args: []json.RawMessage{raw(id)}}) },
		RemoveAuthor: func(k string) { r.add(event{Event: "call", Call: "onRemoveAuthor", Args: []json.RawMessage{raw(k)}}) },
		Clear:        func() { r.add(event{Event: "call", Call: "onClear"}) },
		Status: func(s, d string) {
			r.add(event{Event: "call", Call: "onStatus", Args: []json.RawMessage{raw(s), raw(d)}})
		},
		Meta: func(c string) {
			r.add(event{Event: "call", Call: "onMeta", Args: []json.RawMessage{raw(map[string]string{"channel": c})}})
		},
	}
}

type response struct {
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	Body     json.RawMessage   `json:"body"`
	Raw      *string           `json:"raw"`
	BodyFile string            `json:"bodyFile"`
	Throw    string            `json:"throw"`
}

type transport struct {
	rec    *recorder
	routes map[string][]response
	used   map[string]int
	mu     sync.Mutex
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()
	hdr := map[string]string{}
	for k, v := range req.Header {
		hdr[strings.ToLower(k)] = v[0]
	}
	var body json.RawMessage
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = b
	}
	t.rec.add(event{Event: "fetch", Method: req.Method, URL: req.URL.String(), Headers: hdr, Body: body})
	t.mu.Lock()
	list := t.routes[key]
	i := t.used[key]
	t.used[key] = i + 1
	t.mu.Unlock()
	if len(list) == 0 {
		return nil, errors.New("no route for " + key)
	}
	resp := list[int(math.Min(float64(i), float64(len(list)-1)))]
	if resp.Throw != "" {
		return nil, errors.New(resp.Throw)
	}
	var text []byte
	switch {
	case resp.BodyFile != "":
		text, _ = os.ReadFile(golden.Path(resp.BodyFile))
	case resp.Raw != nil:
		text = []byte(*resp.Raw)
	case resp.Body != nil:
		text = resp.Body
	default:
		text = []byte("{}")
	}
	h := http.Header{}
	for k, v := range resp.Headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: resp.Status, Header: h, Body: io.NopCloser(bytes.NewReader(text)), Request: req}, nil
}

// routes decodes a flow's routes: a response or a list of them.
func routes(t *testing.T, m map[string]json.RawMessage) map[string][]response {
	out := map[string][]response{}
	for k, v := range m {
		var l []response
		if json.Unmarshal(v, &l) != nil {
			var one response
			if err := json.Unmarshal(v, &one); err != nil {
				t.Fatal(err)
			}
			l = []response{one}
		}
		out[k] = l
	}
	return out
}

func isMeta(e event) bool {
	return e.Event == "call" && e.Call == "onMeta" || e.Event == "fetch" && strings.Contains(e.URL, "/oembed?")
}

func compact(b []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return string(b)
	}
	return buf.String()
}

func sameJSON(a, b []byte) bool {
	var x, y any
	json.Unmarshal(a, &x)
	json.Unmarshal(b, &y)
	return reflect.DeepEqual(x, y)
}

func compareEvents(t *testing.T, got, want []event) {
	t.Helper()
	var g, w, gm, wm []event
	for _, e := range got {
		if isMeta(e) {
			gm = append(gm, e)
		} else {
			g = append(g, e)
		}
	}
	for _, e := range want {
		if isMeta(e) {
			wm = append(wm, e)
		} else {
			w = append(w, e)
		}
	}
	if len(gm) != len(wm) {
		t.Errorf("oEmbed events: got %d, want %d", len(gm), len(wm))
	}
	if len(g) != len(w) {
		t.Fatalf("got %d events, want %d\n got %s\nwant %s", len(g), len(w), raw(g), raw(w))
	}
	for i := range g {
		a, b := g[i], w[i]
		bad := a.Event != b.Event
		switch {
		case bad:
		case a.Event == "step":
			bad = a.Step != b.Step
		case a.Event == "later":
			bad = a.Next != b.Next || (a.MS != b.MS && (math.Abs(a.MS-b.MS) >= 1000 || b.MS == math.Trunc(b.MS)))
		case a.Event == "fetch":
			bad = a.Method != b.Method || a.URL != b.URL
			for k, v := range b.Headers {
				bad = bad || a.Headers[k] != v
			}
			if len(b.Body) > 0 && string(b.Body) != "null" {
				bad = bad || !sameJSON(a.Body, b.Body)
			}
		case a.Event == "call":
			bad = a.Call != b.Call || len(a.Args) != len(b.Args)
			for j := 0; !bad && j < len(a.Args); j++ {
				wa := compact(b.Args[j])
				if a.Call == "onStatus" {
					var s string
					json.Unmarshal(b.Args[j], &s)
					wa = string(raw(english(s)))
				}
				bad = compact(a.Args[j]) != wa
			}
		}
		if bad {
			t.Errorf("event %d\n got  %s\n want %s", i, raw(a), raw(b))
		}
	}
}

func TestFlowGoldens(t *testing.T) {
	entries, err := os.ReadDir(golden.Path("youtube", "flows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		t.Run(name, func(t *testing.T) {
			var spec struct {
				URL    string                     `json:"url"`
				State  map[string]any             `json:"state"`
				Steps  []string                   `json:"steps"`
				Routes map[string]json.RawMessage `json:"routes"`
			}
			golden.Load(t, &spec, "youtube", "flows", e.Name())
			var want struct {
				Events []event        `json:"events"`
				State  map[string]any `json:"state"`
			}
			golden.Load(t, &want, "youtube", "golden", "flows", e.Name())

			rec := &recorder{}
			tr := &transport{rec: rec, routes: routes(t, spec.Routes), used: map[string]int{}}
			r := &Reader{URL: spec.URL, H: rec.handlers(), Client: &http.Client{Transport: tr}, Rand: func() float64 { return 0.5 }}
			r.defaults()
			for k, v := range spec.State {
				s, _ := v.(string)
				switch k {
				case "videoId":
					r.VideoID = s
				case "apiKey":
					r.APIKey = s
				case "continuation":
					r.Continuation = s
				case "clientVersion":
					r.ClientVersion = s
				case "errStreak":
					r.ErrStreak = int(v.(float64))
				}
			}
			ctx := context.Background()
			for _, step := range spec.Steps {
				rec.add(event{Event: "step", Step: step})
				next, after := r.Step(ctx, step)
				for next != "" && after == 0 {
					next, after = r.Step(ctx, next)
				}
				if next != "" {
					rec.add(event{Event: "later", MS: float64(after) / 1e6, Next: next})
				}
			}
			waitMeta(rec, want.Events)
			rec.mu.Lock()
			got := append([]event(nil), rec.events...)
			rec.mu.Unlock()
			compareEvents(t, got, want.Events)

			gotState := map[string]any{"videoId": r.VideoID, "ownerChannelId": r.OwnerChannelID, "apiKey": r.APIKey,
				"clientVersion": r.ClientVersion, "continuation": r.Continuation, "errStreak": float64(r.ErrStreak)}
			for k, w := range want.State {
				if w == nil {
					w = ""
				}
				if gotState[k] != w {
					t.Errorf("state %s = %v, want %v", k, gotState[k], w)
				}
			}
		})
	}
}

// waitMeta gives the oEmbed goroutine time to report when the golden says
// it did.
func waitMeta(rec *recorder, want []event) {
	n := 0
	for _, e := range want {
		if isMeta(e) {
			n++
		}
	}
	for i := 0; i < 200; i++ {
		rec.mu.Lock()
		got := 0
		for _, e := range rec.events {
			if isMeta(e) {
				got++
			}
		}
		rec.mu.Unlock()
		if got >= n {
			return
		}
		<-time.After(5 * time.Millisecond)
	}
}

func TestActionGoldens(t *testing.T) {
	entries, err := os.ReadDir(golden.Path("youtube", "actions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		t.Run(name, func(t *testing.T) {
			var batches []json.RawMessage
			golden.Load(t, &batches, "youtube", "actions", e.Name())
			var want []event
			golden.Load(t, &want, "youtube", "golden", "actions", e.Name())
			rec := &recorder{}
			r := &Reader{H: rec.handlers()}
			r.defaults()
			for _, b := range batches {
				v, err := parse(b)
				if err != nil {
					t.Fatal(err)
				}
				r.handleActions(list(v))
				rec.add(event{Event: "batch-end"})
			}
			// the one gift without an ID gets a random one in both versions
			for _, ev := range [][]event{rec.events, want} {
				for i := range ev {
					for j := range ev[i].Args {
						s := compact(ev[i].Args[j])
						if k := strings.Index(s, `"id":"yt:`); k >= 0 && strings.Contains(s, "No Id Gifter") {
							end := strings.Index(s[k+9:], `"`)
							ev[i].Args[j] = json.RawMessage(s[:k+9] + "RANDOM" + s[k+9+end:])
						}
					}
				}
			}
			compareEvents(t, rec.events, want)
		})
	}
}

func TestHelperGoldens(t *testing.T) {
	var h struct {
		DirectVideoID []struct {
			URL string  `json:"url"`
			ID  *string `json:"id"`
		} `json:"directVideoId"`
		IntColor []struct {
			N     json.RawMessage `json:"n"`
			Color *string         `json:"color"`
		} `json:"intColor"`
		ThumbURL []struct {
			Obj json.RawMessage `json:"obj"`
			URL *string         `json:"url"`
		} `json:"thumbUrl"`
		Runs []struct {
			Runs  json.RawMessage `json:"runs"`
			Parts json.RawMessage `json:"parts"`
			Text  string          `json:"text"`
		} `json:"runs"`
		SimpleOrRuns []struct {
			Node json.RawMessage `json:"node"`
			Text string          `json:"text"`
		} `json:"simpleOrRuns"`
		AuthorFrom []struct {
			R      json.RawMessage `json:"r"`
			Author json.RawMessage `json:"author"`
		} `json:"authorFrom"`
		ContData []struct {
			Conts json.RawMessage `json:"conts"`
			Data  *struct {
				Token     string   `json:"token"`
				TimeoutMs *float64 `json:"timeoutMs"`
			} `json:"data"`
		} `json:"contData"`
	}
	golden.Load(t, &h, "youtube", "golden", "helpers.json")
	p := func(b json.RawMessage) any { v, _ := parse(b); return v }
	for _, c := range h.DirectVideoID {
		want := ""
		if c.ID != nil {
			want = *c.ID
		}
		if got := directVideoID(c.URL); got != want {
			t.Errorf("directVideoID(%q) = %q, want %q", c.URL, got, want)
		}
	}
	for _, c := range h.IntColor {
		if got := intColor(p(c.N)); !reflect.DeepEqual(got, c.Color) {
			t.Errorf("intColor(%s) = %v, want %v", c.N, deref(got), deref(c.Color))
		}
	}
	for _, c := range h.ThumbURL {
		if got := thumbURL(p(c.Obj)); !reflect.DeepEqual(got, c.URL) {
			t.Errorf("thumbURL(%s) = %v, want %v", c.Obj, deref(got), deref(c.URL))
		}
	}
	for _, c := range h.Runs {
		if got := raw(runsToParts(p(c.Runs))); compact(got) != compact(c.Parts) {
			t.Errorf("runsToParts(%s) = %s, want %s", c.Runs, got, c.Parts)
		}
		if got := runsToText(p(c.Runs)); got != c.Text {
			t.Errorf("runsToText(%s) = %q, want %q", c.Runs, got, c.Text)
		}
	}
	for _, c := range h.SimpleOrRuns {
		if got := simpleOrRuns(p(c.Node)); got != c.Text {
			t.Errorf("simpleOrRuns(%s) = %q, want %q", c.Node, got, c.Text)
		}
	}
	for _, c := range h.AuthorFrom {
		if got := app.Encode(authorFrom(p(c.R))); compact(got) != compact(c.Author) {
			t.Errorf("authorFrom(%s)\n got  %s\n want %s", c.R, got, compact(c.Author))
		}
	}
	for _, c := range h.ContData {
		tok, to, ok := contData(p(c.Conts))
		switch {
		case c.Data == nil && ok:
			t.Errorf("contData(%s) = %q, want none", c.Conts, tok)
		case c.Data != nil && (!ok || tok != c.Data.Token || !reflect.DeepEqual(to, c.Data.TimeoutMs)):
			t.Errorf("contData(%s) = %q %v, want %q %v", c.Conts, tok, deref(to), c.Data.Token, deref(c.Data.TimeoutMs))
		}
	}
	var pages []struct {
		Page           string          `json:"page"`
		YTInitialData  json.RawMessage `json:"ytInitialData"`
		OwnerChannelID *string         `json:"ownerChannelId"`
	}
	golden.Load(t, &pages, "youtube", "golden", "pages.json")
	for _, pg := range pages {
		html := string(golden.Read(t, "youtube", "pages", pg.Page))
		want := ""
		if pg.OwnerChannelID != nil {
			want = *pg.OwnerChannelID
		}
		if got := ownerChannelID(html); got != want {
			t.Errorf("%s: ownerChannelID = %q, want %q", pg.Page, got, want)
		}
		got := extractJSON(html, "ytInitialData")
		if !sameJSON(raw(plain(got)), pg.YTInitialData) {
			t.Errorf("%s: ytInitialData = %s, want %s", pg.Page, raw(plain(got)), pg.YTInitialData)
		}
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// plain turns *Obj trees back into maps for comparison.
func plain(v any) any {
	switch x := v.(type) {
	case *Obj:
		m := map[string]any{}
		for k, val := range x.M {
			m[k] = plain(val)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = plain(x[i])
		}
		return out
	}
	return v
}
