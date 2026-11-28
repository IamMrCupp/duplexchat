package oauth

import (
	"fmt"
	"testing"
)

// Expected values are what new URLSearchParams(raw) gives in Node.
func TestParseQuery(t *testing.T) {
	for raw, want := range map[string]string{
		"state=a;b&code=c":  "[{state a;b} {code c}]",
		"a=1+2&a=3":         "[{a 1 2} {a 3}]",
		"x=%zz%4&y=%41%2b":  "[{x %zz%4} {y A+}]",
		"&&=v&k":            "[{ v} {k }]",
		"e=%E2%82&f=%C3%A9": "[{e \uFFFD} {f é}]",
		"g=%ED%A0%80":       "[{g \uFFFD\uFFFD\uFFFD}]",
		"h=%F0%9F%98%80%FF": "[{h 😀\uFFFD}]",
		"k=a=b":             "[{k a=b}]",
	} {
		if got := fmt.Sprint(ParseQuery(raw)); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	q := ParseQuery("state=&code=x")
	if !q.Has("state") || q.Get("state") != "" || q.Has("error") || q.Get("nope") != "" {
		t.Errorf("Has/Get: %v", q)
	}
}

func TestEncodeURIComponent(t *testing.T) {
	if got := EncodeURIComponent("a b/c?d&é!~*'()-_."); got != "a%20b%2Fc%3Fd%26%C3%A9!~*'()-_." {
		t.Errorf("%s", got)
	}
}
