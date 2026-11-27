package oauth

// The session cases multichat runs through its senders
// (test/twitch-send.test.js and test/youtube-send.test.js at 29a9d2c),
// rewritten against a fake token endpoint.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
)

const clientID = "fakeclientid0123456789abcdefgh"

// fakeToken has Twitch's bookkeeping: refresh tokens are single-use, and
// every refresh issues a new access token and a new refresh token.
type fakeToken struct {
	mu      sync.Mutex
	n       int
	refresh string
	access  string
	bodies  []string
	status  int           // forced answer, when not 0
	hold    chan struct{} // when set, answers wait for it to close
	seen    chan struct{} // closed when the first refresh arrives
	seenOne sync.Once
	srv     *httptest.Server
}

func newFake(t *testing.T) *fakeToken {
	f := &fakeToken{refresh: "FAKE_REFRESH_0", access: "FAKE_ACCESS_0", seen: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		hold := f.hold
		f.mu.Unlock()
		f.seenOne.Do(func() { close(f.seen) })
		if hold != nil {
			<-hold
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if f.status != 0 {
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"status":500,"message":"down"}`)
			return
		}
		if form.Get("refresh_token") != f.refresh {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"status":400,"message":"Invalid refresh token"}`)
			return
		}
		f.n++
		f.access = fmt.Sprintf("FAKE_ACCESS_%d", f.n)
		f.refresh = fmt.Sprintf("FAKE_REFRESH_%d", f.n)
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":14400,"scope":["user:write:chat"],"token_type":"bearer"}`, f.access, f.refresh)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeToken) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const expired = "Twitch sign-in expired — sign in again."

func session(f *fakeToken, file string, c *clock) *Session {
	return &Session{
		Store:   secrets.New(file, secrets.Options{}),
		Section: "twitch",
		RefreshRequest: func(t *jsjson.Object) (string, []Param) {
			return f.srv.URL + "/oauth2/token", []Param{{"client_id", clientID}, {"grant_type", "refresh_token"}, {"refresh_token", t.String("refreshToken")}}
		},
		ExpiredMessage: expired,
		Now:            c.now,
	}
}

// signedIn writes a sign-in that has `left` of life and returns its session.
func signedIn(t *testing.T, f *fakeToken, left time.Duration) (*Session, string, *clock) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "secrets.json")
	c := &clock{t: time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)}
	s := session(f, file, c)
	rec := jsjson.NewObject().
		Set("accessToken", f.access).Set("refreshToken", f.refresh).
		Set("expiresAt", float64(c.now().Add(left).UnixMilli())).
		Set("userId", "1001").Set("login", "examplechannel")
	if err := s.Write(rec); err != nil {
		t.Fatal(err)
	}
	return s, file, c
}

func onDisk(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestRefreshBeforeExpiryOnceForConcurrentCallersRotatedTokenPersisted(t *testing.T) {
	f := newFake(t)
	s, file, c := signedIn(t, f, 2*time.Minute) // two minutes of life left
	var wg sync.WaitGroup
	got := make([]*jsjson.Object, 3)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := s.Tokens(context.Background(), false)
			if err != nil {
				t.Error(err)
			}
			got[i] = tok
		}(i)
	}
	wg.Wait()
	if f.count() != 1 {
		t.Fatalf("%d refreshes, want 1", f.count())
	}
	if want := "client_id=" + clientID + "&grant_type=refresh_token&refresh_token=FAKE_REFRESH_0"; f.bodies[0] != want {
		t.Fatalf("refresh body %q", f.bodies[0])
	}
	for _, tok := range got {
		if tok == nil || tok.String("accessToken") != "FAKE_ACCESS_1" {
			t.Fatalf("caller got %v", tok)
		}
	}
	// the rotated refresh token is on disk, the record's other fields and
	// key order kept, expiresAt from expires_in
	want := fmt.Sprintf("{\n  \"twitch\": {\n    \"accessToken\": \"FAKE_ACCESS_1\",\n    \"refreshToken\": \"FAKE_REFRESH_1\",\n    \"expiresAt\": %d,\n    \"userId\": \"1001\",\n    \"login\": \"examplechannel\"\n  }\n}",
		c.now().Add(14400*time.Second).UnixMilli())
	if d := onDisk(t, file); d != want {
		t.Fatalf("file\n%s\nwant\n%s", d, want)
	}
	// a healthy token isn't refreshed again
	if _, err := s.Tokens(context.Background(), false); err != nil || f.count() != 1 {
		t.Fatalf("refreshed again: %v, %d", err, f.count())
	}
}

