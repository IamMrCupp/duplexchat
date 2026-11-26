package twitch

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/app"
)

// jsSpace is the set JavaScript's \s matches.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// splitSpace is text.split(/(\s+)/): words and the whitespace runs between
// them, in order, with "" where the text starts or ends in whitespace.
func splitSpace(text string) []string {
	var out []string
	var cur strings.Builder
	inSpace := false
	started := false
	for _, r := range text {
		sp := jsSpace(r)
		if started && sp != inSpace {
			out = append(out, cur.String())
			cur.Reset()
		} else if !started && sp {
			out = append(out, "") // leading whitespace: split yields an empty first piece
		}
		started = true
		inSpace = sp
		cur.WriteRune(r)
	}
	out = append(out, cur.String())
	if inSpace && started {
		out = append(out, "")
	}
	return out
}

// pushText adds plain text, splitting out third-party emote words.
func pushText(parts []app.Part, text string, ext map[string]string) []app.Part {
	if len(ext) == 0 {
		if text != "" {
			parts = append(parts, app.Part{T: "text", V: text})
		}
		return parts
	}
	buf := ""
	for _, tok := range splitSpace(text) {
		u := ""
		if strings.IndexFunc(tok, func(r rune) bool { return !jsSpace(r) }) >= 0 {
			u = ext[tok]
		}
		if u != "" {
			if buf != "" {
				parts = append(parts, app.Part{T: "text", V: buf})
				buf = ""
			}
			parts = append(parts, app.Part{T: "emote", URL: u, Alt: tok})
		} else {
			buf += tok
		}
	}
	if buf != "" {
		parts = append(parts, app.Part{T: "text", V: buf})
	}
	return parts
}

// buildParts splits a message into text and emotes: Twitch's own (by code
// point ranges from the tags) first, third-party words in what's left.
func buildParts(text string, tags map[string]string, ext map[string]string) []app.Part {
	cps := []rune(text)
	n := len(cps)
	parts := []app.Part{}
	pos := 0.0
	for _, r := range collectRanges(tags, n) {
		if r.s < pos {
			continue // overlapping or bogus
		}
		if r.s > pos {
			parts = pushText(parts, string(cps[sliceIdx(pos, n):sliceIdx(r.s, n)]), ext)
		}
		parts = append(parts, app.Part{T: "emote", URL: r.url, Alt: string(cps[sliceIdx(r.s, n):sliceIdx(r.e+1, n)])})
		pos = r.e + 1
	}
	if pos < float64(n) {
		parts = pushText(parts, string(cps[sliceIdx(pos, n):]), ext)
	}
	return parts
}

var badgeRoles = map[string]string{
	"broadcaster": "owner", "moderator": "mod", "vip": "vip", "subscriber": "sub", "founder": "sub",
	"partner": "verified", "staff": "staff", "admin": "staff", "global_mod": "staff",
}

// rolesFromBadges maps the badges tag to roles, in order, without repeats.
func rolesFromBadges(tag string) []string {
	roles := []string{}
	if tag == "" {
		return roles
	}
	for _, b := range strings.Split(tag, ",") {
		role := badgeRoles[strings.SplitN(b, "/", 2)[0]]
		if role == "" {
			continue
		}
		dup := false
		for _, r := range roles {
			dup = dup || r == role
		}
		if !dup {
			roles = append(roles, role)
		}
	}
	return roles
}

var usernoticeSystem = map[string]bool{
	"sub": true, "resub": true, "subgift": true, "submysterygift": true, "anonsubgift": true, "giftpaidupgrade": true,
	"primepaidupgrade": true, "raid": true, "bitsbadgetier": true, "viewermilestone": true, "modiversary": true,
}

var reservedSegments = map[string]bool{
	"videos": true, "directory": true, "subscriptions": true, "subs": true, "collections": true, "clip": true, "clips": true,
	"settings": true, "wallet": true, "drops": true, "search": true, "friends": true, "jobs": true, "p": true, "downloads": true,
	"store": true, "turbo": true, "login": true, "signup": true,
}

var (
	schemeRE  = regexp.MustCompile(`(?i)^https?:`)
	twitchRE  = regexp.MustCompile(`(?i)(^|\.)twitch\.tv$`)
	channelRE = regexp.MustCompile(`^[a-z0-9_]{2,25}$`)
)

// ChannelFromURL gets a channel login from any form of Twitch link —
// twitch.tv/name, /popout/name/chat, /moderator/name, /embed/name/chat,
// m.twitch.tv/name, dashboard.twitch.tv/u/name/…, player.twitch.tv/?channel=name
// — or "" when the link doesn't name one.
func ChannelFromURL(link string) string {
	if !schemeRE.MatchString(link) {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if !twitchRE.MatchString(host) {
		return ""
	}
	login := ""
	if strings.EqualFold(host, "player.twitch.tv") {
		login = u.Query().Get("channel")
	} else {
		var seg []string
		for _, s := range strings.Split(u.EscapedPath(), "/") {
			if s != "" {
				seg = append(seg, s)
			}
		}
		if len(seg) == 0 {
			return ""
		}
		login = seg[0]
		switch l := strings.ToLower(login); {
		case (l == "popout" || l == "moderator" || l == "embed" || l == "u") && len(seg) > 1:
			login = seg[1]
		case reservedSegments[l]:
			return ""
		}
	}
	login = strings.ToLower(login)
	if !utf8.ValidString(login) || !channelRE.MatchString(login) {
		return ""
	}
	return login
}
