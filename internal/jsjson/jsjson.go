// Package jsjson is JSON.parse and JSON.stringify as Node runs them, for
// files both versions write: the same input gives the same bytes. Objects
// keep JavaScript's property order (array-index keys first, ascending, then
// the rest in insertion order), a repeated key keeps its first position and
// its last value, and numbers print as Number#toString does.
//
// Values are nil, bool, float64, string, []any and *Object. One known gap: a
// lone UTF-16 surrogate escape ("\ud800") reads as U+FFFD, where JavaScript
// keeps it.
package jsjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Object is a JSON object in JavaScript property order.
type Object struct {
	keys []string
	m    map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{m: map[string]any{}} }

// Keys are the property names in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Len is the number of properties.
func (o *Object) Len() int { return len(o.keys) }

// Get returns a property and whether it's there.
func (o *Object) Get(k string) (any, bool) {
	v, ok := o.m[k]
	return v, ok
}

// Has reports whether k is a property.
func (o *Object) Has(k string) bool {
	_, ok := o.m[k]
	return ok
}

// Set adds or replaces a property. A new key goes where JavaScript puts it;
// an existing one stays where it is.
func (o *Object) Set(k string, v any) *Object {
	if _, ok := o.m[k]; !ok {
		o.keys = insert(o.keys, k)
	}
	o.m[k] = v
	return o
}

// Delete removes a property.
func (o *Object) Delete(k string) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Copy is a shallow copy, as { ...o }.
func (o *Object) Copy() *Object {
	c := &Object{keys: o.Keys(), m: make(map[string]any, len(o.m))}
	for k, v := range o.m {
		c.m[k] = v
	}
	return c
}

// String returns a string property, or "".
func (o *Object) String(k string) string {
	s, _ := o.m[k].(string)
	return s
}

// Number returns a number property and whether it was one.
func (o *Object) Number(k string) (float64, bool) {
	f, ok := o.m[k].(float64)
	return f, ok
}

// index reports whether k is an array index: the canonical decimal form of
// an integer below 2^32 - 1. JavaScript orders those keys first.
func index(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	if err != nil || n >= math.MaxUint32 {
		return 0, false
	}
	return n, true
}

func insert(keys []string, k string) []string {
	n, ok := index(k)
	if !ok {
		return append(keys, k)
	}
	at := 0
	for at < len(keys) {
		m, isIndex := index(keys[at])
		if !isIndex || m > n {
			break
		}
		at++
	}
	keys = append(keys, "")
	copy(keys[at+1:], keys[at:])
	keys[at] = k
	return keys
}

// Parse is JSON.parse.
func Parse(b []byte) (any, error) {
	if !json.Valid(b) {
		return nil, errors.New("invalid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return value(dec)
}

func value(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := value(dec)
				if err != nil {
					return nil, err
				}
				o.Set(kt.(string), v)
			}
			_, err := dec.Token()
			return o, err
		}
		l := []any{}
		for dec.More() {
			v, err := value(dec)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		_, err := dec.Token()
		return l, err
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return f, nil // out of range is ±Infinity or 0, as in JavaScript
	}
	return tok, nil
}

// Stringify is JSON.stringify(v, null, indent); indent "" is compact.
// Unsupported Go types are written as null.
func Stringify(v any, indent string) []byte {
	var b bytes.Buffer
	write(&b, v, indent, "")
	return b.Bytes()
}

func write(b *bytes.Buffer, v any, indent, at string) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case float64:
		b.WriteString(Number(t))
	case int:
		b.WriteString(Number(float64(t)))
	case int64:
		b.WriteString(Number(float64(t)))
	case string:
		Quote(b, t)
	case []string:
		l := make([]any, len(t))
		for i, s := range t {
			l[i] = s
		}
		write(b, l, indent, at)
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		inner := at + indent
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(b, indent, inner)
			write(b, x, indent, inner)
		}
		newline(b, indent, at)
		b.WriteByte(']')
	case *Object:
		if t == nil {
			b.WriteString("null")
			return
		}
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		inner := at + indent
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(b, indent, inner)
			Quote(b, k)
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			write(b, t.m[k], indent, inner)
		}
		newline(b, indent, at)
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

func newline(b *bytes.Buffer, indent, at string) {
	if indent != "" {
		b.WriteByte('\n')
		b.WriteString(at)
	}
}

// Quote writes s as JSON.stringify does: only quote, backslash and control
// characters are escaped; everything else, U+2028 included, is literal.
func Quote(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0xf])
		default:
			b.WriteRune(r) // invalid UTF-8 becomes U+FFFD
		}
	}
	b.WriteByte('"')
}

// Number is Number#toString for a JSON value: NaN and ±Infinity are null.
func Number(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// shortest round-tripping digits and the decimal exponent n, so that
	// f = 0.digits × 10^n
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	n := x + 1
	k := len(digits)
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		s = digits[:1]
		if k > 1 {
			s += "." + digits[1:]
		}
		if n-1 >= 0 {
			s += "e+" + strconv.Itoa(n-1)
		} else {
			s += "e-" + strconv.Itoa(1-n)
		}
	}
	return sign + s
}
