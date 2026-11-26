// Package server is duplexchat's HTTP server. For now it serves the
// carried-over pages; the SSE feed, config and APIs land with #4.
package server

import (
	"io/fs"
	"net/http"
	"path"
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

// Server answers HTTP requests. pages holds the contents of public/.
type Server struct {
	pages fs.FS
}

// New returns a server for the given pages, which are usually the embedded
// public/ directory.
func New(pages fs.FS) *Server {
	return &Server{pages: pages}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if name, ok := routes[r.URL.Path]; ok {
			s.serveFile(w, name)
			return
		}
	}
	notFound(w)
}

func (s *Server) serveFile(w http.ResponseWriter, name string) {
	data, err := fs.ReadFile(s.pages, name)
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
