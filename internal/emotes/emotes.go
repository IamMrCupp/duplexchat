// Package emotes loads BTTV, FFZ and 7TV emotes for a Twitch channel — the
// global sets and the channel's own (multichat lib/twitch.js at 29a9d2c:
// getJson, loadGlobalExt, loadChannelExt).
//
// One deliberate change: when two services have an emote with the same
// name, multichat's winner is whichever request finished last. Here they're
// applied in a fixed order — BTTV, then FFZ, then 7TV — so 7TV wins and the
// same emote shows every restart. Channel emotes still beat global ones.
package emotes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bases are the services' API roots; tests point them at a fake.
type Bases struct {
	BTTV, FFZ, SevenTV string
}

// Real is where the emotes actually live.
var Real = Bases{
	BTTV:    "https://api.betterttv.net/3",
	FFZ:     "https://api.frankerfacez.com/v1",
	SevenTV: "https://7tv.io/v3",
}

// Loader fetches emote lists. Global sets are fetched once per process and
// kept, even when every request failed, as multichat does.
type Loader struct {
	Bases  Bases
	Client *http.Client // 10 s timeout by default

	once   sync.Once
	global map[string]string
}

// New returns a loader for the real services.
func New() *Loader { return &Loader{Bases: Real} }

func (l *Loader) client() *http.Client {
	if l.Client != nil {
		return l.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (l *Loader) getJSON(ctx context.Context, url string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := l.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// normURL makes protocol-relative URLs https and refuses anything else
// that isn't https.
func normURL(u string) string {
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	if strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

func bttvURL(id string) string    { return "https://cdn.betterttv.net/emote/" + id + "/2x" }
func sevenTVURL(id string) string { return "https://cdn.7tv.app/emote/" + id + "/2x.webp" }

type bttvEmote struct{ ID, Code string }

type ffzEmote struct {
	Name string            `json:"name"`
	URLs map[string]string `json:"urls"`
}

func (e ffzEmote) url() string {
	u := e.URLs["2"]
	if u == "" {
		u = e.URLs["1"]
	}
	return normURL(u)
}

type ffzSet struct {
	Emoticons []ffzEmote `json:"emoticons"`
}

type sevenTVEmote struct{ ID, Name string }

// fetch runs the three services' requests at once and returns their maps
// in BTTV, FFZ, 7TV order. A failed service is an empty map.
func fetch(jobs [3]func() map[string]string) [3]map[string]string {
	var out [3]map[string]string
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = job()
		}()
	}
	wg.Wait()
	return out
}

func merge(dst map[string]string, srcs ...map[string]string) map[string]string {
	for _, m := range srcs {
		for k, v := range m {
			dst[k] = v
		}
	}
	return dst
}

func (l *Loader) loadGlobal(ctx context.Context) map[string]string {
	maps := fetch([3]func() map[string]string{
		func() map[string]string {
			var list []bttvEmote
			out := map[string]string{}
			if l.getJSON(ctx, l.Bases.BTTV+"/cached/emotes/global", &list) == nil {
				for _, e := range list {
					out[e.Code] = bttvURL(e.ID)
				}
			}
			return out
		},
		func() map[string]string {
			var j struct {
				DefaultSets []json.Number     `json:"default_sets"`
				Sets        map[string]ffzSet `json:"sets"`
			}
			out := map[string]string{}
			if l.getJSON(ctx, l.Bases.FFZ+"/set/global", &j) == nil {
				for _, id := range j.DefaultSets {
					for _, e := range j.Sets[id.String()].Emoticons {
						if u := e.url(); u != "" {
							out[e.Name] = u
						}
					}
				}
			}
			return out
		},
		func() map[string]string {
			var j struct{ Emotes []sevenTVEmote }
			out := map[string]string{}
			if l.getJSON(ctx, l.Bases.SevenTV+"/emote-sets/global", &j) == nil {
				for _, e := range j.Emotes {
					out[e.Name] = sevenTVURL(e.ID)
				}
			}
			return out
		},
	})
	return merge(map[string]string{}, maps[0], maps[1], maps[2])
}

// setOrder is JavaScript's own-key order for an object's keys: integer-like
// keys ascending, then the rest (here sorted, since a map has no order).
func setOrder(sets map[string]ffzSet) []string {
	var ints, rest []string
	for k := range sets {
		if n, err := strconv.ParseUint(k, 10, 32); err == nil && strconv.FormatUint(n, 10) == k {
			ints = append(ints, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.Slice(ints, func(i, j int) bool {
		a, _ := strconv.ParseUint(ints[i], 10, 32)
		b, _ := strconv.ParseUint(ints[j], 10, 32)
		return a < b
	})
	sort.Strings(rest)
	return append(ints, rest...)
}

func (l *Loader) loadChannel(ctx context.Context, roomID string) map[string]string {
	maps := fetch([3]func() map[string]string{
		func() map[string]string {
			var j struct{ ChannelEmotes, SharedEmotes []bttvEmote }
			out := map[string]string{}
			if l.getJSON(ctx, l.Bases.BTTV+"/cached/users/twitch/"+roomID, &j) == nil {
				for _, e := range append(j.ChannelEmotes, j.SharedEmotes...) {
					out[e.Code] = bttvURL(e.ID)
				}
			}
			return out
		},
		func() map[string]string {
			var j struct {
				Sets map[string]ffzSet `json:"sets"`
			}
			out := map[string]string{}
			if l.getJSON(ctx, l.Bases.FFZ+"/room/id/"+roomID, &j) == nil {
				for _, id := range setOrder(j.Sets) {
					for _, e := range j.Sets[id].Emoticons {
						if u := e.url(); u != "" {
							out[e.Name] = u
						}
					}
				}
			}
			return out
		},
		func() map[string]string {
			var j struct {
				EmoteSet struct{ Emotes []sevenTVEmote } `json:"emote_set"`
			}
			out := map[string]string{}
			if l.getJSON(ctx, l.Bases.SevenTV+"/users/twitch/"+roomID, &j) == nil {
				for _, e := range j.EmoteSet.Emotes {
					out[e.Name] = sevenTVURL(e.ID)
				}
			}
			return out
		},
	})
	return merge(map[string]string{}, maps[0], maps[1], maps[2])
}

// For returns a channel's third-party emotes: the global sets with the
// channel's own on top. Failures leave gaps, never an error.
func (l *Loader) For(ctx context.Context, roomID string) map[string]string {
	l.once.Do(func() { l.global = l.loadGlobal(ctx) })
	return merge(merge(map[string]string{}, l.global), l.loadChannel(ctx, roomID))
}
