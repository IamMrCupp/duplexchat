package main

// The pages are carried over unchanged, so the server has to answer every
// URL they use. This reads every embedded page, finds each local URL it
// references, and checks the server accounts for it: served now, behind the
// gate, or listed below as landing with a later issue. A page that starts
// using a route nobody serves fails here, not on stream.

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var localURL = regexp.MustCompile("['\"`](/(?:api|fonts|events|overlay|dock|setup|info|viewer-bar|dock-logic|info-logic|overlay-options|favicon)[^'\"`?#$ ]*)")

// notYet are routes the pages call that arrive with later issues. When one
// lands, move it out of here and the test checks it's really served.
var notYet = map[string]string{
	"/api/send":                  "#10",
	"/api/setup/":                "#13",
	"/api/setup/state":           "#13",
	"/api/setup/twitch/signin":   "#11",
	"/api/setup/twitch/signout":  "#11",
	"/api/setup/twitch/cancel":   "#11",
	"/api/setup/youtube/signin":  "#12",
	"/api/setup/youtube/signout": "#12",
	"/api/setup/youtube/client":  "#12",
	"/api/setup/youtube/viewers": "#14",
	"/api/info/state":            "#17",
	"/api/info/current":          "#17",
	"/api/info/categories":       "#17",
	"/api/info/save":             "#17",
	"/api/info/presets":          "#17",
	"/api/info/presets/delete":   "#17",
}

// gated are the panel's writes: without the session headers they're a 403,
// which proves the route exists.
var gated = map[string]string{
	"/api/sources":     "POST",
	"/api/sources/":    "DELETE", // + <id>, and <id>/reconnect
	"/api/test":        "POST",
	"/api/clear":       "POST",
	"/api/dock/target": "POST",
}

func referenced(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := fs.WalkDir(pages(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(pages(), path)
		if err != nil {
			return err
		}
		for _, m := range localURL.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = append(out[m[1]], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPagesOnlyUseKnownRoutes(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	refs := referenced(t)
	var paths []string
	for p := range refs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) < 20 {
		t.Fatalf("found only %d referenced URLs — is the pattern still right?", len(paths))
	}
	for _, p := range paths {
		switch {
		case notYet[p] != "":
			continue
		case gated[p] != "":
			path := p
			if strings.HasSuffix(p, "/") {
				path += "abcd1234"
			}
			if code, body := a.do(t, gated[p], path, map[string]string{}, ""); code != 403 {
				t.Errorf("%s %s (used by %v): %d %s, want 403 from the gate", gated[p], path, refs[p], code, body)
			}
		case p == "/events":
			continue // a stream; TestViewersAndOverlayFiles reads it
		default:
			path := p
			if p == "/fonts/" {
				continue // a prefix; TestFonts covers names
			}
			if code, _, _ := a.get(t, path, ""); code != 200 && code != 204 {
				t.Errorf("GET %s (used by %v): %d", path, refs[p], code)
			}
		}
	}
	// notYet must not list routes the pages no longer use
	for p, issue := range notYet {
		if _, ok := refs[p]; !ok {
			t.Errorf("notYet lists %s (%s), which no page uses any more", p, issue)
		}
	}
}

// Every overlay setting multichat supports still gets the overlay page, so
// existing OBS browser-source URLs keep working (#15).
func TestOverlaySettingsURLs(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{}})
	for _, q := range []string{"limit=40", "fade=20", "ts=1", "dir=top", "platform=0", "badges=0", "size=22", "font=Inter", "origcolors=1", "bg=0", "bg=100",
		"viewers=1", "viewerstyle=inside", "viewersoffline=1", "viewersfmt=short", "size=20&dir=top&fade=30"} {
		code, _, body := a.get(t, "/overlay?"+q, "")
		if code != 200 || !strings.Contains(body, `id="chat"`) {
			t.Errorf("/overlay?%s: %d", q, code)
		}
	}
	_, _, body := a.get(t, "/overlay", "")
	for _, js := range []string{"/overlay-options.js", "/viewer-bar.js", "/overlay.css"} {
		if !strings.Contains(body, js) {
			t.Errorf("overlay page no longer loads %s", js)
		}
	}
	if !strings.Contains(fmt.Sprint(referenced(t)["/events"]), "overlay.html") {
		t.Error("overlay page no longer listens to /events")
	}
}
