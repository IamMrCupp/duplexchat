// Package youtubesend is sending to YouTube live chat (lib/youtube-send.js
// at 29a9d2c). Reading stays on the chat page's own interface in
// internal/youtube (free, no quota) and never moves here. This package makes
// three kinds of API call and no others:
//
//	channels.list?mine=true   once per sign-in, to learn which channel it is
//	videos.list               once per broadcast, to turn the video the reader
//	                          is already following into a live chat ID
//	liveChatMessages.insert   once per reply
//
// It never calls liveChatMessages.list. If the reader can't find the
// stream, the fix belongs in the reader.
//
// Sign-in is Google's installed-app flow: the user's own OAuth client, a
// loopback redirect back to this server, PKCE.
//
//	https://developers.google.com/youtube/v3/guides/auth/installed-apps
//
// The insert call accepts the youtube or the youtube.force-ssl scope;
// Google has no chat-only scope. Quota costs and the daily reset:
//
//	https://developers.google.com/youtube/v3/determine_quota_cost
package youtubesend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // America/Los_Angeles on Windows too
	"unicode/utf16"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/oauth"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

const (
	// Scope is the only scope asked for.
	Scope = "https://www.googleapis.com/auth/youtube.force-ssl"
	// MaxMessage is YouTube Help's 200 characters a live chat message; the
	// API reference gives no number of its own, so that's the one enforced,
	// before any quota is spent.
	//   https://support.google.com/youtube/answer/2524549
	MaxMessage = 200
	// Expired is what the pages are told when Google kills the sign-in
	// (invalid_grant: revoked, unused for six months, or a 7-day token from
	// a project still in Testing).
	Expired = "Google sign-in expired — sign in again."

	signinWindowMs = 10 * 60 * 1000
	section        = "youtube"
)

// Quota units, from the quota cost table.
const (
	CostInsert       = 20
	CostVideosList   = 1
	CostChannelsList = 1
	CostDailyDefault = 10000
)

var clientIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{10,200}$`)

// Endpoints are Google's, injectable for tests.
type Endpoints struct {
	Auth, Token, Revoke, API string
}

// DefaultEndpoints are the real ones.
var DefaultEndpoints = Endpoints{
	Auth:   "https://accounts.google.com/o/oauth2/v2/auth",
	Token:  "https://oauth2.googleapis.com/token",
	Revoke: "https://oauth2.googleapis.com/revoke",
	API:    "https://www.googleapis.com/youtube/v3",
}

// Options are the sender's seams.
type Options struct {
	Store *secrets.Store
	// OnAuthChange is called after a sign-in lands or a sign-in ends. A
	// panic in it is contained.
	OnAuthChange func()

	Endpoints Endpoints // zero → DefaultEndpoints
	Now       func() time.Time
	Timeout   time.Duration // one call; 0 → oauth.DefaultTimeout
	Client    *http.Client
}

// Sender sends to YouTube live chat.
type Sender struct {
	o       Options
	ep      Endpoints
	session *oauth.Session

	mu                sync.Mutex
	pending           *pending // sign-in in progress
	signinError       string
	chatIDs           map[string]any // videoId → liveChatId
	quotaBlockedUntil float64
	usedDay           string
	usedUnits         int
}

type pending struct {
	state, verifier, redirectURI string
	expiresAt                    float64
}

// New returns a sender.
func New(o Options) *Sender {
	ep := o.Endpoints
	if ep == (Endpoints{}) {
		ep = DefaultEndpoints
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Sender{o: o, ep: ep, chatIDs: map[string]any{}}
	s.session = &oauth.Session{
		Store: o.Store, Section: section, Now: o.Now, Client: o.Client, Timeout: o.Timeout,
		ExpiredMessage: Expired,
		Keep:           clientOnly,
		RefreshRequest: func(y *jsjson.Object) (string, []oauth.Param) {
			params := []oauth.Param{param("client_id", value(y, "clientId"))}
			if truthy(y, "clientSecret") {
				params = append(params, param("client_secret", value(y, "clientSecret")))
			}
			return ep.Token, append(params, param("grant_type", "refresh_token"), param("refresh_token", value(y, "refreshToken")))
		},
	}
	return s
}

func (s *Sender) now() float64 { return float64(s.o.Now().UnixMilli()) }

func param(k, v string) oauth.Param { return oauth.Param{Key: k, Value: v} }

func get(v any, k string) any {
	x, _ := jsjson.Get(v, k)
	return x
}

func truthy(v any, k string) bool { return jsjson.Truthy(get(v, k)) }

// or is `v[k] || fallback`.
func or(v any, k string, fallback any) any {
	if x := get(v, k); jsjson.Truthy(x) {
		return x
	}
	return fallback
}

// str is String(v[k] || fallback).
func str(v any, k, fallback string) string { return jsjson.ToString(or(v, k, fallback)) }

// value is what URLSearchParams makes of v[k]: String(v[k]), "undefined"
// when it's missing.
func value(v any, k string) string {
	x, ok := jsjson.Get(v, k)
	if !ok {
		return "undefined"
	}
	return jsjson.ToString(x)
}

// same is a === b for two possibly-missing JSON values.
func same(a any, aok bool, b any, bok bool) bool {
	if aok != bok {
		return false
	}
	switch a.(type) {
	case nil, bool, float64, string:
		return a == b
	}
	return !aok
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

// clientOnly is what outlives a sign-in: the client credentials.
func clientOnly(y *jsjson.Object) *jsjson.Object {
	if y == nil || !truthy(y, "clientId") {
		return nil
	}
	out := ok("clientId", get(y, "clientId"))
	if truthy(y, "clientSecret") {
		out.Set("clientSecret", get(y, "clientSecret"))
	}
	return out
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b64url(b)
}

// esc is the source's esc(): & < > " ' as &amp; &lt; &gt; &quot; &#39;.
// (html.EscapeString writes " as &#34;.)
var esc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace

// ---------- Pacific time ----------

var pacific = func() *time.Location {
	l, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}
	return l
}()

// MsUntilPacificMidnight is how long until Google's daily quota resets,
// read off the Pacific wall clock (so it's up to an hour off on the two days
// a year the clocks change, as in Node).
func MsUntilPacificMidnight(nowMs float64) float64 {
	h, m, sec := time.UnixMilli(int64(nowMs)).In(pacific).Clock()
	return float64(86400-(h*3600+m*60+sec)) * 1000
}

func pacificDay(nowMs float64) string {
	return time.UnixMilli(int64(nowMs)).In(pacific).Format("2006-01-02")
}

func inHoursMinutes(ms float64) string {
	m := int(math.Max(1, math.Floor(ms/60000+0.5))) // Math.round
	if m >= 60 {
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

// ---------- state the pages may see ----------

func redirectURI(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// Status is the sign-in as the dock shows it. Two channels on one Google
// account can share a title: the handle and the channel ID tell them apart,
// so both are given.
func (s *Sender) Status() server.SenderStatus {
	y := s.o.Store.Get(section)
	signedIn := truthy(y, "clientId") && truthy(y, "refreshToken")
	st := server.SenderStatus{Configured: truthy(y, "clientId"), SignedIn: signedIn}
	if !st.Configured {
		st.Detail = "Needs your own Google OAuth client."
	} else if p := s.session.Problem(); !signedIn && p != "" {
		st.Detail = p
	}
	if signedIn {
		var who []string
		if truthy(y, "channelTitle") {
			who = append(who, str(y, "channelTitle", ""))
		}
		if truthy(y, "handle") {
			who = append(who, "("+str(y, "handle", "")+")")
		}
		st.As = strings.Join(who, " ")
		st.ID = str(y, "channelId", "")
	}
	return st
}

// MaxLength is YouTube's message limit.
func (s *Sender) MaxLength() int { return MaxMessage }

// spend counts units against today's estimate. Runs with s.mu held.
func (s *Sender) spend(units int) {
	if day := pacificDay(s.now()); s.usedDay != day {
		s.usedDay, s.usedUnits = day, 0
	}
	s.usedUnits += units
}

// SetupState is what /setup shows: never a token, the secret or the
// verifier.
func (s *Sender) SetupState(port int) *jsjson.Object {
	y := s.o.Store.Get(section)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if day := pacificDay(now); s.usedDay != day {
		s.usedDay, s.usedUnits = day, 0
	}
	channelID, handle := any(""), any("")
	if truthy(y, "refreshToken") {
		channelID, handle = or(y, "channelId", ""), or(y, "handle", "")
	}
	uri := ""
	if port != 0 {
		uri = redirectURI(port)
	}
	return ok("clientId", or(y, "clientId", ""), "hasSecret", truthy(y, "clientSecret"),
		"channelId", channelID, "handle", handle, "redirectUri", uri, "scope", Scope,
		"signinError", s.signinError,
		"quota", ok("perSend", CostInsert, "perBroadcastLookup", CostVideosList, "dailyDefault", CostDailyDefault,
			"usedTodayEstimate", s.usedUnits, "resetsInMs", MsUntilPacificMidnight(now), "exhausted", now < s.quotaBlockedUntil))
}

// ---------- setup actions (POST /api/setup/youtube/<action>) ----------

// reset is what anything that ends or replaces the sign-in does first, so a
// refresh still on the wire can't write the old tokens (or the old client)
// back over what comes next. Runs with s.mu held.
func (s *Sender) reset() {
	s.session.Invalidate()
	s.pending = nil
	s.session.SetProblem("")
	s.signinError = ""
	s.chatIDs = map[string]any{}
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

// Setup runs one of /setup's actions; nil for one it doesn't have. (The
// viewer-source switch comes with #14.)
func (s *Sender) Setup(ctx context.Context, action string, body *jsjson.Object, port int) (*server.SetupResult, error) {
	switch action {
	case "client":
		return s.setClient(body)
	case "signin":
		return s.startSignin(port), nil
	case "signout":
		return s.signOut(ctx)
	}
	return nil, nil
}

func (s *Sender) setClient(body *jsjson.Object) (*server.SetupResult, error) {
	clientID := jsjson.Trim(str(body, "clientId", ""))
	given := jsjson.Trim(str(body, "clientSecret", ""))
	s.mu.Lock()
	y := s.o.Store.Get(section)
	if clientID == "" {
		s.reset()
		err := s.o.Store.Set(section, nil)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		s.authChanged()
		return answer(200, ok("ok", true)), nil
	}
	if !clientIDRE.MatchString(clientID) {
		s.mu.Unlock()
		return answer(400, ok("error", "That doesn't look like a Google OAuth client ID.", "code", "bad_client_id")), nil
	}
	if len(utf16.Encode([]rune(given))) > 200 {
		s.mu.Unlock()
		return answer(400, ok("error", "That client secret is too long to be real.", "code", "bad_client_secret")), nil
	}
	old, has := jsjson.Get(y, "clientId")
	isSame := same(clientID, true, old, has)
	// an empty secret field on the same client means "keep what you have"
	var secret any = given
	if given == "" {
		secret = ""
		if isSame {
			secret = or(y, "clientSecret", "")
		}
	}
	if isSame && same(secret, true, or(y, "clientSecret", ""), true) {
		s.mu.Unlock()
		return answer(200, ok("ok", true)), nil
	}
	s.reset()
	next := ok("clientId", clientID) // tokens belong to the old client
	if jsjson.Truthy(secret) {
		next.Set("clientSecret", secret)
	}
	err := s.o.Store.Set(section, next)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.authChanged()
	return answer(200, ok("ok", true)), nil
}

func (s *Sender) startSignin(port int) *server.SetupResult {
	y := s.o.Store.Get(section)
	if !truthy(y, "clientId") {
		return answer(400, ok("error", "Paste your Google OAuth client first.", "code", "no_client_id"))
	}
	p := &pending{state: random(24), verifier: random(48), redirectURI: redirectURI(port), expiresAt: s.now() + signinWindowMs}
	challenge := sha256.Sum256([]byte(p.verifier))
	s.mu.Lock()
	s.pending = p
	s.signinError = ""
	s.mu.Unlock()
	q := oauth.Form([]oauth.Param{
		param("client_id", value(y, "clientId")),
		param("redirect_uri", p.redirectURI),
		param("response_type", "code"),
		param("scope", Scope),
		param("state", p.state),
		param("code_challenge", b64url(challenge[:])),
		param("code_challenge_method", "S256"),
		param("access_type", "offline"),
		param("prompt", "consent"),
	})
	return answer(200, ok("url", s.ep.Auth+"?"+q))
}

// ---------- the browser coming back from Google ----------

// CallbackMatches claims GET /?state=…&code=… (or &error=…).
func (s *Sender) CallbackMatches(r *http.Request) bool {
	q := oauth.ParseQuery(r.URL.RawQuery)
	return r.Method == http.MethodGet && r.URL.Path == "/" && q.Has("state") && (q.Has("code") || q.Has("error"))
}

func page(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title>`+
		`<body style="background:#161310;color:#f0e8da;font:15px/1.5 system-ui,sans-serif;max-width:560px;margin:60px auto;padding:0 20px">`+
		`<h1 style="font-size:20px">%s</h1><p>%s</p><p><a style="color:#ffb454" href="/setup">Back to setup</a></p>`,
		esc(title), esc(title), esc(message))
}

