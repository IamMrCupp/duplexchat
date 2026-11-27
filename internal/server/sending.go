package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/gate"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// The dock's side of lib/sendapi.js at 29a9d2c: what each platform's
// sending looks like, and where a reply goes. Nothing in a response ever
// carries an access token, a refresh token or a client secret.

var platforms = []string{"twitch", "youtube"}

var label = map[string]string{"twitch": "Twitch", "youtube": "YouTube"}

// SenderStatus is what a sender says about its sign-in. ID is the signed-in
// channel's ID, when the platform has one to compare with a chat's owner.
type SenderStatus struct {
	Configured bool
	SignedIn   bool
	As         string
	ID         string
	Detail     string
}

// Sender is one platform's sender.
type Sender interface {
	Status() SenderStatus
	MaxLength() int // the platform's own message limit; 0 when it has none
}

func (s *Server) targets() map[string]targets.Target {
	if s.opts.Targets == nil {
		return map[string]targets.Target{}
	}
	return s.opts.Targets()
}

func (s *Server) disabled() string {
	if s.opts.Disabled == nil {
		return ""
	}
	return s.opts.Disabled()
}

// limit is limitOf: the platform's own limit, never above MaxText.
func (s *Server) limit(p string) int {
	if snd := s.opts.Senders[p]; snd != nil {
		if n := snd.MaxLength(); n > 0 && n < MaxText {
			return n
		}
	}
	return MaxText
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// platformStatus is each platform's sending, exactly as the dock must show it.
func (s *Server) platformStatus() Platforms {
	all := s.targets()
	one := func(p string) PlatformStatus {
		off := s.disabled()
		var snd Sender
		if off == "" {
			snd = s.opts.Senders[p]
		}
		var st SenderStatus
		if snd != nil {
			st = snd.Status()
		} else if off != "" {
			st.Detail = off
		} else {
			st.Detail = fmt.Sprintf("Sending to %s isn't in this build.", label[p])
		}
		t, ok := all[p]
		if !ok || t.Choices == nil {
			t.Choices = []targets.Choice{}
		}
		out := PlatformStatus{
			Available: snd != nil, Configured: st.Configured, SignedIn: st.SignedIn,
			As: strPtr(st.As), Detail: st.Detail,
			Connected: t.Pinned, Choices: t.Choices, MaxLength: s.limit(p),
		}
		if t.Pinned {
			out.Target, out.TargetKey = &t.Label, &t.Key
		}
		// Signed in as one channel, reading another's chat. Legitimate (a mod
		// answering in someone else's stream), and also exactly what a sign-in
		// that landed on the wrong channel looks like. So: said, not refused.
		if st.SignedIn && st.ID != "" && t.Pinned && t.OwnerID != nil && st.ID != *t.OwnerID {
			as := st.As
			if as == "" {
				as = "a channel"
			}
			out.Warning = fmt.Sprintf("Signed in as %s — not the channel this %s chat belongs to. Replies show under that name. Wrong channel? Sign out and in again on /setup.", as, label[p])
		}
		return out
	}
	return Platforms{Twitch: one("twitch"), YouTube: one("youtube")}
}

// maxAPIBody is readJson's limit, in UTF-16 units as Node counts them.
const maxAPIBody = 16384

var errTooLarge = errors.New("body too large")

// readJSON is the send API's readJson: a JSON object, {} for an empty body.
func readJSON(r *http.Request) (*jsjson.Object, error) {
	// 16384 UTF-16 units are at most 3 bytes each in UTF-8
	raw, err := io.ReadAll(io.LimitReader(r.Body, 3*maxAPIBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 3*maxAPIBody || utf16Len(raw) > maxAPIBody {
		return nil, errTooLarge
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	v, err := jsjson.Parse(raw)
	if err != nil {
		return nil, errors.New("bad json")
	}
	o, ok := v.(*jsjson.Object)
	if !ok {
		// null, a number, an array: valid JSON, not a request
		return nil, errors.New("body must be a JSON object")
	}
	return o, nil
}

func utf16Len(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		n += utf16.RuneLen(r)
	}
	return n
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// dockTarget is POST /api/dock/target: an explicit pick from the dock.
func (s *Server) dockTarget(w http.ResponseWriter, r *http.Request) {
	if !s.gated(w, r, gate.Require, true) {
		return
	}
	body, err := readJSON(r)
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiError{err.Error(), "bad_request"})
		return
	}
	p, _ := body.Get("platform")
	src, _ := body.Get("source")
	platform, _ := p.(string)
	source, isString := src.(string)
	if label[platform] == "" || !isString || s.opts.SetTarget == nil || !s.opts.SetTarget(platform, source) {
		apiJSON(w, http.StatusBadRequest, apiError{"No such source for that platform.", "bad_target"})
		return
	}
	apiJSON(w, http.StatusOK, struct {
		Platforms Platforms `json:"platforms"`
	}{s.platformStatus()})
}
