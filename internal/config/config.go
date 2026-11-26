// Package config reads and writes config.json exactly as multichat does, so
// an existing install can swap executables and keep its file (multichat
// server.js at 29a9d2c: the load block and saveConfig).
package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config is what was loaded. Values keep the JSON types the file had, since
// multichat writes back whatever it read (a string port stays a string).
type Config struct {
	Host           any
	Port           any
	Sources        []any // entries as found; startup takes those with a url
	TwitchClientID any
	YouTubeViewers any
	InfoPresets    any
	ReplyTo        any
}

// Defaults are multichat's: loopback, port 3131, no sources.
func Defaults() Config {
	return Config{Host: "127.0.0.1", Port: float64(3131), Sources: []any{}}
}

// Load reads path. A missing or unreadable file, or one that isn't a JSON
// object, gives the defaults — silently, as on multichat's first run. A
// leading UTF-8 byte-order mark is ignored (Notepad and PowerShell add one).
func Load(path string) Config {
	c := Defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	b = bytes.TrimPrefix(b, []byte("\ufeff"))
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
		return c
	}
	// {...defaults, ...raw}: keys present in the file win, even when null
	if v, ok := raw["host"]; ok {
		c.Host = v
	}
	if v, ok := raw["port"]; ok {
		c.Port = v
	}
	if v, ok := raw["sources"].([]any); ok {
		c.Sources = v
	}
	c.TwitchClientID = raw["twitchClientId"]
	c.YouTubeViewers = raw["youtubeViewers"]
	c.InfoPresets = raw["infoPresets"]
	c.ReplyTo = raw["replyTo"]
	return c
}

// SourceRef is a saved source: id and url, in that order.
type SourceRef struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Field is one key of an ordered JSON object.
type Field struct {
	Key   string
	Value any
}

// Object is a JSON object that keeps its key order when marshalled.
type Object []Field

// MarshalJSON writes the fields in order.
func (o Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := marshal(f.Key)
		if err != nil {
			return nil, err
		}
		v, err := marshal(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshal is json.Marshal without HTML escaping, as JSON.stringify writes.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Encode is JSON.stringify(v, null, 2): two-space indent, no trailing newline.
func Encode(v any) ([]byte, error) {
	compact, err := marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Saved is what saveConfig writes, built by the caller from live state:
// presets already cleaned and reply pins already resolved.
type Saved struct {
	Host           any
	Port           any
	Sources        []SourceRef
	TwitchClientID any
	YouTubeViewers any
	InfoPresets    any    // written when it's a non-empty list
	ReplyTo        Object // written when non-empty
}

// Object returns the file's contents in multichat's key order, with the
// optional keys under multichat's rules.
func (s Saved) Object() Object {
	sources := s.Sources
	if sources == nil {
		sources = []SourceRef{}
	}
	o := Object{{"host", s.Host}, {"port", s.Port}, {"sources", sources}}
	if Truthy(s.TwitchClientID) {
		o = append(o, Field{"twitchClientId", s.TwitchClientID})
	}
	if s.YouTubeViewers == "api" {
		o = append(o, Field{"youtubeViewers", "api"})
	}
	if l, ok := s.InfoPresets.([]any); ok && len(l) > 0 {
		o = append(o, Field{"infoPresets", s.InfoPresets})
	}
	if len(s.ReplyTo) > 0 {
		o = append(o, Field{"replyTo", s.ReplyTo})
	}
	return o
}

// Write saves the file at path through a temporary file and a rename, so a
// crash mid-write can't leave half a config. An existing file keeps its mode.
func (s Saved) Write(path string) error {
	b, err := Encode(s.Object())
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.json.*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Truthy is JavaScript truthiness for a decoded JSON value.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && x == x
	case string:
		return x != ""
	default:
		return true // objects and arrays, even empty ones
	}
}

// AppDir is where the files the app changes live: DUPLEXCHAT_DIR, then
// multichat's MULTICHAT_DIR, then the executable's own folder.
func AppDir(getenv func(string) string, executable func() (string, error)) (string, error) {
	for _, k := range []string{"DUPLEXCHAT_DIR", "MULTICHAT_DIR"} {
		if d := strings.TrimSpace(getenv(k)); d != "" {
			return d, nil
		}
	}
	exe, err := executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe), nil
}
