package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/gate"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// POST /api/send (lib/sendapi.js at 29a9d2c). A reply goes only where the
// page said it would: the dock sends back the target keys it was showing.

// SendResult is a sender's answer. OK with an optional ID, or a failure
// with the platform's code and reason; RetryAfter is in whole seconds.
type SendResult struct {
	OK         bool
	ID         string
	Code       string
	Reason     string
	RetryAfter int
}

// result is a SendResult as the dock reads it.
type result struct {
	OK         bool   `json:"ok"`
	ID         string `json:"id,omitempty"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	RetryAfter int    `json:"retryAfter,omitempty"`
}

func fail(code, reason string) result { return result{Code: code, Reason: reason} }

var breaks = regexp.MustCompile(`[\r\n\t]+`)

// jsTrim is String#trim: JavaScript's white space and line terminators.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0xfeff, 0x2028, 0x2029:
			return true
		}
		return unicode.Is(unicode.Zs, r)
	})
}

// send is POST /api/send.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	if !s.gated(w, r, gate.Require, true) {
		return
	}
	body, err := readJSON(r)
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiError{err.Error(), "bad_request"})
		return
	}
	tv, _ := body.Get("target")
	target, _ := tv.(string)
	var wanted []string
	switch {
	case target == "both":
		wanted = platforms
	case label[target] != "":
		wanted = []string{target}
	default:
		apiJSON(w, http.StatusBadRequest, apiError{"target must be twitch, youtube or both", "bad_target"})
		return
	}
	raw, _ := body.Get("text")
	text, _ := raw.(string)
	text = jsTrim(breaks.ReplaceAllString(text, " "))
	if text == "" {
		apiJSON(w, http.StatusBadRequest, apiError{"Nothing to send.", "empty"})
		return
	}
	// Checked against every wanted platform before anything is sent, so a
	// "both" can't land on one chat only because it was too long for the other.
	length := utf8.RuneCountInString(text)
	var over []string
	for _, p := range wanted {
		if length > s.limit(p) {
			over = append(over, p)
		}
	}
	sort.SliceStable(over, func(i, j int) bool { return s.limit(over[i]) < s.limit(over[j]) })
	if len(over) > 0 {
		p := over[0]
		apiJSON(w, http.StatusBadRequest, struct {
			Error    string `json:"error"`
			Code     string `json:"code"`
			Platform string `json:"platform"`
			Limit    int    `json:"limit"`
			Length   int    `json:"length"`
		}{fmt.Sprintf("Too long for %s — %d characters at most, this is %d. Nothing was sent.", label[p], s.limit(p), length), "too_long", p, s.limit(p), length})
		return
	}
	expect := jsjson.NewObject()
	if ev, _ := body.Get("expect"); ev != nil {
		if o, ok := ev.(*jsjson.Object); ok {
			expect = o
		}
	}
	all := s.targets()
	// a send carries on if the page goes away: it may already be on the wire
	ctx := context.WithoutCancel(r.Context())
	results := make([]result, len(wanted))
	var wg sync.WaitGroup
	for i, p := range wanted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, _ := expect.Get(p)
			results[i] = s.sendOne(ctx, p, text, all, e)
		}()
	}
	wg.Wait()
	apiJSON(w, http.StatusOK, struct {
		Results resultsJSON `json:"results"`
	}{resultsJSON{wanted, results}})
}

// resultsJSON keeps the platforms in a fixed order. (Node's follows which
// send finished first; the dock reads them by name.)
type resultsJSON struct {
	keys []string
	vals []result
}

func (r resultsJSON) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range r.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		v, err := marshalNoEscape(r.vals[i])
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%q:%s", k, v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// marshalNoEscape is json.Marshal without HTML escaping, as JSON.stringify.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func (s *Server) sendOne(ctx context.Context, p, text string, all map[string]targets.Target, expect any) (res result) {
	if off := s.disabled(); off != "" {
		return fail("sending_disabled", off)
	}
	snd := s.opts.Senders[p]
	if snd == nil {
		return fail("unavailable", fmt.Sprintf("Sending to %s isn't in this build.", label[p]))
	}
	if !snd.Status().SignedIn {
		return fail("not_signed_in", fmt.Sprintf("Not signed in to %s — open /setup.", label[p]))
	}
	t, ok := all[p]
	if !ok || !t.Pinned {
		if len(t.Choices) > 0 {
			return fail("no_target", fmt.Sprintf("No %s reply target is chosen — pick one in the dock.", label[p]))
		}
		return fail("no_target", fmt.Sprintf("No %s chat is connected in the panel.", label[p]))
	}
	// the page says where the reply is going; send only if that's still true
	if key, isString := expect.(string); !isString || key != t.Key {
		return fail("target_changed", fmt.Sprintf("The %s reply target is now %s — check it and send again.", label[p], t.Label))
	}
	defer func() {
		if v := recover(); v != nil {
			s.opts.Log("send:"+p, v)
			res = fail("error", fmt.Sprintf("Couldn't reach %s.", label[p]))
		}
	}()
	r, err := snd.Send(ctx, text, t)
	if err != nil {
		s.opts.Log("send:"+p, err)
		return fail("error", fmt.Sprintf("Couldn't reach %s.", label[p]))
	}
	if r.OK {
		return result{OK: true, ID: r.ID}
	}
	code, reason := r.Code, r.Reason
	if code == "" {
		code = "failed"
	}
	if reason == "" {
		reason = "The platform refused the message."
	}
	return result{Code: code, Reason: reason, RetryAfter: r.RetryAfter}
}
