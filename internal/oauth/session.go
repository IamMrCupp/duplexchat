package oauth

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
)

// RefreshMargin is how close to expiry an access token gets refreshed.
const RefreshMargin = 5 * time.Minute

// Session is one platform's sign-in, as stored in one section of the
// secrets file.
type Session struct {
	Store   *secrets.Store
	Section string // its key in the secrets file ("twitch", "youtube")
	// RefreshRequest gives the refresh grant for the stored tokens.
	RefreshRequest func(t *jsjson.Object) (url string, params []Param)
	// Keep is what survives losing the sign-in (the client credentials), or
	// nil when nothing does. A nil Keep keeps nothing.
	Keep func(t *jsjson.Object) *jsjson.Object
	// ExpiredMessage is what the pages are told when the platform kills the sign-in.
	ExpiredMessage string
	Now            func() time.Time
	Client         *http.Client
	Timeout        time.Duration

	mu         sync.Mutex
	problem    string  // why the last sign-in stopped working
	generation int     // bumped by anything that ends or replaces the sign-in
	refreshing *flight // single-flight refresh
}

type flight struct {
	done chan struct{}
	t    *jsjson.Object
	err  error
}

func (s *Session) now() float64 {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return float64(now().UnixMilli())
}

// Problem is why the last sign-in stopped working, or "".
func (s *Session) Problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problem
}

// SetProblem replaces it ("" once a sign-in lands).
func (s *Session) SetProblem(p string) {
	s.mu.Lock()
	s.problem = p
	s.mu.Unlock()
}

// Read is the stored section, from disk.
func (s *Session) Read() *jsjson.Object { return s.Store.Get(s.Section) }

// Write replaces the stored section; nil removes it.
func (s *Session) Write(v *jsjson.Object) error { return s.Store.Set(s.Section, v) }

// Invalidate is called before a sign-out, a client change or a fresh
// sign-in writes the section. A refresh already on the wire belongs to the
// old sign-in: when its answer arrives it must not be written back over
// what replaced it.
func (s *Session) Invalidate() {
	s.mu.Lock()
	s.generation++
	s.refreshing = nil
	s.mu.Unlock()
}

// Expire is the platform saying the sign-in is dead.
func (s *Session) Expire() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expire()
}

// expire runs with s.mu held.
func (s *Session) expire() error {
	var next *jsjson.Object
	if cur := s.Read(); cur != nil && s.Keep != nil {
		next = s.Keep(cur)
	}
	if err := s.Write(next); err != nil {
		return err
	}
	s.problem = s.ExpiredMessage
	return nil
}

// Refresh spends the refresh token. Callers that arrive while one is on the
// wire share its answer.
func (s *Session) Refresh(ctx context.Context) (*jsjson.Object, error) {
	s.mu.Lock()
	f := s.refreshing
	if f == nil {
		f = &flight{done: make(chan struct{})}
		s.refreshing = f
		generation := s.generation
		go func() {
			f.t, f.err = s.refresh(generation)
			s.mu.Lock()
			if s.refreshing == f {
				s.refreshing = nil
			}
			s.mu.Unlock()
			close(f.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.t, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Session) refresh(generation int) (*jsjson.Object, error) {
	t := s.Read() // from disk, not memory: another process may have rotated it
	if t == nil || !truthy(t, "refreshToken") {
		return nil, ErrSignedOut
	}
	url, params := s.RefreshRequest(t)
	r, err := PostForm(context.Background(), s.Client, url, params, s.Timeout)
	if err != nil {
		return nil, err
	}
	// From here to the end is one step, as it is on Node's single thread: a
	// sign-out lands wholly before it or wholly after it.
	s.mu.Lock()
	defer s.mu.Unlock()
	// signed out, or a different client pasted in, while this was in flight
	if generation != s.generation {
		return nil, ErrSignedOut
	}
	j := r.Object()
	if r.Status == 200 && truthy(j, "access_token") {
		next := t.Copy()
		access, _ := j.Get("access_token")
		expiresIn, _ := j.Get("expires_in")
		in := number(expiresIn, j.Has("expires_in"))
		if math.IsNaN(in) { // Number(expires_in) || 0
			in = 0
		}
		next.Set("accessToken", access)
		next.Set("expiresAt", s.now()+in*1000)
		// Twitch rotates the refresh token on every use; Google usually keeps it.
		if truthy(j, "refresh_token") {
			rt, _ := j.Get("refresh_token")
			next.Set("refreshToken", rt)
		}
		// a single-use refresh token is spent: persist it before anything else
		if err := s.Write(next); err != nil {
			return nil, err
		}
		return next, nil
	}
	if r.Status == 400 || r.Status == 401 {
		// Refused. But if the file now holds a different refresh token,
		// another process spent ours and saved its replacement: use that.
		cur := s.Read()
		if cur != nil && truthy(cur, "refreshToken") && !same(cur, t, "refreshToken") {
			return cur, nil
		}
		if err := s.expire(); err != nil {
			return nil, err
		}
		return nil, ErrSignedOut
	}
	return nil, fmt.Errorf("refresh failed: HTTP %d", r.Status)
}

// Tokens are the stored tokens, refreshed first when forced, when there's
// no access token, or within RefreshMargin of expiresAt. A missing
// expiresAt never forces one.
func (s *Session) Tokens(ctx context.Context, force bool) (*jsjson.Object, error) {
	t := s.Read()
	if t == nil || !truthy(t, "refreshToken") {
		return nil, ErrSignedOut
	}
	exp, _ := t.Get("expiresAt")
	left := number(exp, t.Has("expiresAt")) - s.now()
	if force || !truthy(t, "accessToken") || left < float64(RefreshMargin.Milliseconds()) {
		return s.Refresh(ctx)
	}
	return t, nil
}

// Authorized makes an authorised call. A 401 gets exactly one refresh and
// one retry (the documented answer to a dead access token, not a blind
// retry), and the second answer comes back as it is.
func (s *Session) Authorized(ctx context.Context, call func(t *jsjson.Object) (Response, error)) (Response, error) {
	t, err := s.Tokens(ctx, false)
	if err != nil {
		return Response{}, err
	}
	for attempt := 0; ; attempt++ {
		r, err := call(t)
		if err != nil || r.Status != 401 || attempt > 0 {
			return r, err
		}
		if t, err = s.Tokens(ctx, true); err != nil {
			return Response{}, err
		}
	}
}

// truthy is JavaScript truthiness of o[k].
func truthy(o *jsjson.Object, k string) bool {
	v, ok := o.Get(k)
	if !ok {
		return false
	}
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case string:
		return t != ""
	}
	return true
}

// same is o[k] === p[k] for the values a secrets file holds.
func same(o, p *jsjson.Object, k string) bool {
	a, _ := o.Get(k)
	b, _ := p.Get(k)
	switch a.(type) {
	case string, float64, bool, nil:
		return a == b
	}
	return false // objects and arrays are never the same object
}

// number is Number(v) for a JSON value; present false is undefined (NaN).
func number(v any, present bool) float64 {
	if !present {
		return math.NaN()
	}
	switch t := v.(type) {
	case nil:
		return 0
	case bool:
		if t {
			return 1
		}
		return 0
	case float64:
		return t
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if strings.ContainsAny(s, "_xXpPnN") && s != "Infinity" && s != "-Infinity" && s != "+Infinity" {
			return math.NaN()
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}
