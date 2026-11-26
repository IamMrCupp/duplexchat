package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	if c := Load(filepath.Join(t.TempDir(), "missing.json")); c.Host != "127.0.0.1" || c.Port != float64(3131) || len(c.Sources) != 0 {
		t.Errorf("missing file: %+v", c)
	}
	for _, s := range []string{`{"port":`, `[1,2]`, `"text"`, `null`, ``} {
		if c := Load(write(t, s)); c.Host != "127.0.0.1" {
			t.Errorf("%q: not defaults: %+v", s, c)
		}
	}
	c := Load(write(t, "\ufeff{\"host\":null,\"port\":\"4000\",\"sources\":{\"url\":\"x\"},\"twitchClientId\":\"id\"}"))
	if c.Host != nil || c.Port != "4000" || len(c.Sources) != 0 || c.TwitchClientID != "id" {
		t.Errorf("BOM file: %+v", c)
	}
}

func TestEncodeMatchesJSONStringify(t *testing.T) {
	got, err := Encode(Object{{"a", "x & <y>"}, {"b", []any{}}, {"c", Object{}}, {"d", []SourceRef{{"1", "u"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": \"x & <y>\",\n  \"b\": [],\n  \"c\": {},\n  \"d\": [\n    {\n      \"id\": \"1\",\n      \"url\": \"u\"\n    }\n  ]\n}"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWriteKeepsMode(t *testing.T) {
	p := write(t, "{}")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Saved{Host: "h", Port: float64(1)}).Write(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".config.json.*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestTruthy(t *testing.T) {
	yes := []any{"x", float64(1), float64(-1), true, map[string]any{}, []any{}}
	no := []any{nil, "", float64(0), false}
	for _, v := range yes {
		if !Truthy(v) {
			t.Errorf("Truthy(%#v) = false", v)
		}
	}
	for _, v := range no {
		if Truthy(v) {
			t.Errorf("Truthy(%#v) = true", v)
		}
	}
}

func TestAppDir(t *testing.T) {
	exe := func() (string, error) { return "/opt/duplexchat/duplexchat", nil }
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if d, _ := AppDir(getenv, exe); d != "/opt/duplexchat" {
		t.Errorf("default = %q", d)
	}
	env["MULTICHAT_DIR"] = "/old"
	if d, _ := AppDir(getenv, exe); d != "/old" {
		t.Errorf("MULTICHAT_DIR = %q", d)
	}
	env["DUPLEXCHAT_DIR"] = "/new"
	if d, _ := AppDir(getenv, exe); d != "/new" {
		t.Errorf("DUPLEXCHAT_DIR first = %q", d)
	}
	if _, err := AppDir(func(string) string { return "" }, func() (string, error) { return "", errors.New("no") }); err == nil {
		t.Error("want the executable lookup's error")
	}
}
