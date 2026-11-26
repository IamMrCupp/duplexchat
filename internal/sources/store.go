// Package sources is the list of chats being read: what the panel adds and
// removes, and what config.json saves (multichat server.js at 29a9d2c:
// detectPlatform, addSource, removeSource, startSources, saveConfig).
// Readers plug in with the panel routes (#16).
package sources

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/IamMrCupp/duplexchat/internal/config"
	"github.com/IamMrCupp/duplexchat/internal/info"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// ErrNotAStream is the panel's answer to a link that isn't Twitch or YouTube.
var ErrNotAStream = errors.New("That doesn't look like a Twitch or YouTube link")

var (
	schemeRE  = regexp.MustCompile(`(?i)^https?:`)
	twitchRE  = regexp.MustCompile(`(^|\.)twitch\.tv$`)
	youtubeRE = regexp.MustCompile(`(^|\.)youtube\.com$`)
)

// DetectPlatform returns "twitch", "youtube" or "" for a pasted link.
func DetectPlatform(link string) string {
	if !schemeRE.MatchString(link) {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case twitchRE.MatchString(h):
		return "twitch"
	case youtubeRE.MatchString(h) || h == "youtu.be":
		return "youtube"
	}
	return ""
}

// Entry is one source.
type Entry struct {
	ID, URL, Platform      string
	Channel, State, Detail string
}

// StatusItem is a source as the pages see it, in multichat's key order.
type StatusItem struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Platform string `json:"platform"`
	Channel  string `json:"channel"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
}

// Options are the store's seams.
type Options struct {
	NewID func() string // 8 lowercase hex characters by default
	Warn  func(string)  // stderr by default
}

// Store is the ordered source list plus everything saving depends on.
type Store struct {
	mu      sync.Mutex
	cfg     config.Config
	path    string
	opts    Options
	entries []Entry
	pins    *targets.Pins
}

// NewStore wraps a loaded config. path is where saves go.
func NewStore(cfg config.Config, path string, opts Options) *Store {
	if opts.NewID == nil {
		opts.NewID = randomID
	}
	if opts.Warn == nil {
		opts.Warn = func(s string) { fmt.Fprintln(os.Stderr, s) }
	}
	s := &Store{cfg: cfg, path: path, opts: opts}
	s.pins = targets.New(s.targetList)
	return s
}

func randomID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// targetList is called by the pins while the store's lock is held.
func (s *Store) targetList() []targets.Source {
	out := make([]targets.Source, len(s.entries))
	for i, e := range s.entries {
		out[i] = targets.Source{ID: e.ID, URL: e.URL, Platform: e.Platform}
	}
	return out
}

// List returns the sources in the order they were added.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// Status is the source list as /events and /api/state send it; [] when empty.
func (s *Store) Status() []StatusItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StatusItem, len(s.entries))
	for i, e := range s.entries {
		out[i] = StatusItem{e.ID, e.URL, e.Platform, e.Channel, e.State, e.Detail}
	}
	return out
}

// StartSources adds every saved source without saving, then restores the
// saved reply pins. Sources get new IDs every run; bad entries are reported
// and dropped from the file at the next save.
func (s *Store) StartSources() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range s.cfg.Sources {
		m, _ := raw.(map[string]any)
		u, ok := m["url"]
		if !ok || !config.Truthy(u) {
			continue
		}
		link, _ := u.(string)
		if _, err := s.add(link, false); err != nil {
			s.opts.Warn(fmt.Sprintf("Source %v: %v", u, err))
		}
	}
	s.pins.Restore(s.cfg.ReplyTo)
}

// Add adds a source from the panel and saves.
func (s *Store) Add(link string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(link, true)
}

func (s *Store) add(link string, save bool) (Entry, error) {
	platform := DetectPlatform(link)
	if platform == "" {
		return Entry{}, ErrNotAStream
	}
	// channel starts as the platform name until a reader learns the real one
	e := Entry{ID: s.opts.NewID(), URL: link, Platform: platform, Channel: platform, State: "connecting"}
	s.entries = append(s.entries, e)
	s.pins.Added(targets.Source{ID: e.ID, URL: e.URL, Platform: e.Platform})
	if save {
		s.save()
	}
	return e, nil
}

// Get returns one source.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// SetStatus records what a source's reader says about it. It reports
// whether the source still exists.
func (s *Store) SetStatus(id, state, detail string) bool {
	return s.update(id, func(e *Entry) { e.State, e.Detail = state, detail })
}

// SetChannel records the channel a reader resolved (a Twitch login, a
// YouTube channel title).
func (s *Store) SetChannel(id, channel string) bool {
	return s.update(id, func(e *Entry) { e.Channel = channel })
}

func (s *Store) update(id string, f func(*Entry)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID == id {
			f(&s.entries[i])
			return true
		}
	}
	return false
}

// Remove drops a source and saves. It reports whether the ID was known.
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			s.pins.Removed(targets.Source{ID: e.ID, URL: e.URL, Platform: e.Platform})
			s.save()
			return true
		}
	}
	return false
}

// Save writes config.json. A failure is reported and the app keeps running.
func (s *Store) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.save()
}

func (s *Store) save() {
	refs := make([]config.SourceRef, len(s.entries))
	for i, e := range s.entries {
		refs[i] = config.SourceRef{ID: e.ID, URL: e.URL}
	}
	var presets any
	if p := info.CleanPresets(s.cfg.InfoPresets); len(p) > 0 {
		presets = toAny(p)
	}
	saved := config.Saved{
		Host: s.cfg.Host, Port: s.cfg.Port, Sources: refs,
		TwitchClientID: s.cfg.TwitchClientID, YouTubeViewers: s.cfg.YouTubeViewers,
		InfoPresets: presets, ReplyTo: s.pins.ToConfig(),
	}
	if err := saved.Write(s.path); err != nil {
		s.opts.Warn("Couldn't save config.json: " + err.Error())
	}
}

// toAny turns cleaned presets into the []any Saved expects.
func toAny(p []info.Preset) []any {
	out := make([]any, len(p))
	for i := range p {
		out[i] = p[i]
	}
	return out
}