func TestA401GetsOneRefreshAndOneRetry(t *testing.T) {
	f := newFake(t)
	s, _, _ := signedIn(t, f, time.Hour)
	f.mu.Lock()
	f.access = "FAKE_ACCESS_REVOKED_SERVER_SIDE" // our stored access token is now dead
	f.mu.Unlock()
	calls := 0
	call := func(tok *jsjson.Object) (Response, error) {
		calls++
		f.mu.Lock()
		defer f.mu.Unlock()
		if tok.String("accessToken") != f.access {
			return Response{Status: 401}, nil
		}
		return Response{Status: 200}, nil
	}
	r, err := s.Authorized(context.Background(), call)
	if err != nil || r.Status != 200 || calls != 2 || f.count() != 1 {
		t.Fatalf("status %d err %v calls %d refreshes %d", r.Status, err, calls, f.count())
	}
	// a second 401 comes back as it is: no second refresh, no third call
	calls = 0
	r, err = s.Authorized(context.Background(), func(*jsjson.Object) (Response, error) { calls++; return Response{Status: 401}, nil })
	if err != nil || r.Status != 401 || calls != 2 || f.count() != 2 {
		t.Fatalf("status %d err %v calls %d refreshes %d", r.Status, err, calls, f.count())
	}
}

func TestADeadRefreshTokenSignsOutAndSaysSo(t *testing.T) {
	f := newFake(t)
	s, file, c := signedIn(t, f, time.Hour)
	f.mu.Lock()
	f.refresh = "FAKE_REFRESH_ROTATED_ELSEWHERE"
	f.mu.Unlock()
	c.add(4 * time.Hour)
	if _, err := s.Tokens(context.Background(), false); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("err = %v", err)
	}
	if s.Problem() != expired {
		t.Fatalf("problem = %q", s.Problem())
	}
	if d := onDisk(t, file); d != "{}" {
		t.Fatalf("file = %q", d)
	}
	if _, err := s.Tokens(context.Background(), false); !errors.Is(err, ErrSignedOut) || f.count() != 1 {
		t.Fatalf("signed-out session asked again: %v, %d", err, f.count())
	}
}

// YouTube keeps the client when the sign-in dies.
func TestADeadRefreshTokenKeepsWhatKeepSays(t *testing.T) {
	f := newFake(t)
	s, file, c := signedIn(t, f, time.Hour)
	s.Keep = func(t *jsjson.Object) *jsjson.Object {
		return jsjson.NewObject().Set("login", t.String("login"))
	}
	f.mu.Lock()
	f.refresh = "elsewhere"
	f.mu.Unlock()
	c.add(4 * time.Hour)
	s.Tokens(context.Background(), false)
	if d := onDisk(t, file); d != "{\n  \"twitch\": {\n    \"login\": \"examplechannel\"\n  }\n}" {
		t.Fatalf("file = %q", d)
	}
}

func TestARefreshInFlightWhenISignOutDoesNotSignMeBackIn(t *testing.T) {
	f := newFake(t)
	s, file, _ := signedIn(t, f, time.Minute) // close to expiry: the next call refreshes first
	f.hold = make(chan struct{})              // hold the answer until after the sign-out
	done := make(chan error, 1)
	go func() { _, err := s.Tokens(context.Background(), false); done <- err }()
	<-f.seen // the refresh is on the wire
	s.Invalidate()
	if err := s.Write(nil); err != nil {
		t.Fatal(err)
	}
	close(f.hold)
	if err := <-done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("err = %v", err)
	}
	if d := onDisk(t, file); d != "{}" {
		t.Fatalf("the late answer was written: %q", d)
	}
}

// A client change writes a new section mid-refresh: the old sign-in mustn't
// come back over it.
func TestARefreshInFlightWhenTheClientChangesDoesNotRestoreTheOldSignIn(t *testing.T) {
	f := newFake(t)
	s, file, _ := signedIn(t, f, time.Minute)
	f.hold = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := s.Tokens(context.Background(), false); done <- err }()
	<-f.seen
	s.Invalidate()
	s.Write(jsjson.NewObject().Set("clientId", "anotherfakeclientid0123456789"))
	close(f.hold)
	if err := <-done; err == nil {
		t.Fatal("the refresh succeeded")
	}
	if d := onDisk(t, file); d != "{\n  \"twitch\": {\n    \"clientId\": \"anotherfakeclientid0123456789\"\n  }\n}" {
		t.Fatalf("file = %q", d)
	}
	// and a refresh after the change starts afresh rather than sharing the dropped one
	if _, err := s.Refresh(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("err = %v", err)
	}
}

