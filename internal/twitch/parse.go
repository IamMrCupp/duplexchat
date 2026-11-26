// Package twitch reads Twitch chat anonymously over IRC on TLS (multichat
// lib/twitch.js at 29a9d2c): parsing, message building, moderation and the
// connection, ported line for line and checked against goldens generated
// from that file (testdata/twitch).
package twitch

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Line is one parsed IRC line.
type Line struct {
	Tags     map[string]string
	Prefix   *string
	Cmd      string
	Params   []string
	Trailing *string
}

// unescapeTag undoes IRCv3 tag escaping: \s → space, \: → ;, \r, \n, \;
// any other \X → X, and a lone trailing backslash is dropped.
func unescapeTag(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			b.WriteByte(v[i])
			continue
		}
		if i+1 >= len(v) {
			break
		}
		i++
		switch v[i] {
		case 's':
			b.WriteByte(' ')
		case ':':
			b.WriteByte(';')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// jsSlice is String.prototype.slice(from, to) with negative indices counted
// from the end (only -1 occurs here: indexOf found nothing).
func jsSlice(s string, from, to int) string {
	if to < 0 {
		to += len(s)
	}
	if from < 0 {
		from += len(s)
	}
	if from < 0 {
		from = 0
	}
	if to > len(s) {
		to = len(s)
	}
	if from >= to {
		return ""
	}
	return s[from:to]
}

// parseLine is multichat's parseLine, quirks included: a tag block or
// prefix with no space after it leaves the line as it was.
func parseLine(line string) Line {
	rest := line
	tags := map[string]string{}
	if strings.HasPrefix(rest, "@") {
		i := strings.Index(rest, " ")
		for _, kv := range strings.Split(jsSlice(rest, 1, i), ";") {
			if eq := strings.Index(kv, "="); eq == -1 {
				tags[kv] = ""
			} else {
				tags[kv[:eq]] = unescapeTag(kv[eq+1:])
			}
		}
		rest = rest[i+1:]
	}
	var prefix *string
	if strings.HasPrefix(rest, ":") {
		i := strings.Index(rest, " ")
		p := jsSlice(rest, 1, i)
		prefix = &p
		rest = rest[i+1:]
	}
	var trailing *string
	middle := rest
	if ti := strings.Index(rest, " :"); ti != -1 {
		t := rest[ti+2:]
		trailing = &t
		middle = rest[:ti]
	}
	var params []string
	for _, p := range strings.Split(middle, " ") {
		if p != "" {
			params = append(params, p)
		}
	}
	cmd := ""
	if len(params) > 0 {
		cmd, params = params[0], params[1:]
	}
	if params == nil {
		params = []string{}
	}
	return Line{Tags: tags, Prefix: prefix, Cmd: cmd, Params: params, Trailing: trailing}
}

// jsNumber is JavaScript's Number(string): trimmed, "" is 0, otherwise a
// decimal (or 0x/0o/0b) literal or NaN.
func jsNumber(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			if n, err := strconv.ParseUint(s[2:], base, 64); err == nil {
				return float64(n)
			}
			return math.NaN()
		}
	}
	if strings.ContainsAny(s, "_") || strings.HasPrefix(strings.TrimLeft(s, "+-"), "Inf") || strings.HasPrefix(strings.TrimLeft(s, "+-"), "inf") {
		if t := strings.TrimLeft(s, "+-"); t == "Infinity" {
			if strings.HasPrefix(s, "-") {
				return math.Inf(-1)
			}
			return math.Inf(1)
		}
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

type span struct {
	s, e float64
	url  string
}

// sliceIdx turns a JS slice index into a code-point offset (truncated).
func sliceIdx(f float64, n int) int {
	i := int(math.Trunc(f))
	if i < 0 {
		i = 0
	}
	if i > n {
		i = n
	}
	return i
}

// collectRanges reads the emotes and gifs tags. Offsets are Unicode code
// points, inclusive.
func collectRanges(tags map[string]string, n int) []span {
	var out []span
	if em := tags["emotes"]; em != "" {
		for _, grp := range strings.Split(em, "/") {
			c := strings.Index(grp, ":")
			if c == -1 {
				continue
			}
			id := grp[:c]
			for _, r := range strings.Split(grp[c+1:], ",") {
				se := strings.Split(r, "-")
				s := jsNumber(se[0])
				e := math.NaN()
				if len(se) > 1 {
					e = jsNumber(se[1])
				}
				if finite(s) && finite(e) && e >= s && s < float64(n) {
					out = append(out, span{s, math.Min(e, float64(n-1)), emoteCDN(id)})
				}
			}
		}
	}
	if gifs := tags["gifs"]; gifs != "" {
		for _, grp := range strings.Split(gifs, ",") {
			bits := strings.Split(grp, "|")
			if len(bits) < 3 {
				continue
			}
			se := strings.Split(bits[0], "-")
			s := jsNumber(se[0])
			e := math.NaN()
			if len(se) > 1 {
				e = jsNumber(se[1])
			}
			url := strings.Join(bits[2:], "|")
			if finite(s) && finite(e) && e >= s && s < float64(n) && strings.HasPrefix(url, "https://") {
				out = append(out, span{s, math.Min(e, float64(n-1)), url})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].s < out[j].s })
	return out
}

func emoteCDN(id string) string {
	return "https://static-cdn.jtvnw.net/emoticons/v2/" + id + "/default/dark/2.0"
}
