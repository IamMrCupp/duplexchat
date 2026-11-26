package server

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/IamMrCupp/duplexchat/internal/gate"
)

// MaxText is the longest reply the dock may send (multichat MAX_TEXT).
const MaxText = 500

// PlatformStatus is one platform's entry in the session's platforms, in
// multichat's key order (lib/sendapi.js platformStatus).
type PlatformStatus struct {
	Available  bool    `json:"available"`
	Configured bool    `json:"configured"`
	SignedIn   bool    `json:"signedIn"`
	As         *string `json:"as"`
	Detail     string  `json:"detail"`
	Connected  bool    `json:"connected"`
	Target     *string `json:"target"`
	TargetKey  *string `json:"targetKey"`
	Choices    []any   `json:"choices"`
	MaxLength  int     `json:"maxLength"`
	Warning    string  `json:"warning"`
}

// Platforms is the session's platforms, Twitch first.
type Platforms struct {
	Twitch  PlatformStatus `json:"twitch"`
	YouTube PlatformStatus `json:"youtube"`
}

// NoSending is what multichat reports with no senders: nothing signed in,
// nothing to reply to. Sending replaces it (#9–#12).
func NoSending() Platforms {
	off := func(label string) PlatformStatus {
		return PlatformStatus{Detail: "Sending to " + label + " isn't in this build.", Choices: []any{}, MaxLength: MaxText}
	}
	return Platforms{Twitch: off("Twitch"), YouTube: off("YouTube")}
}

// apiJSON is the send API's json(): no caching, no sniffing.
func apiJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// gated runs the gate; on refusal it answers 403 {error, code} and returns
// false. needToken asks for the session token too.
func (s *Server) gated(w http.ResponseWriter, r *http.Request, mode string, needToken bool) bool {
	o := gate.Options{Port: s.opts.Port, Origin: mode, Peer: s.opts.Peer}
	if needToken {
		o.Token = &s.opts.Token
	}
	d := gate.Check(r, o)
	if d == nil {
		return true
	}
	apiJSON(w, d.Status, struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}{d.Error, d.Code})
	return false
}

// guard is the panel's gate: Origin required unless said otherwise, token
// required (multichat sendapi guard()).
func (s *Server) guard(w http.ResponseWriter, r *http.Request, mode string) bool {
	if mode == "" {
		mode = gate.Require
	}
	return s.gated(w, r, mode, true)
}

// session is GET /api/dock/session: hands this server's own pages the token.
// No token needed to ask, but the gate still checks peer, Host and Origin.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if !s.gated(w, r, gate.IfPresent, false) {
		return
	}
	platforms := NoSending()
	if s.opts.Platforms != nil {
		platforms = s.opts.Platforms()
	}
	apiJSON(w, http.StatusOK, struct {
		Token     string    `json:"token"`
		MaxLength int       `json:"maxLength"`
		Platforms Platforms `json:"platforms"`
	}{s.opts.Token, MaxText, platforms})
}
