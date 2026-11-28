package jsjson

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// JavaScript's conversions, for the values Parse returns: nil, bool,
// float64, string, []any and *Object.

// Trim is String#trim: JavaScript's white space and line terminators.
func Trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0xfeff, 0x2028, 0x2029:
			return true
		}
		return unicode.Is(unicode.Zs, r)
	})
}

// Truthy is JavaScript truthiness.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case int:
		return t != 0
	case string:
		return t != ""
	}
	return true
}

// Get is o[k] for an object, and undefined (false) for anything else.
func Get(v any, k string) (any, bool) {
	if o, ok := v.(*Object); ok && o != nil {
		return o.Get(k)
	}
	return nil, false
}

var decimal = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// ToNumber is Number(v). Pass present false for undefined (NaN).
func ToNumber(v any, present bool) float64 {
	if !present {
		return math.NaN()
	}
	switch t := v.(type) {
	case nil:
		return 0
	case bool:
		if t {
			return 1
		}
		return 0
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		return stringToNumber(t)
	case []any:
		// [] is 0, [x] is Number(String(x)), anything longer is NaN
		switch len(t) {
		case 0:
			return 0
		case 1:
			return stringToNumber(ToString(t[0]))
		}
	}
	return math.NaN()
}

func stringToNumber(s string) float64 {
	s = Trim(s)
	switch s {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		if base != 0 {
			f := 0.0
			for _, c := range s[2:] {
				d, err := strconv.ParseUint(string(c), base, 8)
				if err != nil {
					return math.NaN()
				}
				f = f*float64(base) + float64(d)
			}
			return f
		}
	}
	if !decimal.MatchString(s) {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(s, 64) // out of range is ±Inf, as in JavaScript
	return f
}

// ToString is String(v).
func ToString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return Number(t)
	case int:
		return strconv.Itoa(t)
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			if e != nil {
				parts[i] = ToString(e)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}
