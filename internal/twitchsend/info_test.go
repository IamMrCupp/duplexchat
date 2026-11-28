package twitchsend

// Stream info, test/twitch-send.test.js 23–28 at 29a9d2c. 29, over HTTP,
// is in http_test.go.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/server"
)

const bothScopes = "user:write:chat " + InfoScope

func signInForInfo(t *testing.T, s *Sender) {
	t.Helper()
	if code, body := setup(t, s, "signin", `{"info":true}`); code != 200 {
		t.Fatalf("signin: %d %v", code, body)
	}
	waitFor(t, "the info scope", s.hasInfoScope)
}

// plainMap is a result as the pages would parse it.
func plainMap(t *testing.T, o *jsjson.Object) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(jsjson.Stringify(o, ""), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getInfo(t *testing.T, s *Sender) map[string]any {
	t.Helper()
	r, err := s.GetInfo(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	return plainMap(t, r)
}

func setInfo(t *testing.T, s *Sender, c server.InfoChange) map[string]any {
	t.Helper()
	r, err := s.SetInfo(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return plainMap(t, r)
}

func search(t *testing.T, s *Sender, q string) map[string]any {
	t.Helper()
	r, err := s.SearchCategories(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	return plainMap(t, r)
}

func (f *fakeTwitch) patchCalls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.patches...)
}

func (f *fakeTwitch) deviceScopes() []string {
	var out []string
	for _, c := range f.find(func(c call) bool { return c.key == "POST /oauth2/device" }) {
		out = append(out, c.body["scopes"].(string))
	}
	return out
}

func strp(s string) *string { return &s }

// 23
func TestInfoOrdinarySignInAsksForChatOnlyAndASaveIsRefusedBeforeTwitchHears(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	if got := f.deviceScopes(); got[0] != "user:write:chat" {
		t.Fatalf("asked for %q", got[0])
	}
	if got := r.saved(t)["scopes"]; !reflect.DeepEqual(got, []any{"user:write:chat"}) {
		t.Fatalf("saved scopes %v", got)
	}
	st := s.InfoStatus()
	if !st.SignedIn || !st.NeedsPermission || st.Ready {
		t.Fatalf("status %+v", st)
	}
	if st.Detail != "Twitch needs one more permission to change stream info." {
		t.Fatalf("detail %q", st.Detail)
	}
	if state(t, s)["hasInfoScope"] != false {
		t.Fatal("hasInfoScope")
	}
	if got := setInfo(t, s, server.InfoChange{Title: "New title"}); got["code"] != "needs_permission" || got["ok"] != false {
		t.Fatalf("save: %v", got)
	}
	if n := len(f.patchCalls()); n != 0 {
		t.Fatalf("%d patches", n)
	}
	// reading what's there needs no extra scope
	want := map[string]any{"title": "Old title", "category": map[string]any{"id": "100", "name": "Old Category"}, "tags": []any{"old"}, "where": "#example_user"}
	if got := getInfo(t, s); got["ok"] != true || !reflect.DeepEqual(got["info"], want) {
		t.Fatalf("get: %v", got)
	}
}

// 24
func TestInfoASignInFromBeforeScopesWereRecordedCountsAsChatOnly(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	b, err := os.ReadFile(r.file)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]map[string]any
	json.Unmarshal(b, &saved)
	delete(saved["twitch"], "scopes")
	b, _ = json.Marshal(saved)
	if err := os.WriteFile(r.file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.InfoStatus().NeedsPermission {
		t.Fatal("needsPermission")
	}
	if got := setInfo(t, s, server.InfoChange{Title: "x"}); got["code"] != "needs_permission" {
		t.Fatalf("save: %v", got)
	}
}

// 25
func TestInfoThePanelsSignInAsksForBothScopesAndLaterSignInsKeepAskingForBoth(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	signInForInfo(t, s)
	if got := f.deviceScopes(); got[1] != bothScopes {
		t.Fatalf("asked for %q", got[1])
	}
	// the poll carries the same list it asked for
	polls := f.find(func(c call) bool { return c.key == "POST /oauth2/token" && c.body["device_code"] != nil })
	if got := polls[len(polls)-1].body["scopes"]; got != bothScopes {
		t.Fatalf("poll scopes %q", got)
	}
	if got := r.saved(t)["scopes"]; !reflect.DeepEqual(got, []any{"user:write:chat", InfoScope}) {
		t.Fatalf("saved scopes %v", got)
	}
	st := s.InfoStatus()
	limits := plainMap(t, st.Limits)
	if st.NeedsPermission || !st.Ready || st.Detail != "" ||
		!reflect.DeepEqual(limits, map[string]any{"title": 140.0, "tags": 10.0, "tagLength": 25.0}) {
		t.Fatalf("status %+v %v", st, limits)
	}
	if got := string(jsjson.Stringify(st.Limits, "")); got != `{"title":140,"tags":10,"tagLength":25}` {
		t.Fatalf("limits %s", got)
	}

	// a refresh doesn't lose them
	if _, err := s.session.Tokens(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if !s.hasInfoScope() {
		t.Fatal("lost the scope in a refresh")
	}
	// signing in again from /setup (no info flag) must not drop the permission
	f.state(func() { f.pendingLeft = 0 })
	setup(t, s, "signin", "{}")
	if got := f.deviceScopes(); got[2] != bothScopes {
		t.Fatalf("asked for %q", got[2])
	}
	// after a deliberate sign-out it's back to asking for chat only
	setup(t, s, "cancel", "")
	setup(t, s, "signout", "{}")
	signIn(t, s)
	if got := f.deviceScopes(); got[3] != "user:write:chat" {
		t.Fatalf("asked for %q", got[3])
	}
}

// 26
func TestInfoSavePatchesMyOwnChannelWithExactlyWhatTheFormHeld(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signInForInfo(t, s)
	got := setInfo(t, s, server.InfoChange{Title: "Fixing an example board", CategoryID: strp("509670"), Tags: []string{"repair", "soldering"}})
	if !reflect.DeepEqual(got, map[string]any{"ok": true}) {
		t.Fatalf("save: %v", got)
	}
	c := f.patchCalls()[0]
	if c.query.Get("broadcaster_id") != "1001" || c.header.Get("content-type") != "application/json" {
		t.Fatalf("patch: %s %v", c.rawQuery, c.header)
	}
	if c.raw != `{"title":"Fixing an example board","tags":["repair","soldering"],"game_id":"509670"}` {
		t.Fatalf("body %s", c.raw)
	}
	// category left alone: no game_id at all. Cleared: an empty one.
	setInfo(t, s, server.InfoChange{Title: "x"})
	if got := f.patchCalls()[1].raw; got != `{"title":"x","tags":[]}` {
		t.Fatalf("body %s", got)
	}
	setInfo(t, s, server.InfoChange{Title: "x", CategoryID: strp("")})
	if got := f.patchCalls()[2].raw; got != `{"title":"x","tags":[],"game_id":""}` {
		t.Fatalf("body %s", got)
	}
}

// 27
func TestInfoTwitchsRefusalsComeBackInItsWordsAMissingScopeIsNotADeadSignIn(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signInForInfo(t, s)
	form := server.InfoChange{Title: "x", Tags: []string{"a"}}

	f.state(func() {
		f.patch = func(call) reply {
			return reply{400, map[string]any{"status": 400, "message": "One or more tags were not applied because they failed AutoMod review."}, nil}
		}
	})
	got := setInfo(t, s, form)
	if got["ok"] != false || got["code"] != "rejected" || !strings.Contains(got["reason"].(string), "AutoMod") {
		t.Fatalf("automod: %v", got)
	}

	// Twitch says the token lacks the scope although we thought it had it
	f.state(func() {
		f.patch = func(call) reply {
			return reply{401, map[string]any{"status": 401, "message": "Missing scope: channel:manage:broadcast or user:edit:broadcast"}, nil}
		}
	})
	if got := setInfo(t, s, form); got["code"] != "needs_permission" || got["reason"] != "Twitch needs one more permission to change stream info." {
		t.Fatalf("scope: %v", got)
	}
	if !s.Status().SignedIn {
		t.Fatal("a missing scope signed me out")
	}

	reset := fmt.Sprint((r.clock.ms.Load()+999)/1000 + 30)
	f.state(func() {
		f.patch = func(call) reply {
			return reply{429, map[string]any{"status": 429, "message": "Too Many Requests"}, map[string]string{"ratelimit-reset": reset}}
		}
	})
	got = setInfo(t, s, form)
	if got["code"] != "rate_limited" {
		t.Fatalf("429: %v", got)
	}
	if ra, _ := got["retryAfter"].(float64); !(ra > 0 && ra <= 31) {
		t.Fatalf("retryAfter %v", got["retryAfter"])
	}
	before := len(f.patchCalls())
	if got := setInfo(t, s, form); got["code"] != "rate_limited" { // held off: nothing sent
		t.Fatalf("held: %v", got)
	}
	if got := getInfo(t, s); got["code"] != "rate_limited" || got["retryAfter"] == nil {
		t.Fatalf("held get: %v", got)
	}
	if n := len(f.patchCalls()); n != before {
		t.Fatalf("sent while held: %d → %d", before, n)
	}
	r.clock.add(31 * time.Second)

	f.state(func() { f.patch = func(call) reply { return invalid } })
	if got := setInfo(t, s, form); got["code"] != "not_signed_in" || got["reason"] != Expired {
		t.Fatalf("401: %v", got)
	}
	if s.Status().SignedIn {
		t.Fatal("still signed in")
	}
	if got := getInfo(t, s); got["code"] != "not_signed_in" {
		t.Fatalf("get: %v", got)
	}
	if got := search(t, s, "x"); got["code"] != "not_signed_in" {
		t.Fatalf("search: %v", got)
	}
}

// 28
func TestInfoCategorySearchAndTheRulesCheckedBeforeTwitchIsAsked(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s) // searching needs no extra scope
	got := search(t, s, "exam ple&x")
	want := map[string]any{"ok": true, "categories": []any{map[string]any{"id": "509670", "name": "Example Category"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("search: %v", got)
	}
	c := f.find(func(c call) bool { return c.key == "GET /helix/search/categories" })[0]
	if !reflect.DeepEqual(c.query, url.Values{"first": {"10"}, "query": {"exam ple&x"}}) {
		t.Fatalf("query %s", c.rawQuery)
	}

	tags := make([]string, 10)
	for i := range tags {
		tags[i] = fmt.Sprint("tag", i)
	}
	title := strings.Repeat("x", 140)
	check := func(title string, tags []string, want string) {
		t.Helper()
		got := s.CheckInfo(title, tags)
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("CheckInfo(%d chars, %v) = %q, want %q", len(title), tags, got, want)
		}
	}
	check(title, tags, "")
	check("", tags, "needs a title")
	check(strings.Repeat("x", 141), tags, "140")
	check(title, append(append([]string(nil), tags...), "eleven"), "10 tags")
	check(title, []string{"two words"}, "spaces")
	check(title, []string{strings.Repeat("x", 26)}, "25")
	check(title, []string{strings.Repeat("x", 25)}, "")
}
