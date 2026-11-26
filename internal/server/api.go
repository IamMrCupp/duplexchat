package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// State is GET /api/state, in multichat's key order.
type State struct {
	Version string   `json:"version"`
	Host    any      `json:"host"`
	Port    int      `json:"port"`
	LAN     []string `json:"lan"`
	Sources any      `json:"sources"`
	Viewers any      `json:"viewers"`
}

// writeJSON is multichat server.js's json(): status, content type, body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

var fontRE = regexp.MustCompile(`(?i)\.(ttf|otf|woff2?)$`)

// fontList is the user's fonts, sorted; [] when there's no folder.
func (s *Server) fontList() []string {
	out := []string{}
	if s.opts.FontsDir == "" {
		return out
	}
	entries, err := os.ReadDir(s.opts.FontsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if fontRE.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// serveFont is GET /fonts/<name>: a font file from the user's folder, by
// name only — nothing with a path in it, nothing that isn't a font.
func (s *Server) serveFont(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/fonts/"))
	if err != nil || s.opts.FontsDir == "" || !fontRE.MatchString(name) ||
		strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		notFound(w)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.opts.FontsDir, name))
	if err != nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", contentType(strings.ToLower(name)))
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Write(data)
}

// recorder notes whether anything was written, so a panic can still answer
// 500 when nothing has gone out yet. It passes Flush through for the feed.
type recorder struct {
	http.ResponseWriter
	wrote bool
}

func (r *recorder) WriteHeader(code int) { r.wrote = true; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recover logs a handler panic to error.log and answers 500 if it can. The
// server keeps running (multichat: the router's catch).
func (s *Server) recover(w *recorder) {
	v := recover()
	if v == nil {
		return
	}
	if v == http.ErrAbortHandler {
		panic(v)
	}
	s.opts.Log("http", v)
	if !w.wrote {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprint(v)})
	}
}
