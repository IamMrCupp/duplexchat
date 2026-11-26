// Package targets decides which chat a reply goes to: one pinned source per
// platform (multichat lib/targets.js at 29a9d2c). This is the pin half —
// enough to write replyTo into config.json as multichat does. The dock's
// view and explicit choices land with sending (#9).
package targets

import "github.com/IamMrCupp/duplexchat/internal/config"

// Platforms in the order multichat writes them.
var Platforms = []string{"twitch", "youtube"}

// Source is what pinning needs to know about a source.
type Source struct {
	ID, URL, Platform string
}

// Pins holds the pinned source ID per platform.
type Pins struct {
	list func() []Source
	pin  map[string]string
}

// New returns empty pins over the live source list.
func New(list func() []Source) *Pins {
	return &Pins{list: list, pin: map[string]string{}}
}

func (p *Pins) of(platform string) []Source {
	var out []Source
	for _, s := range p.list() {
		if s.Platform == platform {
			out = append(out, s)
		}
	}
	return out
}

func known(platform string) bool { return platform == "twitch" || platform == "youtube" }

// Added: call after a source joins the list. It's pinned only when it's the
// platform's only source and nothing is pinned yet.
func (p *Pins) Added(s Source) {
	if !known(s.Platform) {
		return
	}
	if p.pin[s.Platform] == "" && len(p.of(s.Platform)) == 1 {
		p.pin[s.Platform] = s.ID
	}
}

// Removed: call after a source leaves the list. Its platform is left with no
// target rather than hopping to another source.
func (p *Pins) Removed(s Source) {
	if p.pin[s.Platform] == s.ID {
		p.pin[s.Platform] = ""
	}
}

// Restore re-applies config.json's replyTo by URL at startup (IDs are per
// run). URLs that match no source are ignored.
func (p *Pins) Restore(saved any) {
	m, _ := saved.(map[string]any)
	for _, platform := range Platforms {
		url, _ := m[platform].(string)
		if url == "" {
			continue
		}
		for _, s := range p.of(platform) {
			if s.URL == url {
				p.pin[platform] = s.ID
				break
			}
		}
	}
}

// ToConfig is replyTo as config.json holds it: platform → pinned URL.
func (p *Pins) ToConfig() config.Object {
	var out config.Object
	for _, platform := range Platforms {
		id := p.pin[platform]
		if id == "" {
			continue
		}
		for _, s := range p.of(platform) {
			if s.ID == id {
				out = append(out, config.Field{Key: platform, Value: s.URL})
				break
			}
		}
	}
	return out
}
