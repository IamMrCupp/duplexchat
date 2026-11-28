package main

// Source case 11 (test/server.test.js at 29a9d2c): the stream info page is
// served, its API is gated, and presets land in config.json; nothing else
// does.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestStreamInfoPageAPIAndPresetsFromTheBinary(t *testing.T) {
	a := start(t, map[string]any{"port": freePort(t), "sources": []any{},
		"infoPresets": []any{map[string]any{"name": "From before", "title": "Kept across a restart", "tags": []any{"a"}}, map[string]any{"junk": true}}})
	if code, _, page := a.get(t, "/info", ""); code != 200 || !strings.Contains(page, `id="form"`) || !strings.Contains(page, "/info-logic.js") {
		t.Fatalf("/info: %d", code)
	}
	if _, _, js := a.get(t, "/info-logic.js", ""); !strings.Contains(js, "InfoLogic") {
		t.Fatal("/info-logic.js")
	}
	if !regexp.MustCompile(`Stream info:\s+http://127\.0\.0\.1:\d+/info`).MatchString(a.out.String()) {
		t.Fatalf("banner:\n%s", a.out.String())
	}

	origin := fmt.Sprintf("http://127.0.0.1:%d", a.port)
	code := func(status int, body string) any {
		var j map[string]any
		json.Unmarshal([]byte(body), &j)
		return j["code"]
	}
	// no token: nothing, not even the state
	if c := code(a.do(t, "GET", "/api/info/state", nil, "")); c != "no_token" {
		t.Fatalf("state without a token: %v", c)
	}
	if c := code(a.do(t, "POST", "/api/info/save", map[string]string{"origin": origin}, `{"platforms":["twitch"],"title":"x"}`)); c != "no_token" {
		t.Fatalf("save without a token: %v", c)
	}
	if c := code(a.do(t, "GET", "/api/info/state", map[string]string{"host": fmt.Sprintf("some-lan-name:%d", a.port), "x-dock-token": "x"}, "")); c != "bad_host" {
		t.Fatalf("state from a LAN name: %v", c)
	}

	token := a.token(t)
	headers := map[string]string{"origin": origin, "x-dock-token": token}
	status, body := a.do(t, "GET", "/api/info/state", map[string]string{"x-dock-token": token}, "")
	var st struct {
		Platforms map[string]struct {
			Available, Ready bool
			Detail           string
		}
		Presets []any
	}
	if err := json.Unmarshal([]byte(body), &st); status != 200 || err != nil {
		t.Fatalf("state: %d %s", status, body)
	}
	tw, yt := st.Platforms["twitch"], st.Platforms["youtube"]
	if !tw.Available || tw.Ready || !yt.Available || yt.Ready || !strings.Contains(yt.Detail, "off until a Google client is set up") {
		t.Fatalf("state: %s", body)
	}
	want := []any{map[string]any{"name": "From before", "title": "Kept across a restart", "tags": []any{"a"}, "category": map[string]any{}}}
	if !reflect.DeepEqual(st.Presets, want) {
		t.Fatalf("presets: %v", st.Presets)
	}

	// not signed in: an honest per-platform answer, no crash
	_, body = a.do(t, "POST", "/api/info/save", headers, `{"platforms":["twitch","youtube"],"title":"x","tags":[]}`)
	var save struct {
		Results map[string]struct{ Code string }
	}
	json.Unmarshal([]byte(body), &save)
	if save.Results["twitch"].Code != "not_signed_in" || save.Results["youtube"].Code != "not_signed_in" {
		t.Fatalf("save: %s", body)
	}

	if status, body := a.do(t, "POST", "/api/info/presets", headers,
		`{"name":"Repair night","title":"Fixing an example board","tags":["repair"],"category":{"twitch":{"id":"509670","name":"Example Category"}}}`); status != 200 {
		t.Fatalf("keep: %d %s", status, body)
	}
	b, err := os.ReadFile(filepath.Join(a.dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		InfoPresets []struct {
			Name     string
			Category any
		}
	}
	json.Unmarshal(b, &cfg)
	if len(cfg.InfoPresets) != 2 || cfg.InfoPresets[0].Name != "From before" || cfg.InfoPresets[1].Name != "Repair night" {
		t.Fatalf("config.json: %s", b)
	}
	if !reflect.DeepEqual(cfg.InfoPresets[1].Category, map[string]any{"twitch": map[string]any{"id": "509670", "name": "Example Category"}}) {
		t.Fatalf("category: %v", cfg.InfoPresets[1].Category)
	}
	if _, err := os.Stat(filepath.Join(a.dir, "secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("secrets.json: %v", err)
	}
}
