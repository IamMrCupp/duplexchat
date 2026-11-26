package emotes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/golden"
)

// fake serves canned responses keyed by the real URL they stand for.
func fake(t *testing.T, responses map[string]json.RawMessage, hits *atomic.Int32) Bases {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		real := map[string]string{"/bttv": Real.BTTV, "/ffz": Real.FFZ, "/7tv": Real.SevenTV}
		for prefix, base := range real {
			if strings.HasPrefix(r.URL.Path, prefix) {
				if body, ok := responses[base+strings.TrimPrefix(r.URL.Path, prefix)]; ok {
					w.Write(body)
					return
				}
			}
		}
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return Bases{BTTV: srv.URL + "/bttv", FFZ: srv.URL + "/ffz", SevenTV: srv.URL + "/7tv"}
}

// The canned responses from testdata, through the Go loader, give the same
// map multichat's loaders gave (scripts/golden/twitch.js; sorted by name).
func TestEmoteMapGolden(t *testing.T) {
	var api map[string]json.RawMessage
	golden.Load(t, &api, "twitch", "emote-api.json")
	var want [][2]string
	golden.Load(t, &want, "twitch", "golden", "emote-map.json")
	got := (&Loader{Bases: fake(t, api, nil)}).For(context.Background(), "12345")
	var pairs [][2]string
	for k, v := range got {
		pairs = append(pairs, [2]string{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	a, _ := json.Marshal(pairs)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Errorf("got  %s\nwant %s", a, b)
	}
}

func TestCollisionsHaveAFixedOrder(t *testing.T) {
	api := map[string]json.RawMessage{
		Real.BTTV + "/cached/emotes/global":  json.RawMessage(`[{"id":"b","code":"Same"},{"id":"b2","code":"BvF"}]`),
		Real.FFZ + "/set/global":             json.RawMessage(`{"default_sets":[1],"sets":{"1":{"emoticons":[{"name":"Same","urls":{"2":"//f/2"}},{"name":"BvF","urls":{"2":"//f/bvf"}}]}}}`),
		Real.SevenTV + "/emote-sets/global":  json.RawMessage(`{"emotes":[{"id":"s","name":"Same"},{"id":"g","name":"ChanBeatsGlobal"}]}`),
		Real.BTTV + "/cached/users/twitch/1": json.RawMessage(`{"channelEmotes":[{"id":"c","code":"ChanBeatsGlobal"}]}`),
		Real.FFZ + "/room/id/1":              json.RawMessage(`{"sets":{"10":{"emoticons":[{"name":"Order","urls":{"2":"//f/10"}}]},"9":{"emoticons":[{"name":"Order","urls":{"2":"//f/9"}}]}}}`),
		Real.SevenTV + "/users/twitch/1":     json.RawMessage(`{"emote_set":null}`),
	}
	for i := 0; i < 20; i++ { // the order must not depend on which request finishes first
		got := (&Loader{Bases: fake(t, api, nil)}).For(context.Background(), "1")
		checks := map[string]string{
			"Same":            "https://cdn.7tv.app/emote/s/2x.webp",  // 7TV wins among services
			"BvF":             "https://f/bvf",                        // FFZ beats BTTV
			"ChanBeatsGlobal": "https://cdn.betterttv.net/emote/c/2x", // channel beats global
			"Order":           "https://f/10",                         // FFZ room sets in numeric key order
		}
		for k, want := range checks {
			if got[k] != want {
				t.Fatalf("run %d: %s = %q, want %q", i, k, got[k], want)
			}
		}
	}
}

func TestFailuresLeaveGaps(t *testing.T) {
	api := map[string]json.RawMessage{
		Real.SevenTV + "/emote-sets/global": json.RawMessage(`{"emotes":[{"id":"s","name":"Only7TV"}]}`),
		Real.FFZ + "/set/global":            json.RawMessage(`not json`),
	}
	got := (&Loader{Bases: fake(t, api, nil)}).For(context.Background(), "1")
	if len(got) != 1 || got["Only7TV"] == "" {
		t.Errorf("got %v", got)
	}
}

func TestGlobalFetchedOnce(t *testing.T) {
	var hits atomic.Int32
	l := &Loader{Bases: fake(t, map[string]json.RawMessage{}, &hits)}
	l.For(context.Background(), "1")
	l.For(context.Background(), "2")
	if n := hits.Load(); n != 9 { // 3 global once + 3 per channel × 2
		t.Errorf("%d requests, want 9", n)
	}
}

func TestNormURL(t *testing.T) {
	for in, want := range map[string]string{"//a/b": "https://a/b", "https://a": "https://a", "http://a": "", "": "", "ftp://a": ""} {
		if got := normURL(in); got != want {
			t.Errorf("normURL(%q) = %q", in, got)
		}
	}
}
