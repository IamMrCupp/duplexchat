package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/golden"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

// Every case in testdata/secrets/cases leaves the same bytes on disk, and
// get() returns the same sections, as multichat's SecretStore
// (scripts/golden/secrets.js).
func TestGoldenFiles(t *testing.T) {
	cases, _ := filepath.Glob(golden.Path("secrets", "cases", "*.json"))
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		name := filepath.Base(c)
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(c)
			if err != nil {
				t.Fatal(err)
			}
			v, err := jsjson.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			spec := v.(*jsjson.Object)
			var want struct {
				File *string
				Gets map[string]string
			}
			gb, err := os.ReadFile(golden.Path("secrets", "golden", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(gb, &want); err != nil {
				t.Fatal(err)
			}

			f := filepath.Join(t.TempDir(), "secrets.json")
			if start, ok := spec.Get("file"); ok && start != nil {
				os.WriteFile(f, []byte(start.(string)), 0o600)
			}
			s := New(f, Options{})
			steps, _ := spec.Get("steps")
			for _, st := range steps.([]any) {
				pair := st.([]any)
				value, _ := pair[1].(*jsjson.Object)
				if err := s.Set(pair[0].(string), value); err != nil {
					t.Fatal(err)
				}
			}

			got, err := os.ReadFile(f)
			switch {
			case want.File == nil:
				if !os.IsNotExist(err) {
					t.Fatalf("file written: %q", got)
				}
			case err != nil:
				t.Fatal(err)
			case string(got) != *want.File:
				t.Errorf("file\n got: %q\nwant: %q", got, *want.File)
			}
			gets, _ := spec.Get("gets")
			for _, g := range gets.([]any) {
				sec := g.(string)
				var got string
				if o := s.Get(sec); o != nil {
					got = string(jsjson.Stringify(o, ""))
				} else {
					got = "null"
				}
				if got != want.Gets[sec] {
					t.Errorf("get(%q)\n got: %s\nwant: %s", sec, got, want.Gets[sec])
				}
			}
		})
	}
}
