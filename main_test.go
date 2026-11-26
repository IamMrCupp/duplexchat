package main

import (
	"io/fs"
	"testing"
)

// Every page the server routes to must be in the embedded tree, so a file
// dropped from public/ fails here rather than as a 404 on stream.
func TestEmbeddedPages(t *testing.T) {
	want := []string{
		"panel.html", "dock.html", "setup.html", "info.html", "overlay.html",
		"overlay.css", "info-logic.js", "dock-logic.js", "viewer-bar.js",
		"overlay-options.js", "favicon.svg",
	}
	p := pages()
	for _, name := range want {
		if _, err := fs.Stat(p, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
