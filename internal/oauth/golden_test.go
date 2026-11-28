package oauth

// testdata/session: a sign-in on disk being refreshed, step by step, as
// multichat's own TokenSession did it (scripts/golden/session.js). Each step
// checks the refresh request sent, what Tokens returned, and the exact bytes
// and mode of secrets.json afterwards.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/golden"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
)

type sessionCase struct {
	Now   int64
	File  *string
	Steps []struct {
		Section string
		Params  [][2]string
		Keep    []string
		Force   bool
		Status  int
		Answer  string
	}
}

type sessionGolden struct {
	Steps []struct {
		Request *string
		Result  string
		File    *string
	}
}

func TestSessionGoldens(t *testing.T) {
	cases, _ := filepath.Glob(golden.Path("session", "cases", "*.json"))
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		name := filepath.Base(c)
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			var spec sessionCase
			var want sessionGolden
			golden.Load(t, &spec, "session", "cases", name)
			golden.Load(t, &want, "session", "golden", name)
			if len(want.Steps) != len(spec.Steps) {
				t.Fatalf("%d golden steps for %d case steps", len(want.Steps), len(spec.Steps))
			}

			var mu sync.Mutex
			var status int
			var answer string
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, string(b))
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, answer)
			}))
			defer srv.Close()

			file := filepath.Join(t.TempDir(), "secrets.json")
			if spec.File != nil {
				if err := os.WriteFile(file, []byte(*spec.File), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			store := secrets.New(file, secrets.Options{})
			for i, step := range spec.Steps {
				mu.Lock()
				status, answer, requests = step.Status, step.Answer, nil
				mu.Unlock()
				s := &Session{
					Store: store, Section: step.Section, ExpiredMessage: "expired",
					Now:  func() time.Time { return time.UnixMilli(spec.Now) },
					Keep: keeper(step.Keep),
					RefreshRequest: func(tok *jsjson.Object) (string, []Param) {
						var ps []Param
						for _, p := range step.Params {
							v := p[1]
							if strings.HasPrefix(v, "$") {
								v = tok.String(v[1:])
							}
							ps = append(ps, Param{p[0], v})
						}
						return srv.URL + "/token", ps
					},
				}
				tok, err := s.Tokens(context.Background(), step.Force)
				var result string
				switch {
				case errors.Is(err, ErrSignedOut):
					result = "SignedOut"
				case err != nil:
					result = "Error: " + err.Error()
				default:
					result = string(jsjson.Stringify(tok, ""))
				}
				w := want.Steps[i]
				mu.Lock()
				got := requests
				mu.Unlock()
				switch {
				case w.Request == nil && len(got) != 0:
					t.Errorf("step %d: sent %q, Node sent nothing", i, got)
				case w.Request != nil && strings.Join(got, "\n") != *w.Request:
					t.Errorf("step %d: sent %q, Node sent %q", i, got, *w.Request)
				}
				if result != w.Result {
					t.Errorf("step %d: got %s\nNode got %s", i, result, w.Result)
				}
				b, err := os.ReadFile(file)
				switch {
				case w.File == nil && err == nil:
					t.Errorf("step %d: file exists, Node's doesn't", i)
				case w.File != nil && string(b) != *w.File:
					t.Errorf("step %d: file\n%s\nNode's\n%s", i, b, *w.File)
				case w.File != nil:
					if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
						t.Errorf("step %d: mode %v", i, fi.Mode().Perm())
					}
				}
			}
		})
	}
}

// keeper is the case's keep: the listed fields that are set, or nil when
// none are (session.js's keeper).
func keeper(fields []string) func(*jsjson.Object) *jsjson.Object {
	return func(tok *jsjson.Object) *jsjson.Object {
		o := jsjson.NewObject()
		for _, f := range fields {
			if v, ok := tok.Get(f); ok && jsjson.Truthy(v) {
				o.Set(f, v)
			}
		}
		if o.Len() == 0 {
			return nil
		}
		return o
	}
}
