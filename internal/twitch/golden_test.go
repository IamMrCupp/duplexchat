package twitch

// Every testdata/twitch input through the Go port, compared with what
// multichat's own lib/twitch.js produced (scripts/golden/twitch.js).

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/golden"
)

func same(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	json.Unmarshal(b, &g)
	json.Unmarshal(want, &w)
	return reflect.DeepEqual(g, w)
}

func TestParseGolden(t *testing.T) {
	var cases []struct {
		Line   string          `json:"line"`
		Parsed json.RawMessage `json:"parsed"`
	}
	golden.Load(t, &cases, "twitch", "golden", "parse.json")
	for _, c := range cases {
		l := parseLine(c.Line)
		got := map[string]any{"tags": l.Tags, "prefix": l.Prefix, "cmd": l.Cmd, "params": l.Params, "trailing": l.Trailing}
		if !same(t, got, c.Parsed) {
			b, _ := json.Marshal(got)
			t.Errorf("%q\n got  %s\n want %s", c.Line, b, c.Parsed)
		}
	}
}

func TestSmallGoldens(t *testing.T) {
	var urls []struct {
		URL     string  `json:"url"`
		Channel *string `json:"channel"`
	}
	golden.Load(t, &urls, "twitch", "golden", "channel-urls.json")
	for _, c := range urls {
		want := ""
		if c.Channel != nil {
			want = *c.Channel
		}
		if got := ChannelFromURL(c.URL); got != want {
			t.Errorf("ChannelFromURL(%q) = %q, want %q", c.URL, got, want)
		}
	}
	var esc []struct{ Raw, Value string }
	golden.Load(t, &esc, "twitch", "golden", "tag-escapes.json")
	for _, c := range esc {
		if got := unescapeTag(c.Raw); got != c.Value {
			t.Errorf("unescapeTag(%q) = %q, want %q", c.Raw, got, c.Value)
		}
	}
	var badges []struct {
		Tag   string   `json:"tag"`
		Roles []string `json:"roles"`
	}
	golden.Load(t, &badges, "twitch", "golden", "badges.json")
	for _, c := range badges {
		if got := rolesFromBadges(c.Tag); strings.Join(got, ",") != strings.Join(c.Roles, ",") {
			t.Errorf("rolesFromBadges(%q) = %v, want %v", c.Tag, got, c.Roles)
		}
	}
}

type call struct {
	Call string            `json:"call"`
	Args []json.RawMessage `json:"args"`
}

type fakeConn struct{ calls *[]call }

func (f fakeConn) Read([]byte) (int, error) { select {} }
func (f fakeConn) Write(b []byte) (int, error) {
	a, _ := json.Marshal(string(b))
	*f.calls = append(*f.calls, call{"write", []json.RawMessage{a}})
	return len(b), nil
}
func (f fakeConn) Close() error {
	*f.calls = append(*f.calls, call{"destroy", []json.RawMessage{}})
	return nil
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

var randomID = regexp.MustCompile(`"id":"tw:[0-9a-z]+(:msg)?"`)

func compact(b []byte) string {
	var buf bytes.Buffer
	json.Compact(&buf, b)
	return buf.String()
}

// The lines that carry no id or source-id tag get a random ID in both
// versions; only its shape is compared.
func normal(b []byte, random bool) string {
	s := compact(b)
	if random {
		s = randomID.ReplaceAllString(s, `"id":"tw:RANDOM$1"`)
	}
	return s
}

func TestIRCGolden(t *testing.T) {
	var ext map[string]string
	golden.Load(t, &ext, "twitch", "ext-emotes.json")
	entries, err := os.ReadDir(golden.Path("twitch", "irc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".irc")
		t.Run(name, func(t *testing.T) {
			var want []struct {
				Line  string `json:"line"`
				Calls []call `json:"calls"`
			}
			golden.Load(t, &want, "twitch", "golden", "irc", name+".json")
			var calls []call
			r := &Reader{Channel: "examplechannel"}
			r.H = app.Handlers{
				Message:      func(f []app.Field) { calls = append(calls, call{"onMessage", []json.RawMessage{app.Encode(f)}}) },
				Remove:       func(id string) { calls = append(calls, call{"onRemove", []json.RawMessage{raw(id)}}) },
				RemoveAuthor: func(k string) { calls = append(calls, call{"onRemoveAuthor", []json.RawMessage{raw(k)}}) },
				Clear:        func() { calls = append(calls, call{"onClear", []json.RawMessage{}}) },
				Status:       func(s, d string) { calls = append(calls, call{"onStatus", []json.RawMessage{raw(s), raw(d)}}) },
			}
			r.conn = fakeConn{&calls}
			r.extLoaded = true // ROOMSTATE must not fetch
			if !strings.HasSuffix(name, "-noext") {
				r.ext = ext
			}
			for _, w := range want {
				calls = nil
				r.safeHandle(w.Line)
				random := !regexp.MustCompile(`(^@|;)(id|source-id)=[^;]`).MatchString(w.Line)
				if len(calls) != len(w.Calls) {
					t.Errorf("%s\n got %d calls, want %d", w.Line, len(calls), len(w.Calls))
					continue
				}
				for i := range calls {
					g, wc := calls[i], w.Calls[i]
					if g.Call != wc.Call || len(g.Args) != len(wc.Args) {
						t.Errorf("%s\n call %d: %s/%d, want %s/%d", w.Line, i, g.Call, len(g.Args), wc.Call, len(wc.Args))
						continue
					}
					for j := range g.Args {
						if gs, ws := normal(g.Args[j], random), normal(wc.Args[j], random); gs != ws {
							t.Errorf("%s\n call %d %s arg %d\n got  %s\n want %s", w.Line, i, g.Call, j, gs, ws)
						}
					}
				}
			}
		})
	}
}
