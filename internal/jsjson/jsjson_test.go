package jsjson

import (
	"math"
	"testing"
)

// Number#toString, as Node prints these.
func TestNumber(t *testing.T) {
	for f, want := range map[float64]string{
		0: "0", math.Copysign(0, -1): "0", 100: "100", -42: "-42", 1.5: "1.5",
		1e20: "100000000000000000000", 1e21: "1e+21", 1e-6: "0.000001", 1e-7: "1e-7",
		0.30000000000000004: "0.30000000000000004", 5e-324: "5e-324", 1764000000000: "1764000000000",
		math.MaxFloat64: "1.7976931348623157e+308", 123456789012345678901234: "1.2345678901234569e+23",
		math.Inf(1): "null", math.NaN(): "null", -1.25e-9: "-1.25e-9",
	} {
		if got := Number(f); got != want {
			t.Errorf("Number(%v) = %q, want %q", f, got, want)
		}
	}
}

// Array-index keys go first, ascending; a repeated key keeps its first
// place and its last value.
func TestKeyOrder(t *testing.T) {
	v, err := Parse([]byte(`{"b":1,"2":0,"a":0,"10":0,"01":0,"b":2,"4294967295":0,"1":0}`))
	if err != nil {
		t.Fatal(err)
	}
	got := string(Stringify(v, ""))
	want := `{"1":0,"2":0,"10":0,"b":2,"a":0,"01":0,"4294967295":0}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{"", "{", "{} x", "nope", "\ufeff{}"} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
}
