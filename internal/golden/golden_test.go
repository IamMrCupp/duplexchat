package golden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every golden file is valid JSON.
func TestGoldensParse(t *testing.T) {
	files, err := filepath.Glob(Path("*", "golden", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	more, _ := filepath.Glob(Path("*", "golden", "*", "*.json"))
	files = append(files, more...)
	if len(files) == 0 {
		t.Fatal("no golden files found")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(b) {
			t.Errorf("%s: not valid JSON", f)
		}
	}
}

// Every input has its golden and every golden has its input, so a fixture
// added without regenerating (or a stale golden) fails here.
func TestInputsMatchGoldens(t *testing.T) {
	pairs := []struct{ in, ext, out string }{
		{"twitch/irc", ".irc", "twitch/golden/irc"},
		{"youtube/flows", ".json", "youtube/golden/flows"},
		{"youtube/actions", ".json", "youtube/golden/actions"},
		{"config/cases", ".json", "config/golden"},
	}
	for _, p := range pairs {
		ins := names(t, p.in, p.ext)
		outs := names(t, p.out, ".json")
		for n := range ins {
			if !outs[n] {
				t.Errorf("%s/%s%s has no golden in %s", p.in, n, p.ext, p.out)
			}
		}
		for n := range outs {
			if !ins[n] {
				t.Errorf("%s/%s.json has no input in %s", p.out, n, p.in)
			}
		}
	}
}

func names(t *testing.T, dir, ext string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			out[strings.TrimSuffix(e.Name(), ext)] = true
		}
	}
	return out
}
