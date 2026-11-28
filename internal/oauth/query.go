package oauth

import "strings"

// Query is URLSearchParams over a query string (WHATWG
// application/x-www-form-urlencoded parsing): split on &, the first =
// separates name from value, + is a space, a % not followed by two hex
// digits stays as it is, and bytes that aren't UTF-8 become U+FFFD one
// maximal subpart at a time. Unlike url.ParseQuery, ; is an ordinary
// character.
type Query []Param

// ParseQuery parses a raw query string (without the ?).
func ParseQuery(raw string) Query {
	var q Query
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		q = append(q, Param{Key: formDecode(name), Value: formDecode(value)})
	}
	return q
}

// Has is URLSearchParams#has.
func (q Query) Has(k string) bool {
	_, ok := q.Lookup(k)
	return ok
}

// Get is URLSearchParams#get: the first value, "" when there's none.
func (q Query) Get(k string) string {
	v, _ := q.Lookup(k)
	return v
}

// Lookup is the first value and whether there is one.
func (q Query) Lookup(k string) (string, bool) {
	for _, p := range q {
		if p.Key == k {
			return p.Value, true
		}
	}
	return "", false
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func formDecode(s string) string {
	b := []byte(strings.ReplaceAll(s, "+", " "))
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '%' && i+2 < len(b) {
			hi, ok1 := unhex(b[i+1])
			lo, ok2 := unhex(b[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, b[i])
	}
	return decodeUTF8(out)
}

// decodeUTF8 is the WHATWG UTF-8 decoder: each maximal subpart of an
// ill-formed sequence becomes one U+FFFD.
func decodeUTF8(b []byte) string {
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c == 0xE0:
			need, lo = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			need = 2
		case c == 0xED:
			need, hi = 2, 0x9F
		case c == 0xF0:
			need, lo = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			need = 3
		case c == 0xF4:
			need, hi = 3, 0x8F
		default:
			out.WriteString("\uFFFD")
			i++
			continue
		}
		j := i + 1
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lo || b[j] > hi {
				break
			}
			lo, hi = 0x80, 0xBF
			j++
		}
		if j-i == need+1 {
			out.Write(b[i:j])
		} else {
			out.WriteString("\uFFFD")
		}
		i = j
	}
	return out.String()
}
