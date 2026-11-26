// Package server is duplexchat's HTTP server. For now it serves the
// carried-over pages; the SSE feed, config and APIs land with #4.
package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// routes is the source's static route table (multichat server.js at 29a9d2c).
// Paths match exactly: no trailing-slash variants, no direct file names.
var routes = map[string]string{
	"/":                   "panel.html",
	"/dock":               "dock.html",
	"/setup":              "setup.html",
	"/info":               "info.html",
	"/overlay":            "overlay.html",
	"/overlay.css":        "overlay.css",
	"/info-logic.js":      "info-logic.js",
	"/dock-logic.js":      "dock-logic.js",
	"/viewer-bar.js":      "viewer-bar.js",
	"/overlay-options.js": "overlay-options.js",
	"/favicon.svg":        "favicon.svg",
}

// mimeTypes matches the source's MIME table. The font types are for the
// user's fonts/ folder, served with #4.
var mimeTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

func contentType(name string) string {
	if t, ok := mimeTypes[path.Ext(name)]; ok {
		return t
	}
	return "application/octet-stream"
}

// Options are what the server serves from.
type Options struct {
	Pages    fs.FS             // public/, usually embedded
	Events   http.Handler      // GET /events (the SSE hub); nil → 404
	State    func() State      // GET /api/state; nil → 404
	FontsDir string            // the user's fonts/ folder; "" → no fonts
	Log      func(string, any) // error.log; nil → ignored

	Port      int              // the port actually bound; Host and Origin must name it
	Token     string           // the per-process session token
	Platforms func() Platforms // the session's platforms; nil → NoSending
	Peer      *string          // test seam for the gate: the peer address
	Panel     Panel            // the panel's buttons; nil → their routes 404
}

// Server answers HTTP requests.
type Server struct {
	opts Options
}

// New returns a server.
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = func(string, any) {}
	}
	return &Server{opts: opts}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rw := &recorder{ResponseWriter: w}
	defer s.recover(rw)
	if r.Method == http.MethodGet && r.URL.Path == "/api/dock/session" {
		s.session(rw, r)
		return
	}
	if r.Method == http.MethodGet && s.get(rw, r) {
		return
	}
	if s.opts.Panel != nil && s.panel(rw, r) {
		return
	}
	notFound(rw)
}

// get answers the GET routes; false means none matched.
func (s *Server) get(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	switch {
	case p == "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	case p == "/events" && s.opts.Events != nil:
		s.opts.Events.ServeHTTP(w, r)
	case p == "/api/state" && s.opts.State != nil:
		writeJSON(w, http.StatusOK, s.opts.State())
	case p == "/api/fonts":
		writeJSON(w, http.StatusOK, s.fontList())
	case strings.HasPrefix(p, "/fonts/"):
		s.serveFont(w, r)
	default:
		name, ok := routes[p]
		if !ok {
			return false
		}
		s.serveFile(w, name)
	}
	return true
}

func (s *Server) serveFile(w http.ResponseWriter, name string) {
	data, err := fs.ReadFile(s.opts.Pages, name)
	if err != nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("not found"))
}