// Callback is where the browser lands from Google. The state value is what
// ties the request to a sign-in this server started; it's single-use.
func (s *Sender) Callback(w http.ResponseWriter, r *http.Request) error {
	q := oauth.ParseQuery(r.URL.RawQuery)
	bad := func(why string) error {
		s.mu.Lock()
		s.signinError = why
		s.mu.Unlock()
		page(w, 400, "Not signed in", why)
		return nil
	}
	s.mu.Lock()
	p := s.pending
	// constant time, and a length mismatch (a non-ASCII state has more
	// bytes than characters) is simply a mismatch
	if p == nil || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(p.state)) != 1 {
		s.mu.Unlock()
		page(w, 400, "Not signed in", "This isn't a sign-in this server started. Start again from the setup page.")
		return nil
	}
	s.pending = nil
	s.mu.Unlock()
	if s.now() > p.expiresAt {
		return bad("That sign-in took too long. Start again.")
	}
	if e, has := q.Lookup("error"); has {
		if e == "access_denied" {
			return bad("Google sign-in was cancelled.")
		}
		return bad("Google refused the sign-in.")
	}
	y := s.o.Store.Get(section)
	params := []oauth.Param{param("client_id", value(y, "clientId"))}
	if truthy(y, "clientSecret") {
		params = append(params, param("client_secret", value(y, "clientSecret")))
	}
	code := "null" // URLSearchParams#get gives null for a missing name
	if c, has := q.Lookup("code"); has {
		code = c
	}
	params = append(params, param("code", code), param("code_verifier", p.verifier),
		param("grant_type", "authorization_code"), param("redirect_uri", p.redirectURI))
	tok, err := oauth.PostForm(context.Background(), s.o.Client, s.ep.Token, params, s.o.Timeout)
	if err != nil {
		return err
	}
	j := tok.Object()
	if tok.Status != 200 || !truthy(j, "access_token") {
		why := jsjson.ToString(or(j, "error_description", or(j, "error", fmt.Sprintf("HTTP %d", tok.Status))))
		return bad("Google wouldn't finish the sign-in: " + why)
	}
	if !truthy(j, "refresh_token") {
		return bad("Google didn't return a refresh token, so the sign-in wouldn't last. Start again.")
	}
	req, err := http.NewRequest(http.MethodGet, s.ep.API+"/channels?part=snippet&mine=true", nil)
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+value(j, "access_token"))
	who, err := oauth.Fetch(context.Background(), s.o.Client, req, s.o.Timeout)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.spend(CostChannelsList)
	s.mu.Unlock()
	var ch any
	if items, isList := get(who.JSON, "items").([]any); isList && len(items) > 0 {
		ch = items[0]
	}
	if who.Status != 200 || !jsjson.Truthy(ch) {
		return bad("Signed in, but that Google account has no YouTube channel to chat as.")
	}
	s.mu.Lock()
	// the client was changed or removed while the browser was at Google
	before, hadBefore := jsjson.Get(y, "clientId")
	nowID, hasNow := jsjson.Get(s.o.Store.Get(section), "clientId")
	if !same(nowID, hasNow, before, hadBefore) {
		s.mu.Unlock()
		return bad("The Google client changed during sign-in. Start again.")
	}
	s.session.Invalidate()
	record := ok("clientId", before)
	if truthy(y, "clientSecret") {
		record.Set("clientSecret", get(y, "clientSecret"))
	}
	in := jsjson.ToNumber(j.Get("expires_in"))
	if !jsjson.Truthy(in) {
		in = 0
	}
	snippet := get(ch, "snippet")
	record.Set("accessToken", get(j, "access_token")).Set("refreshToken", get(j, "refresh_token")).
		Set("expiresAt", s.now()+in*1000).Set("channelId", or(ch, "id", "")).
		Set("channelTitle", or(snippet, "title", ""))
	// snippet.customUrl is "the channel's custom URL"; the reference doesn't
	// promise a handle there, so it's kept only when it looks like one.
	if custom := or(snippet, "customUrl", ""); isHandle(jsjson.ToString(custom)) {
		record.Set("handle", custom)
	}
	err = s.o.Store.Set(section, record)
	if err == nil {
		s.session.SetProblem("")
		s.signinError = ""
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.authChanged()
	w.Header().Set("Location", "/setup")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusFound)
	return nil
}

