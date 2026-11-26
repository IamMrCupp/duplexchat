package info

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestClip(t *testing.T) {
	cases := []struct {
		in   any
		n    int
		want string
	}{
		{"  a \t\n b  ", 10, "a b"},
		{"a\u00a0\u2003\u3000b\ufeff", 10, "a b"},
		{"a\u0085b", 10, "a\u0085b"}, // NEL isn't JavaScript whitespace
		{"😀😀😀", 2, "😀😀"},             // code points, not bytes
		{42, 10, ""},
		{nil, 10, ""},
		{"", 10, ""},
	}
	for _, c := range cases {
		if got := clip(c.in, c.n); got != c.want {
			t.Errorf("clip(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestCleanTags(t *testing.T) {
	got := CleanTags(decode(t, `[" repair ", "Repair", "", 5, "solder", "SOLDER", "a  b"]`))
	if strings.Join(got, "|") != "repair|solder|a b" {
		t.Errorf("got %q", got)
	}
	var many []any
	for i := 0; i < 300; i++ {
		many = append(many, strings.Repeat("x", i%7+1)+string(rune('a'+i%26))+string(rune('A'+i/26)))
	}
	if n := len(CleanTags(many)); n != 50 {
		t.Errorf("kept %d tags, want 50", n)
	}
	if got := CleanTags("not a list"); len(got) != 0 {
		t.Errorf("non-list gave %q", got)
	}
}

func TestCleanCategory(t *testing.T) {
	ok := []string{`{"id":"509670","name":"Science & Technology"}`, `{"id":"a_b-C"}`, `{"id":"` + strings.Repeat("x", 32) + `"}`}
	bad := []string{`{"id":""}`, `{"id":"has space"}`, `{"id":509670}`, `{"id":"` + strings.Repeat("x", 33) + `"}`, `null`, `"509670"`, `[]`}
	for _, s := range ok {
		if CleanCategory(decode(t, s)) == nil {
			t.Errorf("%s refused", s)
		}
	}
	for _, s := range bad {
		if c := CleanCategory(decode(t, s)); c != nil {
			t.Errorf("%s kept as %+v", s, c)
		}
	}
}

func TestCleanPresets(t *testing.T) {
	got := CleanPresets(decode(t, `[
		{"name":"Repair night","title":"  Fixing   things ","tags":["a"],"category":{"twitch":{"id":"1","name":"T"},"youtube":{"id":"bad id"}}},
		{"name":"REPAIR NIGHT","title":"dupe"},
		{"name":"   "},
		{"title":"no name"},
		"junk", null, [],
		{"name":"`+strings.Repeat("n", 50)+`"}
	]`))
	b, _ := json.Marshal(got)
	want := `[{"name":"Repair night","title":"Fixing things","tags":["a"],"category":{"twitch":{"id":"1","name":"T"}}},` +
		`{"name":"` + strings.Repeat("n", 40) + `","title":"","tags":[],"category":{}}]`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
	var many []any
	for i := 0; i < 40; i++ {
		many = append(many, map[string]any{"name": string(rune('A' + i))})
	}
	if n := len(CleanPresets(many)); n != MaxPresets {
		t.Errorf("kept %d presets, want %d", n, MaxPresets)
	}
}
