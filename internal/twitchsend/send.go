// Package twitchsend is sending to Twitch chat (lib/twitch-send.js at
// 29a9d2c). Reading stays on the anonymous IRC connection in
// internal/twitch and doesn't depend on any of this: if the token dies, the
// reader keeps reading.
//
// Sign-in is the device code grant flow with a public client: no redirect
// server, no client secret.
//
//	https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/#device-code-grant-flow
//
// Sending is the Send Chat Message endpoint with a user access token, which
// needs exactly one scope, user:write:chat.
//
//	https://dev.twitch.tv/docs/api/reference/#send-chat-message
//
// A public client's refresh token is single-use, so the new one is written
// to disk before anything else happens (internal/oauth). On HTTP 429 the
// Ratelimit-Reset header says when the bucket refills; nothing is sent again
// before then.
//
//	https://dev.twitch.tv/docs/api/guide/#twitch-rate-limits
//
// The stream info scope is asked for when the stream info panel starts a
// sign-in, and kept by later sign-ins once granted. The panel itself comes
// with #17.
package twitchsend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/oauth"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

const (
	// Scopes is what an ordinary sign-in asks for.
	Scopes = "user:write:chat"
	// InfoScope is what changing the stream info needs.
	InfoScope = "channel:manage:broadcast"
	// MaxMessage is "limited to a maximum of 500 characters" (Send Chat
	// Message reference).
	MaxMessage = 500
	// Expired is what the pages are told when Twitch kills the sign-in.
	Expired = "Twitch sign-in expired — sign in again."

	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	section     = "twitch"
)

var clientIDRE = regexp.MustCompile(`(?i)^[a-z0-9]{10,64}$`)

// Options are the sender's seams.
type Options struct {
	Store *secrets.Store
	// The client ID lives in config.json (it's public).
	ClientID    func() string
	SetClientID func(id string)
	// OnAuthChange is called after a sign-in lands or a sign-in ends, so
	// whoever shows something that depends on it can ask again now. A panic
	// in it is contained.
	OnAuthChange func()

	IDBase, APIBase string // "" → https://id.twitch.tv, https://api.twitch.tv
	Now             func() time.Time
	PollInterval    time.Duration // 0 → the interval Twitch asks for
	Timeout         time.Duration // one call; 0 → oauth.DefaultTimeout
	Client          *http.Client
}

// Sender sends to Twitch chat.
type Sender struct {
	o       Options
	session *oauth.Session

	mu             sync.Mutex
	pending        *pending // device flow in progress
	pendingError   string
	blockedUntil   float64 // rate limit: no sends before this time (ms)
	broadcasterIDs map[string]string
}

type pending struct {
	deviceCode      any
	userCode        string
	verificationURI string
	scopes          string
	intervalMs      float64
	expiresAt       float64
	timer           *time.Timer
}