// isHandle is /^@[^\s/]{1,60}$/.test(s): 1 to 60 UTF-16 units after the @,
// none of them white space or a slash.
func isHandle(s string) bool {
	rest, found := strings.CutPrefix(s, "@")
	if !found || rest == "" {
		return false
	}
	units := 0
	for _, r := range rest {
		if r == '/' || jsjson.IsSpace(r) || r == utf8.RuneError {
			return false
		}
		units += utf16.RuneLen(r)
	}
	return units <= 60
}

func (s *Sender) signOut(ctx context.Context) (*server.SetupResult, error) {
	s.mu.Lock()
	y := s.o.Store.Get(section)
	s.reset()
	err := s.o.Store.Set(section, clientOnly(y)) // keep the client, drop the tokens
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.authChanged()
	if truthy(y, "refreshToken") {
		// best effort: the local copy is gone either way
		oauth.PostForm(ctx, s.o.Client, s.ep.Revoke, []oauth.Param{param("token", value(y, "refreshToken"))}, s.o.Timeout)
	}
	return answer(200, ok("ok", true)), nil
}

// ---------- the API ----------

// api is an authorised call: one refresh and one retry on a 401. Every
// attempt is counted against the quota estimate.
func (s *Sender) api(ctx context.Context, method, pathAndQuery string, body *jsjson.Object, cost int) (oauth.Response, error) {
	return s.session.Authorized(ctx, func(y *jsjson.Object) (oauth.Response, error) {
		s.mu.Lock()
		s.spend(cost)
		s.mu.Unlock()
		var payload []byte
		if body != nil {
			payload = jsjson.Stringify(body, "")
		}
		req, err := http.NewRequest(method, s.ep.API+pathAndQuery, bytes.NewReader(payload))
		if err != nil {
			return oauth.Response{}, err
		}
		req.Header.Set("authorization", "Bearer "+value(y, "accessToken"))
		if body != nil {
			req.Header.Set("content-type", "application/json")
		}
		return oauth.Fetch(ctx, s.o.Client, req, s.o.Timeout)
	})
}