func TestTwoCopiesOnOneFolderUseTheRotatedRefreshToken(t *testing.T) {
	f := newFake(t)
	a, file, c := signedIn(t, f, time.Hour)
	b := session(f, file, c) // a second process: its own store, same file
	c.add(4*time.Hour - time.Minute)
	if _, err := a.Tokens(context.Background(), false); err != nil {
		t.Fatal(err) // a refreshes: the old refresh token is spent
	}
	tok, err := b.Tokens(context.Background(), false) // b reads the file and sees the new tokens
	if err != nil || tok.String("refreshToken") != "FAKE_REFRESH_1" || f.count() != 1 {
		t.Fatalf("b: %v %v, %d refreshes", tok, err, f.count())
	}

	// The worst case: both start a refresh with the same token. The loser
	// gets a 400, finds the winner's token on disk and carries on with it.
	c.add(4 * time.Hour)
	f.mu.Lock()
	f.hold = make(chan struct{})
	f.seen = make(chan struct{})
	f.seenOne = sync.Once{}
	f.mu.Unlock()
	lost := make(chan *jsjson.Object, 1)
	go func() {
		tok, err := a.Tokens(context.Background(), false)
		if err != nil {
			t.Error(err)
		}
		lost <- tok
	}()
	<-f.seen // a's refresh is on the wire with FAKE_REFRESH_1, held
	f.mu.Lock()
	hold := f.hold
	f.hold = nil // b's goes straight through
	f.mu.Unlock()
	won, err := b.Tokens(context.Background(), false)
	if err != nil || won.String("refreshToken") != "FAKE_REFRESH_2" {
		t.Fatalf("b: %v %v", won, err)
	}
	close(hold) // now a's answer: 400, the token it sent was spent
	got := <-lost
	if got == nil || got.String("refreshToken") != "FAKE_REFRESH_2" || got.String("accessToken") != "FAKE_ACCESS_2" {
		t.Fatalf("a adopted %v", got)
	}
	if a.Problem() != "" || b.Problem() != "" {
		t.Fatal("someone was signed out")
	}
	if tok := a.Read(); tok == nil || tok.String("refreshToken") != "FAKE_REFRESH_2" {
		t.Fatalf("on disk: %v", tok)
	}
}

func TestWhenARefreshIsDue(t *testing.T) {
	f := newFake(t)
	s, _, _ := signedIn(t, f, time.Hour)
	rec := s.Read()
	rec.Delete("expiresAt")
	s.Write(rec)
	if _, err := s.Tokens(context.Background(), false); err != nil || f.count() != 0 {
		t.Fatalf("a missing expiresAt refreshed: %v %d", err, f.count())
	}
	rec.Delete("accessToken")
	s.Write(rec)
	if _, err := s.Tokens(context.Background(), false); err != nil || f.count() != 1 {
		t.Fatalf("no access token didn't refresh: %v %d", err, f.count())
	}
	if _, err := s.Tokens(context.Background(), true); err != nil || f.count() != 2 {
		t.Fatalf("force didn't refresh: %v %d", err, f.count())
	}
	s.Write(nil)
	if _, err := s.Tokens(context.Background(), false); !errors.Is(err, ErrSignedOut) || f.count() != 2 {
		t.Fatalf("no sign-in: %v %d", err, f.count())
	}
}

func TestPlatformTroubleIsAnErrorAndKeepsTheTokens(t *testing.T) {
	f := newFake(t)
	s, file, _ := signedIn(t, f, time.Minute)
	before := onDisk(t, file)
	f.status = 500
	_, err := s.Tokens(context.Background(), false)
	if err == nil || err.Error() != "refresh failed: HTTP 500" {
		t.Fatalf("err = %v", err)
	}
	if onDisk(t, file) != before || s.Problem() != "" {
		t.Fatal("tokens changed")
	}
	f.srv.Close()
	if _, err := s.Tokens(context.Background(), false); err == nil || errors.Is(err, ErrSignedOut) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestForm(t *testing.T) {
	got := Form([]Param{{"a b", "*-._~"}, {"é", "&=+/!'()"}, {"redirect_uri", "http://127.0.0.1:3131/api/setup/youtube/callback"}})
	// new URLSearchParams([...]).toString() in Node
	want := "a+b=*-._%7E&%C3%A9=%26%3D%2B%2F%21%27%28%29&redirect_uri=http%3A%2F%2F127.0.0.1%3A3131%2Fapi%2Fsetup%2Fyoutube%2Fcallback"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestFetchReadsJSONOrNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Ratelimit-Reset", "123")
		switch r.URL.Path {
		case "/json":
			fmt.Fprint(w, "\ufeff{\"b\":1,\"a\":2}")
		case "/empty":
			w.WriteHeader(204)
		default:
			fmt.Fprint(w, "<html>")
		}
	}))
	defer srv.Close()
	for path, want := range map[string]string{"/json": `{"b":1,"a":2}`, "/empty": "null", "/html": "null"} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		r, err := Fetch(context.Background(), nil, req, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(jsjson.Stringify(r.JSON, "")); got != want || r.Header.Get("ratelimit-reset") != "123" {
			t.Errorf("%s: %s", path, got)
		}
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	req, _ := http.NewRequest("GET", slow.URL, nil)
	if _, err := Fetch(context.Background(), nil, req, 50*time.Millisecond); err == nil {
		t.Fatal("no timeout")
	}
}

func TestFail(t *testing.T) {
	got := string(jsjson.Stringify(Fail("rate_limited", "Slow down.", jsjson.NewObject().Set("retryAfter", 30)), ""))
	if want := `{"ok":false,"code":"rate_limited","reason":"Slow down.","retryAfter":30}`; got != want {
		t.Fatalf("got %s", got)
	}
}
