package youtubesend

// Beyond the source's cases: the parts of lib/youtube-send.js its tests
// don't reach, each pinned so a change to it fails here.

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/server"
)

// A new secret for the same client replaces the stored one and ends the
// sign-in (the tokens came from the old credentials).
func TestANewSecretForTheSameClient(t *testing.T) {
	c := setup(t, true)
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": "FAKE_NEW_SECRET"}, nil)
	if c.sender.Status().SignedIn || !reflect.DeepEqual(c.saved(t), map[string]any{"clientId": clientID, "clientSecret": "FAKE_NEW_SECRET"}) {
		t.Fatalf("saved %v", c.saved(t))
	}
}

// Removing the client cancels a sign-in the browser is still doing.
func TestRemovingTheClientCancelsAPendingSignIn(t *testing.T) {
	c := setup(t, false)
	auth := c.begin(t)
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": ""}, nil)
	if r := c.comeBack(t, nil, "state", auth.Query().Get("state"), "code", c.code()); r.status != 400 || c.g.count("POST /token") != 0 {
		t.Fatalf("%d, %d exchanges", r.status, c.g.count("POST /token"))
	}
	if c.saved(t) != nil {
		t.Fatalf("saved %v", c.saved(t))
	}
}

// Signing out forgets the looked-up chats.
func TestSignOutForgetsLookedUpChats(t *testing.T) {
	c := setup(t, true)
	send(t, c.sender, "x", target)
	c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil)
	c.signIn(t)
	send(t, c.sender, "y", target)
	if n := c.g.count("GET /youtube/v3/videos"); n != 2 {
		t.Fatalf("%d lookups", n)
	}
}

// No client: sign-in is refused before anything else.
func TestSignInWithNoClient(t *testing.T) {
	c := setup(t, false)
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": ""}, nil)
	r := c.app.post(t, "/api/setup/youtube/signin", map[string]any{}, nil)
	if r.status != 400 || r.json["code"] != "no_client_id" || r.json["error"] != "Paste your Google OAuth client first." {
		t.Fatalf("%d %v", r.status, r.json)
	}
	if got := c.sender.Status(); got != (server.SenderStatus{Detail: "Needs your own Google OAuth client."}) {
		t.Fatalf("%+v", got)
	}
}

// Only GET / with a state and a code or an error is the callback.
func TestWhatTheCallbackClaims(t *testing.T) {
	c := setup(t, false)
	c.begin(t)
	for _, r := range []struct{ method, path string }{
		{"POST", "/?state=x&code=y"}, {"GET", "/?state=x"}, {"GET", "/setup?state=x&code=y"}, {"GET", "/?code=y&error=z"},
	} {
		if got := c.app.request(t, r.method, r.path, "", nil); got.status != 404 {
			t.Errorf("%s %s: %d %s", r.method, r.path, got.status, got.text)
		}
	}
	// URLSearchParams, not url.ParseQuery: a ; is part of the value
	if got := c.app.request(t, "GET", "/?state=a;b&code=y", "", nil); got.status != 400 {
		t.Errorf("state with a semicolon: %d", got.status)
	}
	// an empty code is still a code, and an error with no value still an error
	if got := c.app.request(t, "GET", "/?state=x&code", "", nil); got.status != 400 {
		t.Errorf("bare code: %d", got.status)
	}
}

// A new sign-in landing while the old one's refresh is in flight wins, and
// the send caught in between says it isn't signed in, not that the old
// sign-in expired.
func TestASignInLandingDuringARefreshWins(t *testing.T) {
	c := setup(t, true)
	// an expired sign-in first, so there's a stale problem to clear
	c.g.state(func() { c.g.refresh = "FAKE_YT_REFRESH_GONE" })
	c.clock.add(3600 * time.Second)
	send(t, c.sender, "x", target)
	c.signIn(t)

	release, seen := c.holdRefresh()
	c.clock.add((3599 - 60) * time.Second)
	sending := make(chan server.SendResult)
	go func() { r, _ := c.sender.Send(context.Background(), "x", target); sending <- r }()
	<-seen
	c.signIn(t)
	latest := c.g.get(func() string { return c.g.access })
	release()
	if got := <-sending; got != (server.SendResult{Code: "not_signed_in", Reason: "Not signed in to YouTube."}) {
		t.Fatalf("%+v", got)
	}
	if got := c.saved(t)["accessToken"]; got != latest {
		t.Fatalf("stored %v, want the new sign-in's %s", got, latest)
	}
}