// quotaBlocked runs with s.mu held.
func (s *Sender) quotaBlocked() server.SendResult {
	ms := math.Max(0, s.quotaBlockedUntil-s.now())
	return server.SendResult{
		Code:       "quota_exhausted",
		Reason:     "YouTube API quota is used up for today — it resets at midnight Pacific, in about " + inHoursMinutes(ms) + ".",
		RetryAfter: int(math.Ceil(ms / 1000)),
	}
}

// refusal is Google's error envelope as a failure in Google's words.
func (s *Sender) refusal(r oauth.Response, videoID string) (server.SendResult, error) {
	e := get(r.JSON, "error")
	if !jsjson.Truthy(e) {
		e = nil
	}
	var first any
	if list, isList := get(e, "errors").([]any); isList && len(list) > 0 && jsjson.Truthy(list[0]) {
		first = list[0]
	}
	reason := or(first, "reason", "")
	message := jsjson.ToString(or(first, "message", or(e, "message", fmt.Sprintf("YouTube answered HTTP %d.", r.Status))))
	if reason == "quotaExceeded" || reason == "dailyLimitExceeded" {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.quotaBlockedUntil = s.now() + MsUntilPacificMidnight(s.now())
		return s.quotaBlocked(), nil
	}
	if r.Status == 401 {
		if err := s.session.Expire(); err != nil {
			return server.SendResult{}, err
		}
		return failure("not_signed_in", Expired), nil
	}
	// the chat we cached is gone: look it up fresh next time
	if reason == "liveChatEnded" || reason == "liveChatNotFound" {
		s.mu.Lock()
		delete(s.chatIDs, videoID)
		s.mu.Unlock()
	}
	code := "rejected"
	if jsjson.Truthy(reason) {
		code = jsjson.ToString(reason)
	}
	return failure(code, message), nil
}

