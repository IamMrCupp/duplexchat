package twitchsend

// Stream info (title, category, tags) is the signed-in user's own channel:
//
//	Get Channel Information      any user token, no scope
//	Modify Channel Information   needs channel:manage:broadcast; 204 on success
//	  https://dev.twitch.tv/docs/api/reference/#modify-channel-information
//	Search Categories            any user token, no scope
//	  https://dev.twitch.tv/docs/api/reference/#search-categories
//
// That scope is not asked for by an ordinary sign-in. It is asked for when
// the stream info panel starts a sign-in, and kept by later sign-ins once
// granted.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/oauth"
	"github.com/IamMrCupp/duplexchat/internal/server"
)

// The limits Modify Channel Information spells out: "Title exceeds the 140
// character limit", "a maximum of 10 tags", "Each tag is limited to a
// maximum of 25 characters and may not be an empty string or contain spaces
// or special characters".
const (
	InfoTitleLimit = 140
	InfoTagLimit   = 10
	InfoTagLength  = 25

	needsPermission = "Twitch needs one more permission to change stream info."
)

var (
	_ server.InfoSender       = (*Sender)(nil)
	_ server.InfoChecker      = (*Sender)(nil)
	_ server.CategorySearcher = (*Sender)(nil)
)

func infoLimits() *jsjson.Object {
	return ok("title", float64(InfoTitleLimit), "tags", float64(InfoTagLimit), "tagLength", float64(InfoTagLength))
}

// InfoStatus is the stream info panel's view of the sign-in.
func (s *Sender) InfoStatus() server.InfoStatus {
	st := s.Status()
	needs := st.SignedIn && !s.hasInfoScope()
	detail := st.Detail
	switch {
	case !st.Configured:
		detail = "Needs a Twitch client ID — open /setup."
	case !st.SignedIn:
		if detail == "" {
			detail = "Not signed in to Twitch."
		}
	case needs:
		detail = needsPermission
	}
	return server.InfoStatus{
		Configured: st.Configured, SignedIn: st.SignedIn, As: st.As, NeedsPermission: needs,
		Ready: st.SignedIn && !needs, Detail: detail, Limits: infoLimits(),
	}
}

// CheckInfo is what Twitch rejects, said before Twitch has to. Only the
// rules the reference spells out; anything else comes back in Twitch's own
// words.
func (s *Sender) CheckInfo(title string, tags []string) string {
	if title == "" {
		return "Twitch needs a title."
	}
	if utf8.RuneCountInString(title) > InfoTitleLimit {
		return fmt.Sprintf("Twitch titles are %d characters at most.", InfoTitleLimit)
	}
	if len(tags) > InfoTagLimit {
		return fmt.Sprintf("Twitch takes %d tags at most.", InfoTagLimit)
	}
	for _, tag := range tags {
		if strings.IndexFunc(tag, jsjson.IsSpace) >= 0 {
			return "Twitch tags can't contain spaces: \"" + tag + "\"."
		}
		if utf8.RuneCountInString(tag) > InfoTagLength {
			return fmt.Sprintf("Twitch tags are %d characters at most: \"%s\".", InfoTagLength, tag)
		}
	}
	return ""
}

func failObj(code, reason string) *jsjson.Object {
	return ok("ok", false, "code", code, "reason", reason)
}

func resultObj(r server.SendResult) *jsjson.Object {
	o := failObj(r.Code, r.Reason)
	if r.RetryAfter != 0 {
		o.Set("retryAfter", float64(r.RetryAfter))
	}
	return o
}

