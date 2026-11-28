package jsjson

import (
	"math"
	"testing"
)

func TestToNumber(t *testing.T) {
	nan := math.NaN()
	for _, c := range []struct {
		in   any
		want float64
	}{
		{nil, 0}, {true, 1}, {false, 0}, {42.5, 42.5}, {"", 0}, {"  \n", 0}, {" 12 ", 12},
		{"1e3", 1000}, {".5", 0.5}, {"5.", 5}, {"-0x10", nan}, {"0x10", 16}, {"0b101", 5}, {"0o17", 15},
		{"0x", nan}, {"0xg", nan}, {"Infinity", math.Inf(1)}, {"-Infinity", math.Inf(-1)}, {"inf", nan},
		{"1_000", nan}, {"12px", nan}, {"1e999", math.Inf(1)}, {"\ufeff7\u00a0", 7}, {"+3", 3}, {"1e", nan},
		{[]any{}, 0}, {[]any{"7"}, 7}, {[]any{1.0, 2.0}, nan}, {NewObject(), nan},
	} {
		got := ToNumber(c.in, true)
		if got != c.want && !(math.IsNaN(got) && math.IsNaN(c.want)) {
			t.Errorf("Number(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
	if !math.IsNaN(ToNumber(nil, false)) {
		t.Error("Number(undefined) isn't NaN")
	}
}

func TestToString(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, "null"}, {true, "true"}, {1e21, "1e+21"}, {0.1, "0.1"}, {"x", "x"},
		{[]any{"a", nil, 2.0, []any{"b", "c"}}, "a,,2,b,c"}, {NewObject(), "[object Object]"},
	} {
		if got := ToString(c.in); got != c.want {
			t.Errorf("String(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruthyAndTrim(t *testing.T) {
	for v, want := range map[any]bool{nil: false, "": false, "0": true, 0.0: false, false: false} {
		if Truthy(v) != want {
			t.Errorf("Truthy(%#v) != %v", v, want)
		}
	}
	if !Truthy(math.NaN()) == false || Truthy(math.NaN()) {
		t.Error("NaN is truthy")
	}
	if got := Trim("\ufeff\u2003 x \u0085"); got != "x \u0085" {
		t.Errorf("Trim = %q", got)
	}
}