// An insert that's still a 401 after its one refresh is a dead sign-in.
func TestAnInsertStill401AfterTheRefreshSignsOut(t *testing.T) {
	c := setup(t, true)
	c.g.state(func() {
		c.g.insert = func(call) reply { return gErr(401, "authError", "Invalid Credentials") }
	})
	if got := send(t, c.sender, "x", target); got != (server.SendResult{Code: "not_signed_in", Reason: Expired}) {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(c.saved(t), map[string]any{"clientId": clientID, "clientSecret": clientSecret}) || len(c.g.refreshes()) != 1 {
		t.Fatalf("saved %v, %d refreshes", c.saved(t), len(c.g.refreshes()))
	}
}

// Tokens without a client aren't a sign-in: they can't be refreshed.
func TestTokensWithoutAClient(t *testing.T) {
	c := setup(t, false)
	if err := os.WriteFile(c.file, []byte(`{"youtube":{"refreshToken":"FAKE_YT_REFRESH_x","channelId":"UCx"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.sender.Status(); got != (server.SenderStatus{Detail: "Needs your own Google OAuth client."}) {
		t.Fatalf("%+v", got)
	}
}

// The client changing while Google is answering the code exchange: the
// sign-in belongs to the old client, so it isn't stored over the new one.
func TestTheClientChangingDuringTheCodeExchange(t *testing.T) {
	c := setup(t, false)
	auth := c.begin(t)
	hold, seen := make(chan struct{}), make(chan struct{})
	c.g.state(func() { c.g.exchangeHold, c.g.exchangeSeen = hold, seen })
	back := make(chan response)
	go func() { back <- c.comeBack(t, nil, "state", auth.Query().Get("state"), "code", c.code()) }()
	<-seen
	other := map[string]any{"clientId": "111111111111-otherfakeclient.apps.googleusercontent.com", "clientSecret": "FAKE_OTHER_SECRET"}
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": other["clientId"], "clientSecret": other["clientSecret"]}, nil)
	close(hold)
	r := <-back
	if r.status != 400 || c.setupState(t)["signinError"] != "The Google client changed during sign-in. Start again." {
		t.Fatalf("%d %v", r.status, c.setupState(t)["signinError"])
	}
	if !reflect.DeepEqual(c.saved(t), other) {
		t.Fatalf("saved %v", c.saved(t))
	}
}

// A channel recorded without a sign-in (a hand-edited file) isn't shown.
func TestTheChannelIsShownOnlyWhileSignedIn(t *testing.T) {
	c := setup(t, false)
	if err := os.WriteFile(c.file, []byte(`{"youtube":{"clientId":"`+clientID+`","channelId":"UCx","handle":"@x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := c.setupState(t); st["channelId"] != "" || st["handle"] != "" || st["clientId"] != clientID {
		t.Fatalf("%v", st)
	}
}

// The secret's limit is JavaScript's .length: UTF-16 units, so 101 emoji
// (202 units) is too long and 200 one-unit characters is not.
func TestSecretLengthIsUTF16(t *testing.T) {
	c := setup(t, false)
	if r := c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": strings.Repeat("😀", 101)}, nil); r.json["code"] != "bad_client_secret" {
		t.Fatalf("%v", r.json)
	}
	if r := c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": strings.Repeat("é", 200)}, nil); r.status != 200 {
		t.Fatalf("%d %v", r.status, r.json)
	}
}