// hasScopeWord is /scope/i without the u flag: ASCII letters fold, nothing
// else does.
func hasScopeWord(s string) bool {
	b := []byte(s)
	for i := range b {
		if 'A' <= b[i] && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return strings.Contains(string(b), "scope")
}

// helixFailure is the shared answer to a Helix call that didn't work; nil
// when it did.
func (s *Sender) helixFailure(r oauth.Response, okStatus int) (*jsjson.Object, error) {
	if r.Status == okStatus {
		return nil, nil
	}
	message := get(r.JSON, "message")
	if !jsjson.Truthy(message) {
		message = ""
	}
	switch r.Status {
	case 429:
		return resultObj(s.rateLimited(r)), nil
	case 401:
		// A live token without the scope is also a 401. That isn't a dead
		// sign-in.
		if hasScopeWord(jsjson.ToString(message)) {
			return failObj("needs_permission", needsPermission), nil
		}
		if err := s.session.Expire(); err != nil {
			return nil, err
		}
		return failObj("not_signed_in", Expired), nil
	}
	return failObj("rejected", str(r.JSON, "message", fmt.Sprintf("Twitch answered HTTP %d.", r.Status))), nil
}

// info runs one stream info call for the signed-in user. Whatever goes
// wrong on the way is an answer, not an error.
func (s *Sender) info(work func(userID string) (*jsjson.Object, error)) (*jsjson.Object, error) {
	signedOut := func() *jsjson.Object {
		reason := s.session.Problem()
		if reason == "" {
			reason = "Not signed in to Twitch."
		}
		return failObj("not_signed_in", reason)
	}
	if !s.Status().SignedIn {
		return signedOut(), nil
	}
	s.mu.Lock()
	if s.now() < s.blockedUntil {
		r := s.blocked()
		s.mu.Unlock()
		return resultObj(r), nil
	}
	s.mu.Unlock()
	// encodeURIComponent((store.get('twitch') || {}).userId)
	userID := "undefined"
	if t := s.o.Store.Get(section); t != nil {
		if v, has := t.Get("userId"); has {
			userID = jsjson.ToString(v)
		}
	}
	out, err := work(userID)
	if err != nil {
		if errors.Is(err, oauth.ErrSignedOut) {
			return signedOut(), nil
		}
		return failObj("unreachable", "Couldn't reach Twitch."), nil
	}
	return out, nil
}

// GetInfo reads the channel's title, category and tags:
// {ok, info: {title, category: {id, name} | null, tags, where}}.
func (s *Sender) GetInfo(ctx context.Context, _ string) (*jsjson.Object, error) {
	return s.info(func(userID string) (*jsjson.Object, error) {
		r, err := s.helix(ctx, http.MethodGet, "/helix/channels?broadcaster_id="+oauth.EncodeURIComponent(userID), nil)
		if err != nil {
			return nil, err
		}
		if bad, err := s.helixFailure(r, 200); bad != nil || err != nil {
			return bad, err
		}
		d := first(r.JSON)
		if !jsjson.Truthy(d) {
			return failObj("rejected", "Twitch returned no channel for this sign-in."), nil
		}
		var category any
		if truthy(d, "game_id") {
			category = ok("id", jsjson.ToString(get(d, "game_id")), "name", str(d, "game_name", ""))
		}
		tags := []any{}
		if l, isList := get(d, "tags").([]any); isList {
			for _, t := range l {
				tags = append(tags, jsjson.ToString(t))
			}
		}
		where := "#" + str(d, "broadcaster_login", str(d, "broadcaster_name", ""))
		return ok("ok", true, "info", ok("title", str(d, "title", ""), "category", category, "tags", tags, "where", where)), nil
	})
}

// SetInfo saves exactly what the form held. A nil CategoryID leaves the
// category alone; "" clears it.
func (s *Sender) SetInfo(ctx context.Context, c server.InfoChange) (*jsjson.Object, error) {
	if s.Status().SignedIn && !s.hasInfoScope() {
		return failObj("needs_permission", needsPermission), nil
	}
	return s.info(func(userID string) (*jsjson.Object, error) {
		tags := make([]any, len(c.Tags))
		for i, t := range c.Tags {
			tags[i] = t
		}
		body := ok("title", c.Title, "tags", tags)
		if c.CategoryID != nil {
			body.Set("game_id", *c.CategoryID)
		}
		r, err := s.helix(ctx, http.MethodPatch, "/helix/channels?broadcaster_id="+oauth.EncodeURIComponent(userID), body)
		if err != nil {
			return nil, err
		}
		if bad, err := s.helixFailure(r, 204); bad != nil || err != nil {
			return bad, err
		}
		return ok("ok", true), nil
	})
}

// SearchCategories is Search Categories, ten at most:
// {ok, categories: [{id, name}]}.
func (s *Sender) SearchCategories(ctx context.Context, query string) (*jsjson.Object, error) {
	return s.info(func(string) (*jsjson.Object, error) {
		r, err := s.helix(ctx, http.MethodGet, "/helix/search/categories?first=10&query="+oauth.EncodeURIComponent(query), nil)
		if err != nil {
			return nil, err
		}
		if bad, err := s.helixFailure(r, 200); bad != nil || err != nil {
			return bad, err
		}
		list := []any{}
		if data, isList := get(r.JSON, "data").([]any); isList {
			for _, c := range data {
				if !jsjson.Truthy(c) || !truthy(c, "id") {
					continue
				}
				list = append(list, ok("id", jsjson.ToString(get(c, "id")), "name", str(c, "name", "")))
			}
		}
		return ok("ok", true, "categories", list), nil
	})
}