// New returns a sender.
func New(o Options) *Sender {
	if o.IDBase == "" {
		o.IDBase = "https://id.twitch.tv"
	}
	if o.APIBase == "" {
		o.APIBase = "https://api.twitch.tv"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Sender{o: o, broadcasterIDs: map[string]string{}}
	s.session = &oauth.Session{
		Store: o.Store, Section: section, Now: o.Now, Client: o.Client, Timeout: o.Timeout,
		ExpiredMessage: Expired,
		RefreshRequest: func(t *jsjson.Object) (string, []oauth.Param) {
			rt, _ := t.Get("refreshToken")
			return o.IDBase + "/oauth2/token", []oauth.Param{
				{Key: "client_id", Value: o.ClientID()},
				{Key: "grant_type", Value: "refresh_token"},
				{Key: "refresh_token", Value: jsjson.ToString(rt)},
			}
		},
	}
	return s
}

func (s *Sender) now() float64 { return float64(s.o.Now().UnixMilli()) }

func get(v any, k string) any {
	x, _ := jsjson.Get(v, k)
	return x
}

func truthy(v any, k string) bool { return jsjson.Truthy(get(v, k)) }

// str is String(v[k] || fallback).
func str(v any, k, fallback string) string {
	if x := get(v, k); jsjson.Truthy(x) {
		return jsjson.ToString(x)
	}
	return fallback
}

// first is `Array.isArray(v.data) && v.data[0]`.
func first(v any) any {
	if d, ok := get(v, "data").([]any); ok && len(d) > 0 {
		return d[0]
	}
	return nil
}

func ok(fields ...any) *jsjson.Object {
	o := jsjson.NewObject()
	for i := 0; i+1 < len(fields); i += 2 {
		o.Set(fields[i].(string), fields[i+1])
	}
	return o
}

func answer(status int, body *jsjson.Object) *server.SetupResult {
	return &server.SetupResult{Status: status, Body: body}
}

func failure(code, reason string) server.SendResult {
	return server.SendResult{Code: code, Reason: reason}
}

// ---------- state the pages may see ----------

// Status is the sign-in as the dock shows it.
func (s *Sender) Status() server.SenderStatus {
	t := s.o.Store.Get(section)
	clientID := s.o.ClientID()
	signedIn := clientID != "" && t != nil && truthy(t, "refreshToken")
	st := server.SenderStatus{Configured: clientID != "", SignedIn: signedIn}
	if clientID == "" {
		st.Detail = "Needs a Twitch client ID."
	} else if p := s.session.Problem(); !signedIn && p != "" {
		st.Detail = p
	}
	if signedIn {
		st.As = str(t, "displayName", str(t, "login", ""))
	}
	return st
}

// MaxLength is Twitch's own message limit.
func (s *Sender) MaxLength() int { return MaxMessage }

// SetupState is what /setup shows: never a token or the device code.
func (s *Sender) SetupState(int) *jsjson.Object {
	has := s.hasInfoScope()
	s.mu.Lock()
	defer s.mu.Unlock()
	var p any
	if s.pending != nil {
		p = ok("userCode", s.pending.userCode, "verificationUri", s.pending.verificationURI, "expiresAt", s.pending.expiresAt)
	}
	return ok("clientId", s.o.ClientID(), "scopes", Scopes, "infoScope", InfoScope,
		"hasInfoScope", has, "pending", p, "pendingError", s.pendingError)
}

// scopes is what the sign-in granted. Sign-ins from before scopes were
// recorded had chat only.
func (s *Sender) scopes() []any {
	t := s.o.Store.Get(section)
	if t == nil || !truthy(t, "refreshToken") {
		return nil
	}
	if l, ok := get(t, "scopes").([]any); ok {
		return l
	}
	return []any{Scopes}
}

func (s *Sender) hasInfoScope() bool {
	for _, v := range s.scopes() {
		if v == InfoScope {
			return true
		}
	}
	return false
}

// ---------- http ----------

func (s *Sender) form(ctx context.Context, path string, params ...oauth.Param) (oauth.Response, error) {
	return oauth.PostForm(ctx, s.o.Client, s.o.IDBase+path, params, s.o.Timeout)
}

func param(k, v string) oauth.Param { return oauth.Param{Key: k, Value: v} }

// ---------- setup actions (POST /api/setup/twitch/<action>) ----------

// Setup runs one of /setup's actions; nil for one it doesn't have.
func (s *Sender) Setup(ctx context.Context, action string, body *jsjson.Object, _ int) (*server.SetupResult, error) {
	switch action {
	case "client":
		return s.setClient(body)
	case "signin":
		return s.startDeviceFlow(ctx, body)
	case "cancel":
		s.mu.Lock()
		s.stopPending()
		s.pendingError = ""
		s.mu.Unlock()
		return answer(200, ok("ok", true)), nil
	case "signout":
		return s.signOut(ctx)
	}
	return nil, nil
}

func (s *Sender) setClient(body *jsjson.Object) (*server.SetupResult, error) {
	id := jsjson.Trim(str(body, "clientId", ""))
	if id != "" && !clientIDRE.MatchString(id) {
		return answer(400, ok("error", "That doesn't look like a Twitch client ID.", "code", "bad_client_id")), nil
	}
	s.mu.Lock()
	changed := id != s.o.ClientID()
	if changed {
		// tokens belong to the client that got them
		s.stopPending()
		s.session.Invalidate()
		if err := s.o.Store.Set(section, nil); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.session.SetProblem("")
		s.pendingError = ""
		s.o.SetClientID(id)
	}
	s.mu.Unlock()
	if changed {
		s.authChanged()
	}
	return answer(200, ok("ok", true)), nil
}

// startDeviceFlow asks Twitch for a code to show. body.info: the stream info
// panel is asking, so ask for its scope too. A permission already granted is
// asked for again, or signing in a second time from /setup would quietly
// take it away.
func (s *Sender) startDeviceFlow(ctx context.Context, body *jsjson.Object) (*server.SetupResult, error) {
	clientID := s.o.ClientID()
	if clientID == "" {
		return answer(400, ok("error", "Paste a Twitch client ID first.", "code", "no_client_id")), nil
	}
	scopes := Scopes
	if info, _ := body.Get("info"); info == true || s.hasInfoScope() {
		scopes += " " + InfoScope
	}
	s.mu.Lock()
	s.stopPending()
	s.pendingError = ""
	s.mu.Unlock()
	r, err := s.form(ctx, "/oauth2/device", param("client_id", clientID), param("scopes", scopes))
	if err != nil {
		return nil, err
	}
	j := r.Object()
	if r.Status != 200 || !truthy(j, "device_code") || !truthy(j, "user_code") {
		why := str(j, "message", fmt.Sprintf("HTTP %d", r.Status))
		return answer(502, ok("error", "Twitch wouldn't start sign-in: "+why, "code", "device_flow_failed")), nil
	}
	intervalMs := float64(s.o.PollInterval.Milliseconds())
	if intervalMs == 0 {
		n := jsjson.ToNumber(j.Get("interval"))
		if !jsjson.Truthy(n) {
			n = 5
		}
		intervalMs = math.Max(1000, n*1000)
	}
	expiresIn := jsjson.ToNumber(j.Get("expires_in"))
	if !jsjson.Truthy(expiresIn) {
		expiresIn = 1800
	}
	userCode, _ := j.Get("user_code")
	device, _ := j.Get("device_code")
	p := &pending{
		deviceCode:      device,
		userCode:        jsjson.ToString(userCode),
		verificationURI: str(j, "verification_uri", ""),
		scopes:          scopes,
		intervalMs:      intervalMs,
		expiresAt:       s.now() + expiresIn*1000,
	}
	s.mu.Lock()
	s.pending = p
	s.schedule(p)
	s.mu.Unlock()
	return answer(200, ok("userCode", p.userCode, "verificationUri", p.verificationURI, "expiresAt", p.expiresAt)), nil
}

// delay is setTimeout's: anything below 1 ms or above 2^31-1 ms is 1 ms.
func delay(ms float64) time.Duration {
	if !(ms >= 1 && ms <= math.MaxInt32) {
		ms = 1
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// schedule polls p after its interval. One poll failing (a Wi-Fi blip, DNS,
// a timeout) isn't the flow failing: keep asking until the device code
// itself runs out. Runs with s.mu held.
func (s *Sender) schedule(p *pending) {
	p.timer = time.AfterFunc(delay(p.intervalMs), func() {
		if err := s.poll(p); err != nil {
			s.mu.Lock()
			if s.pending == p {
				s.schedule(p)
			}
			s.mu.Unlock()
		}
	})
}

// stopPending runs with s.mu held.
func (s *Sender) stopPending() {
	if s.pending != nil && s.pending.timer != nil {
		s.pending.timer.Stop()
	}
	s.pending = nil
}

// pendingFailed runs with s.mu held.
func (s *Sender) pendingFailed(p *pending, why string) {
	if s.pending != p {
		return
	}
	s.stopPending()
	s.pendingError = why
}

// poll asks once whether the code was approved. An error means the
// question didn't get an answer.
func (s *Sender) poll(p *pending) error {
	s.mu.Lock()
	if s.pending != p {
		s.mu.Unlock()
		return nil
	}
	if s.now() >= p.expiresAt {
		s.pendingFailed(p, "The code expired before it was approved. Start again.")
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	clientID := s.o.ClientID()
	r, err := s.form(context.Background(), "/oauth2/token",
		param("client_id", clientID), param("scopes", p.scopes),
		param("device_code", jsjson.ToString(p.deviceCode)), param("grant_type", deviceGrant))
	if err != nil {
		return err
	}
	j := r.Object()
	if r.Status == 200 && truthy(j, "access_token") && truthy(j, "refresh_token") {
		s.mu.Lock()
		gone := s.pending != p
		s.mu.Unlock()
		if gone {
			return nil
		}
		// Twitch says what it granted; if it doesn't, it's what was asked for.
		tokens := s.tokensFrom(j)
		var granted []any
		if l, ok := get(j, "scope").([]any); ok {
			for _, v := range l {
				granted = append(granted, jsjson.ToString(v))
			}
		} else {
			for _, v := range strings.Split(p.scopes, " ") {
				granted = append(granted, v)
			}
		}
		if granted == nil {
			granted = []any{}
		}
		tokens.Set("scopes", granted)
		access, _ := tokens.Get("accessToken")
		req, err := http.NewRequest(http.MethodGet, s.o.APIBase+"/helix/users", nil)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+jsjson.ToString(access))
		req.Header.Set("client-id", clientID)
		who, err := oauth.Fetch(context.Background(), s.o.Client, req, s.o.Timeout)
		if err != nil {
			return err
		}
		u := first(who.JSON)
		s.mu.Lock()
		if s.pending != p {
			s.mu.Unlock()
			return nil
		}
		if who.Status != 200 || !jsjson.Truthy(u) || !truthy(u, "id") {
			s.pendingFailed(p, "Signed in, but Twitch wouldn't say as whom. Start again.")
			s.mu.Unlock()
			return nil
		}
		login := get(u, "login")
		if !jsjson.Truthy(login) {
			login = ""
		}
		display := get(u, "display_name")
		if !jsjson.Truthy(display) {
			display = login
		}
		tokens.Set("userId", jsjson.ToString(get(u, "id"))).Set("login", login).Set("displayName", display)
		s.session.Invalidate()
		if err := s.o.Store.Set(section, tokens); err != nil {
			s.mu.Unlock()
			return err
		}
		s.stopPending()
		s.session.SetProblem("")
		s.pendingError = ""
		s.mu.Unlock()
		s.authChanged()
		return nil
	}
	msg := str(j, "message", "")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != p {
		return nil
	}
	switch {
	case r.Status == 400 && msg == "authorization_pending":
		s.schedule(p)
	case r.Status == 400 && msg == "slow_down":
		p.intervalMs += 5000
		s.schedule(p)
	default:
		why := msg
		if why == "" {
			why = fmt.Sprintf("HTTP %d", r.Status)
		}
		s.pendingFailed(p, "Twitch refused the sign-in: "+why+". Start again.")
	}
	return nil
}

// authChanged tells the listener. One that panics must not undo a sign-in
// or a sign-out.
func (s *Sender) authChanged() {
	if s.o.OnAuthChange == nil {
		return
	}
	defer func() { recover() }()
	s.o.OnAuthChange()
}

func (s *Sender) tokensFrom(j *jsjson.Object) *jsjson.Object {
	access, _ := j.Get("access_token")
	refresh, _ := j.Get("refresh_token")
	in := jsjson.ToNumber(j.Get("expires_in"))
	if !jsjson.Truthy(in) {
		in = 0
	}
	return ok("accessToken", access, "refreshToken", refresh, "expiresAt", s.now()+in*1000)
}

func (s *Sender) signOut(ctx context.Context) (*server.SetupResult, error) {
	s.mu.Lock()
	s.stopPending()
	t := s.o.Store.Get(section)
	clientID := s.o.ClientID()
	s.session.Invalidate() // a refresh in flight must not write the tokens back
	if err := s.o.Store.Set(section, nil); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.session.SetProblem("")
	s.pendingError = ""
	s.broadcasterIDs = map[string]string{}
	s.mu.Unlock()
	s.authChanged()
	// Best effort: the local copy is gone either way.
	//   https://dev.twitch.tv/docs/authentication/revoke-tokens/
	if t != nil && truthy(t, "accessToken") && clientID != "" {
		access, _ := t.Get("accessToken")
		s.form(ctx, "/oauth2/revoke", param("client_id", clientID), param("token", jsjson.ToString(access)))
	}
	return answer(200, ok("ok", true)), nil
}

// helix is an authorised Helix call: one refresh and one retry on a 401.
func (s *Sender) helix(ctx context.Context, method, pathAndQuery string, body *jsjson.Object) (oauth.Response, error) {
	return s.session.Authorized(ctx, func(t *jsjson.Object) (oauth.Response, error) {
		var payload []byte
		if body != nil {
			payload = jsjson.Stringify(body, "")
		}
		req, err := http.NewRequest(method, s.o.APIBase+pathAndQuery, bytes.NewReader(payload))
		if err != nil {
			return oauth.Response{}, err
		}
		access, _ := t.Get("accessToken")
		req.Header.Set("authorization", "Bearer "+jsjson.ToString(access))
		req.Header.Set("client-id", s.o.ClientID())
		if body != nil {
			req.Header.Set("content-type", "application/json")
		}
		return oauth.Fetch(ctx, s.o.Client, req, s.o.Timeout)
	})
}

// rateLimited holds sends until Ratelimit-Reset. No header: the Helix bucket
// is counted per minute, so wait one out.
func (s *Sender) rateLimited(r oauth.Response) server.SendResult {
	v := r.Header.Values("Ratelimit-Reset")
	reset := jsjson.ToNumber(strings.Join(v, ", "), true) * 1000
	if len(v) == 0 {
		reset = 0 // headers.get gives null: Number(null) is 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now := s.now(); reset > now {
		s.blockedUntil = reset
	} else {
		s.blockedUntil = now + 60000
	}
	return s.blocked()
}

// blocked runs with s.mu held.
func (s *Sender) blocked() server.SendResult {
	retry := math.Max(1, math.Ceil((s.blockedUntil-s.now())/1000))
	return server.SendResult{Code: "rate_limited", Reason: "Twitch rate limit hit — holding off.", RetryAfter: int(retry)}
}

// ---------- send ----------

// Send posts text to the target's chat as the signed-in user.
func (s *Sender) Send(ctx context.Context, text string, target targets.Target) (server.SendResult, error) {
	s.mu.Lock()
	if s.now() < s.blockedUntil {
		r := s.blocked()
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()
	if utf8.RuneCountInString(text) > MaxMessage {
		return failure("too_long", fmt.Sprintf("Twitch messages are %d characters at most.", MaxMessage)), nil
	}
	if target.Channel == nil || *target.Channel == "" {
		return failure("no_target", "No Twitch chat is connected in the panel."), nil
	}
	r, err := s.send(ctx, text, *target.Channel)
	if err != nil {
		if errors.Is(err, oauth.ErrSignedOut) {
			reason := s.session.Problem()
			if reason == "" {
				reason = "Not signed in to Twitch."
			}
			return failure("not_signed_in", reason), nil
		}
		return failure("unreachable", "Couldn't reach Twitch."), nil
	}
	return r, nil
}

func (s *Sender) expired() (server.SendResult, error) {
	if err := s.session.Expire(); err != nil {
		return server.SendResult{}, err
	}
	return failure("not_signed_in", Expired), nil
}

func (s *Sender) send(ctx context.Context, text, channel string) (server.SendResult, error) {
	s.mu.Lock()
	broadcasterID := s.broadcasterIDs[channel]
	s.mu.Unlock()
	if broadcasterID == "" {
		r, err := s.helix(ctx, http.MethodGet, "/helix/users?login="+oauth.EncodeURIComponent(channel), nil)
		if err != nil {
			return server.SendResult{}, err
		}
		if r.Status == 429 {
			return s.rateLimited(r), nil
		}
		if r.Status == 401 {
			return s.expired()
		}
		u := first(r.JSON)
		if r.Status != 200 {
			return failure("lookup_failed", fmt.Sprintf("Twitch couldn't look up %s: %s", channel, str(r.JSON, "message", fmt.Sprintf("HTTP %d", r.Status)))), nil
		}
		if !jsjson.Truthy(u) || !truthy(u, "id") {
			return failure("no_channel", fmt.Sprintf("Twitch has no channel called %s.", channel)), nil
		}
		broadcasterID = jsjson.ToString(get(u, "id"))
		s.mu.Lock()
		s.broadcasterIDs[channel] = broadcasterID
		s.mu.Unlock()
	}
	body := ok("broadcaster_id", broadcasterID)
	if me := s.o.Store.Get(section); me != nil {
		if id, has := me.Get("userId"); has {
			body.Set("sender_id", id)
		}
	}
	body.Set("message", text)
	r, err := s.helix(ctx, http.MethodPost, "/helix/chat/messages", body)
	if err != nil {
		return server.SendResult{}, err
	}
	j := r.Object()
	switch r.Status {
	case 200:
		d := first(j)
		if jsjson.Truthy(d) && truthy(d, "is_sent") {
			return server.SendResult{OK: true, ID: str(d, "message_id", "")}, nil
		}
		var dr any
		if jsjson.Truthy(d) {
			dr = get(d, "drop_reason")
		}
		return failure(str(dr, "code", "dropped"), str(dr, "message", "Twitch dropped the message.")), nil
	case 429:
		return s.rateLimited(r), nil
	case 401:
		return s.expired()
	case 422:
		return failure("too_long", str(j, "message", "The message is too large.")), nil
	case 403:
		return failure("forbidden", str(j, "message", "Twitch says this account can't chat there.")), nil
	}
	return failure("rejected", str(j, "message", fmt.Sprintf("Twitch answered HTTP %d.", r.Status))), nil
}
