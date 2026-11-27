package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/IamMrCupp/duplexchat/internal/gate"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

// The setup half of lib/sendapi.js at 29a9d2c: what /setup reads, the
// actions it posts, and the browser redirect back from a platform's
// sign-in. The senders fill these in. A sender has each one only if it
// implements the matching interface, as a Node sender has each method only
// if it defines it.

// SetupResult is a setup action's answer. A nil Body sends no body.
type SetupResult struct {
	Status int
	Body   *jsjson.Object
}

// Setupper is a sender with actions on /setup
// (POST /api/setup/<platform>/<action>).
type Setupper interface {
	// Setup runs one action. nil means there's no such action (404). An
	// error (or a panic) is logged and /setup is told only that the platform
	// couldn't be reached.
	Setup(ctx context.Context, action string, body *jsjson.Object, port int) (*SetupResult, error)
}

// SetupStater adds page-safe fields to the platform's entry on /setup.
// Never a token or a client secret.
type SetupStater interface {
	SetupState(port int) *jsjson.Object
}

// Callbacker takes a browser redirect coming back from the platform's
// sign-in.
type Callbacker interface {
	CallbackMatches(r *http.Request) bool
	// Callback answers it. An error (or a panic) is logged, and answered
	// 502 if nothing has been written yet.
	Callback(w http.ResponseWriter, r *http.Request) error
}

func (s *Server) notices() []string {
	if s.opts.Notices == nil {
		return nil
	}
	return s.opts.Notices()
}

// object is the entry as an ordered object, so a sender's fields can be
// added to it as Object.assign adds them.
func (ps PlatformStatus) object() *jsjson.Object {
	b, err := marshalNoEscape(ps)
	if err != nil {
		panic(err)
	}
	v, err := jsjson.Parse(b)
	if err != nil {
		panic(err)
	}
	return v.(*jsjson.Object)
}

// setupState is GET /api/setup/state.
func (s *Server) setupState(w http.ResponseWriter, r *http.Request) {
	if !s.gated(w, r, gate.IfPresent, true) {
		return
	}
	status := s.platformStatus()
	entries := map[string]*jsjson.Object{"twitch": status.Twitch.object(), "youtube": status.YouTube.object()}
	off := s.disabled()
	out := jsjson.NewObject()
	for _, p := range platforms {
		entry := entries[p]
		if st, ok := s.opts.Senders[p].(SetupStater); ok && off == "" {
			if extra := st.SetupState(s.opts.Port); extra != nil {
				for _, k := range extra.Keys() {
					v, _ := extra.Get(k)
					entry.Set(k, v)
				}
			}
		}
		out.Set(p, entry)
	}
	notices := []any{}
	for _, n := range append([]string{off}, s.notices()...) {
		if n != "" {
			notices = append(notices, n)
		}
	}
	apiRaw(w, http.StatusOK, jsjson.Stringify(jsjson.NewObject().Set("platforms", out).Set("notices", notices), ""))
}

var setupPath = regexp.MustCompile(`^/api/setup/(twitch|youtube)/([a-z-]+)$`)

// setupAction is POST /api/setup/<platform>/<action>.
func (s *Server) setupAction(w http.ResponseWriter, r *http.Request, p, action string) {
	if !s.gated(w, r, gate.Require, true) {
		return
	}
	if off := s.disabled(); off != "" {
		apiJSON(w, http.StatusConflict, apiError{off, "sending_disabled"})
		return
	}
	snd, ok := s.opts.Senders[p].(Setupper)
	if !ok {
		apiJSON(w, http.StatusNotFound, apiError{fmt.Sprintf("Sending to %s isn't in this build.", label[p]), "unavailable"})
		return
	}
	body, err := readJSON(r)
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiError{err.Error(), "bad_request"})
		return
	}
	res, err := s.runSetup(snd, r, action, body, p)
	if err != nil {
		s.opts.Log("setup:"+p, err)
		apiJSON(w, http.StatusBadGateway, apiError{fmt.Sprintf("Couldn't reach %s.", label[p]), "error"})
		return
	}
	if res == nil {
		apiJSON(w, http.StatusNotFound, apiError{"not found", "not_found"})
		return
	}
	var b []byte
	if res.Body != nil {
		b = jsjson.Stringify(res.Body, "")
	}
	apiRaw(w, res.Status, b)
}

// runSetup turns a panic into an error, and a status Node's writeHead
// would throw on into one too.
func (s *Server) runSetup(snd Setupper, r *http.Request, action string, body *jsjson.Object, p string) (res *SetupResult, err error) {
	defer func() {
		if v := recover(); v != nil {
			res, err = nil, fmt.Errorf("panic: %v", v)
		}
	}()
	res, err = snd.Setup(context.WithoutCancel(r.Context()), action, body, s.opts.Port)
	if err == nil && res != nil && (res.Status < 100 || res.Status > 999) {
		return nil, fmt.Errorf("invalid status code: %d", res.Status)
	}
	return res, err
}

// callback answers a sign-in redirect when a sender claims it; false means
// none did. A copy that doesn't own sending claims nothing.
func (s *Server) callback(w *recorder, r *http.Request) bool {
	if s.disabled() != "" {
		return false
	}
	for _, p := range platforms {
		cb, ok := s.opts.Senders[p].(Callbacker)
		if !ok || !cb.CallbackMatches(r) {
			continue
		}
		// the redirect arrives from the platform's site: no Origin check, no
		// token, but still this machine and this server's own Host
		if !s.gated(w, r, gate.Skip, false) {
			return true
		}
		if err := runCallback(cb, w, r); err != nil {
			s.opts.Log("callback:"+p, err)
			if !w.wrote {
				apiJSON(w, http.StatusBadGateway, apiError{fmt.Sprintf("Couldn't reach %s.", label[p]), "error"})
			}
		}
		return true
	}
	return false
}

func runCallback(cb Callbacker, w http.ResponseWriter, r *http.Request) (err error) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return cb.Callback(w, r)
}
