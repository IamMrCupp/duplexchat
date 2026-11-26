// Package info is the stream info panel. For now it holds preset cleaning,
// which config.json saving needs (multichat lib/infoapi.js at 29a9d2c).
package info

import "strings"

const (
	MaxPresets = 30
	maxTags    = 50
)

// Platforms in the order multichat writes them.
var Platforms = []string{"twitch", "youtube"}

// jsSpace is the set JavaScript's \s and String.prototype.trim treat as space.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029',
		'\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// clip: a string with whitespace runs collapsed to one space, trimmed, cut to
// n code points; anything that isn't a string is "".
func clip(v any, n int) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		if jsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := []rune(b.String())
	if len(out) > n {
		out = out[:n]
	}
	return string(out)
}

// CleanTags: trimmed, empties dropped, repeats dropped whatever their case,
// at most 50 kept from the first 200 given.
func CleanTags(list any) []string {
	out := []string{}
	l, _ := list.([]any)
	if len(l) > maxTags*4 {
		l = l[:maxTags*4]
	}
	seen := map[string]bool{}
	for _, raw := range l {
		tag := clip(raw, 100)
		key := strings.ToLower(tag)
		if tag == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
		if len(out) >= maxTags {
			break
		}
	}
	return out
}

// Category is a platform category: an ID that looks like one, and a name.
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func validID(id string) bool {
	if len(id) < 1 || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// CleanCategory returns nil unless c is an object with a plausible string ID.
func CleanCategory(c any) *Category {
	m, ok := c.(map[string]any)
	if !ok {
		return nil
	}
	id, ok := m["id"].(string)
	if !ok || !validID(id) {
		return nil
	}
	return &Category{ID: id, Name: clip(m["name"], 100)}
}

// Categories keeps multichat's key order: twitch, then youtube.
type Categories struct {
	Twitch  *Category `json:"twitch,omitempty"`
	YouTube *Category `json:"youtube,omitempty"`
}

// Preset is a named copy of the info form.
type Preset struct {
	Name     string     `json:"name"`
	Title    string     `json:"title"`
	Tags     []string   `json:"tags"`
	Category Categories `json:"category"`
}

// CleanPreset returns nil unless p is an object with a non-empty name.
func CleanPreset(p any) *Preset {
	m, ok := p.(map[string]any)
	if !ok {
		return nil
	}
	name := clip(m["name"], 40)
	if name == "" {
		return nil
	}
	out := &Preset{Name: name, Title: clip(m["title"], 200), Tags: CleanTags(m["tags"])}
	cats, _ := m["category"].(map[string]any)
	out.Category.Twitch = CleanCategory(cats["twitch"])
	out.Category.YouTube = CleanCategory(cats["youtube"])
	return out
}

// CleanPresets keeps valid presets, first of each name (any case), at most 30.
func CleanPresets(list any) []Preset {
	out := []Preset{}
	l, _ := list.([]any)
	for _, raw := range l {
		p := CleanPreset(raw)
		if p != nil && !hasName(out, p.Name) {
			out = append(out, *p)
		}
		if len(out) >= MaxPresets {
			break
		}
	}
	return out
}

func hasName(list []Preset, name string) bool {
	for _, p := range list {
		if strings.ToLower(p.Name) == strings.ToLower(name) {
			return true
		}
	}
	return false
}
