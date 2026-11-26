package youtube

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/IamMrCupp/duplexchat/internal/app"
)

var videoIDREs = []*regexp.Regexp{
	regexp.MustCompile(`[?&]v=([A-Za-z0-9_-]{11})`),
	regexp.MustCompile(`youtu\.be/([A-Za-z0-9_-]{11})`),
	regexp.MustCompile(`/live/([A-Za-z0-9_-]{11})`),
	regexp.MustCompile(`/shorts/([A-Za-z0-9_-]{11})`),
	regexp.MustCompile(`/embed/([A-Za-z0-9_-]{11})`),
	regexp.MustCompile(`/(?:v|e)/([A-Za-z0-9_-]{11})`),
}

// directVideoID finds a video ID in the link itself, or "".
func directVideoID(link string) string {
	for _, re := range videoIDREs {
		if m := re.FindStringSubmatch(link); m != nil {
			return m[1]
		}
	}
	return ""
}

// intColor turns an ARGB integer into #rrggbb; anything else is nil.
func intColor(v any) *string {
	n, ok := v.(float64)
	if !ok {
		return nil
	}
	s := fmt.Sprintf("#%06x", uint32(int64(n))&0xffffff)
	return &s
}

// thumbURL is the last thumbnail's URL, https only.
func thumbURL(v any) *string {
	t := list(get(v, "thumbnails"))
	if len(t) == 0 {
		return nil
	}
	u := str(get(t[len(t)-1], "url"))
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	if !strings.HasPrefix(u, "https:") {
		return nil
	}
	return &u
}

func first(v any) string {
	if l := list(v); len(l) > 0 {
		return str(l[0])
	}
	return ""
}

// runsToParts turns message runs into text and emote parts.
func runsToParts(runs any) []app.Part {
	parts := []app.Part{}
	push := func(v string) {
		if v == "" {
			return
		}
		if n := len(parts); n > 0 && parts[n-1].T == "text" {
			parts[n-1].V += v
			return
		}
		parts = append(parts, app.Part{T: "text", V: v})
	}
	for _, run := range list(runs) {
		o, _ := run.(*Obj)
		if o == nil {
			continue
		}
		if t, ok := o.M["text"]; ok && t != nil {
			push(fmt.Sprint(t))
			continue
		}
		e := get(o, "emoji")
		if e == nil {
			continue
		}
		if b, _ := get(e, "isCustomEmoji").(bool); b {
			if u := thumbURL(get(e, "image")); u != nil {
				alt := first(get(e, "shortcuts"))
				if alt == "" {
					alt = first(get(e, "searchTerms"))
				}
				if alt == "" {
					alt = "emoji"
				}
				parts = append(parts, app.Part{T: "emote", URL: *u, Alt: alt})
			} else {
				push(first(get(e, "shortcuts")))
			}
			continue
		}
		id := str(get(e, "emojiId"))
		if id == "" {
			id = first(get(e, "shortcuts"))
		}
		push(id)
	}
	return parts
}

func runsToText(runs any) string {
	var b strings.Builder
	for _, run := range list(runs) {
		if t := get(run, "text"); t != nil {
			b.WriteString(fmt.Sprint(t))
		} else if e := get(run, "emoji"); e != nil {
			b.WriteString(str(get(e, "emojiId")))
		}
	}
	return b.String()
}

func simpleOrRuns(node any) string {
	o, ok := node.(*Obj)
	if !ok {
		return ""
	}
	if t, ok := o.M["simpleText"]; ok && t != nil {
		return fmt.Sprint(t)
	}
	return runsToText(o.M["runs"])
}

// authorFrom builds the author: name, color (always null), roles, key,
// avatar, and badgeUrl only for members.
func authorFrom(r any) []app.Field {
	var key any
	if k := str(get(r, "authorExternalChannelId")); k != "" {
		key = k
	}
	roles := []string{}
	var badge *string
	member := false
	for _, b := range list(get(r, "authorBadges")) {
		br := get(b, "liveChatAuthorBadgeRenderer")
		if br == nil {
			continue
		}
		switch str(get(br, "icon", "iconType")) {
		case "MODERATOR":
			roles = append(roles, "mod")
		case "OWNER":
			roles = append(roles, "owner")
		case "VERIFIED":
			roles = append(roles, "verified")
		default:
			if ct := get(br, "customThumbnail"); ct != nil {
				roles = append(roles, "member")
				badge = thumbURL(ct)
				member = true
			}
		}
	}
	f := []app.Field{
		{Key: "name", Value: simpleOrRuns(get(r, "authorName"))},
		{Key: "color", Value: nil},
		{Key: "roles", Value: roles},
		{Key: "key", Value: key},
		{Key: "avatar", Value: thumbURL(get(r, "authorPhoto"))},
	}
	if member {
		f = append(f, app.Field{Key: "badgeUrl", Value: badge})
	}
	return f
}

// contData is the first continuation in key order that has a token.
func contData(conts any) (token string, timeoutMs *float64, ok bool) {
	for _, c := range list(conts) {
		o, _ := c.(*Obj)
		if o == nil {
			continue
		}
		for _, k := range o.Keys {
			v, _ := o.M[k].(*Obj)
			if v == nil {
				continue
			}
			if t, ok := v.M["continuation"].(string); ok {
				if n, ok := v.M["timeoutMs"].(float64); ok {
					return t, &n, true
				}
				return t, nil, true
			}
		}
	}
	return "", nil, false
}

// extractJSON is the object assigned after marker in a page, found by
// brace matching that skips strings.
func extractJSON(html, marker string) any {
	idx := strings.Index(html, marker)
	if idx == -1 {
		return nil
	}
	start := strings.IndexByte(html[idx+len(marker):], '{')
	if start == -1 {
		return nil
	}
	start += idx + len(marker)
	depth, inStr, esc := 0, false, false
	for i := start; i < len(html); i++ {
		c := html[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				v, err := parse([]byte(html[start : i+1]))
				if err != nil {
					return nil
				}
				return v
			}
		}
	}
	return nil
}

var ownerRE = regexp.MustCompile(`"videoDetails":\{(?:"\w+":(?:"(?:[^"\\]|\\.)*"|\[(?:"(?:[^"\\]|\\.)*",?)*\]),){0,8}"channelId":"(UC[\w-]{22})"`)

// ownerChannelID is the channel a watch page's video belongs to, or "".
func ownerChannelID(html string) string {
	if m := ownerRE.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	return ""
}