// ---------- send ----------

// Send posts text to the live chat of the video the reader follows.
func (s *Sender) Send(ctx context.Context, text string, target targets.Target) (server.SendResult, error) {
	s.mu.Lock()
	if s.now() < s.quotaBlockedUntil {
		r := s.quotaBlocked()
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()
	if utf8.RuneCountInString(text) > MaxMessage {
		return failure("too_long", fmt.Sprintf("YouTube live chat messages are %d characters at most.", MaxMessage)), nil
	}
	if target.VideoID == nil || *target.VideoID == "" {
		return failure("not_live", "The reader hasn't found a live broadcast yet, so there's no chat to reply in."), nil
	}
	r, err := s.send(ctx, text, *target.VideoID)
	if err != nil {
		if errors.Is(err, oauth.ErrSignedOut) {
			reason := s.session.Problem()
			if reason == "" {
				reason = "Not signed in to YouTube."
			}
			return failure("not_signed_in", reason), nil
		}
		return failure("unreachable", "Couldn't reach YouTube."), nil
	}
	return r, nil
}

func (s *Sender) send(ctx context.Context, text, videoID string) (server.SendResult, error) {
	s.mu.Lock()
	chatID := s.chatIDs[videoID]
	s.mu.Unlock()
	if !jsjson.Truthy(chatID) {
		r, err := s.api(ctx, http.MethodGet, "/videos?part=liveStreamingDetails&id="+oauth.EncodeURIComponent(videoID), nil, CostVideosList)
		if err != nil {
			return server.SendResult{}, err
		}
		if r.Status != 200 {
			return s.refusal(r, videoID)
		}
		var v any
		if items, isList := get(r.JSON, "items").([]any); isList && len(items) > 0 {
			v = items[0]
		}
		chatID = get(get(v, "liveStreamingDetails"), "activeLiveChatId")
		if !jsjson.Truthy(chatID) {
			return failure("not_live", "That broadcast has no active live chat — it isn't live yet, or it has ended."), nil
		}
		s.mu.Lock()
		s.chatIDs[videoID] = chatID
		s.mu.Unlock()
	}
	body := ok("snippet", ok("liveChatId", chatID, "type", "textMessageEvent", "textMessageDetails", ok("messageText", text)))
	r, err := s.api(ctx, http.MethodPost, "/liveChat/messages?part=snippet", body, CostInsert)
	if err != nil {
		return server.SendResult{}, err
	}
	if r.Status == 200 {
		return server.SendResult{OK: true, ID: str(r.JSON, "id", "")}, nil
	}
	return s.refusal(r, videoID)
}
