package sources

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/config"
	"github.com/IamMrCupp/duplexchat/internal/golden"
)

// counterIDs matches the golden driver's crypto stub: 00000001, 00000002, …
func counterIDs() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("%08d", n) }
}

type goldenCase struct {
	File *string `json:"file"`
	Ops  []struct {
		Add    *string `json:"add"`
		Remove *int    `json:"remove"`
	} `json:"ops"`
}

type goldenOut struct {
	IDs   []string `json:"ids"`
	Steps []struct {
		Saved *string `json:"saved"`
	} `json:"steps"`
}

// Every config case: load what multichat would load, start the sources, apply
// the panel's adds and removes, and save exactly the bytes multichat saved.
func TestConfigGoldens(t *testing.T) {
	entries, err := os.ReadDir(golden.Path("config", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		t.Run(name, func(t *testing.T) {
			var in goldenCase
			var want goldenOut
			golden.Load(t, &in, "config", "cases", e.Name())
			golden.Load(t, &want, "config", "golden", e.Name())

			path := filepath.Join(t.TempDir(), "config.json")
			if in.File != nil {
				if err := os.WriteFile(path, []byte(*in.File), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s := NewStore(config.Load(path), path, Options{NewID: counterIDs()})
			s.StartSources()
			for i, op := range in.Ops {
				switch {
				case op.Add != nil:
					s.Add(*op.Add)
				case op.Remove != nil:
					list := s.List()
					s.Remove(list[*op.Remove].ID)
				}
				got, err := os.ReadFile(path)
				wantSaved := want.Steps[i].Saved
				switch {
				case wantSaved == nil && err == nil:
					t.Errorf("op %d: saved a file, multichat didn't", i)
				case wantSaved != nil && err != nil:
					t.Errorf("op %d: nothing saved: %v", i, err)
				case wantSaved != nil && !bytes.Equal(got, []byte(*wantSaved)):
					t.Errorf("op %d: saved\n%s\nwant\n%s", i, got, *wantSaved)
				}
			}
			var ids []string
			for _, en := range s.List() {
				ids = append(ids, en.ID)
			}
			if strings.Join(ids, ",") != strings.Join(want.IDs, ",") {
				t.Errorf("ids = %v, want %v", ids, want.IDs)
			}
		})
	}
}

func TestDetectPlatform(t *testing.T) {
	cases := map[string]string{
		"https://www.twitch.tv/x":         "twitch",
		"twitch.tv/x":                     "twitch",
		"HTTPS://M.TWITCH.TV/x":           "twitch",
		"https://youtube.com/@x":          "youtube",
		"https://www.youtube.com/watch?v": "youtube",
		"youtu.be/AAAAAAAAAAA":            "youtube",
		"https://music.youtube.com/x":     "youtube",
		"https://twitch.tv.evil.example/": "",
		"https://nottwitch.tv/x":          "",
		"https://youtu.be.evil/x":         "",
		"ftp://twitch.tv/x":               "",
		"not a stream":                    "",
		"":                                "",
	}
	for url, want := range cases {
		if got := DetectPlatform(url); got != want {
			t.Errorf("DetectPlatform(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestAddRefusesNonStreams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewStore(config.Load(path), path, Options{NewID: counterIDs()})
	if _, err := s.Add("https://example.com/x"); err == nil {
		t.Fatal("want an error for a non-stream link")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a refused add saved config.json")
	}
}
