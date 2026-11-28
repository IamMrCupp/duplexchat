package twitchsend

// Stream info beyond the source's cases: what each answer says, word for
// word, and the edges of what Twitch sends back.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/server"
)

func TestInfoStatusBeforeSigningIn(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	want := server.InfoStatus{Configured: true, Detail: "Not signed in to Twitch."}
	if st := s.InfoStatus(); st.Limits == nil || !reflect.DeepEqual(func() server.InfoStatus { st.Limits = nil; return st }(), want) {
		t.Fatalf("signed out: %+v", st)
	}
	r.cfg.set("")
	if st := s.InfoStatus(); st.Configured || st.NeedsPermission || st.Ready || st.Detail != "Needs a Twitch client ID — open /setup." {
		t.Fatalf("no client ID: %+v", st)
	}
}

func TestInfoStatusAfterTwitchEndedTheSignInSaysSo(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signInForInfo(t, s)
	f.state(func() { f.patch = func(call) reply { return invalid } })
	setInfo(t, s, server.InfoChange{Title: "x"})
	if st := s.InfoStatus(); st.SignedIn || st.NeedsPermission || st.Detail != Expired {
		t.Fatalf("status %+v", st)
	}
	if got := getInfo(t, s); got["reason"] != Expired {
		t.Fatalf("get: %v", got)
	}
}

func TestInfoNotSignedInNeverReachesTwitch(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	want := map[string]any{"ok": false, "code": "not_signed_in", "reason": "Not signed in to Twitch."}
	for name, got := range map[string]map[string]any{
		"get":    getInfo(t, s),
		"set":    setInfo(t, s, server.InfoChange{Title: "x"}),
		"search": search(t, s, "x"),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", name, got)
		}
	}
	// signed in once, then out: the tokens are gone, so nothing is asked
	signIn(t, s)
	setup(t, s, "signout", "{}")
	calls := f.callCount()
	if got := getInfo(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("after sign-out: %v", got)
	}
	if f.callCount() != calls {
		t.Fatal("a signed-out read reached Twitch")
	}
}

func TestInfoTwitchUnreachableIsAPlainAnswer(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signInForInfo(t, s)
	s.o.APIBase = "http://127.0.0.1:1"
	want := map[string]any{"ok": false, "code": "unreachable", "reason": "Couldn't reach Twitch."}
	for name, got := range map[string]map[string]any{
		"get":    getInfo(t, s),
		"set":    setInfo(t, s, server.InfoChange{Title: "x"}),
		"search": search(t, s, "x"),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", name, got)
		}
	}
	if !s.Status().SignedIn {
		t.Fatal("being offline signed me out")
	}
}

func TestInfoChecksSayWhichTagAndCountCharacters(t *testing.T) {
	s := makeSender(t, newFake(t, 1)).sender
	for _, c := range []struct {
		title string
		tags  []string
		want  string
	}{
		{"", nil, "Twitch needs a title."},
		{strings.Repeat("é", 140), nil, ""},
		{strings.Repeat("😀", 141), nil, "Twitch titles are 140 characters at most."},
		{"x", make([]string, 11), "Twitch takes 10 tags at most."},
		{"x", []string{"ok", "two words"}, `Twitch tags can't contain spaces: "two words".`},
		{"x", []string{"tab\there"}, `Twitch tags can't contain spaces: "tab	here".`},
		{"x", []string{"no break"}, "Twitch tags can't contain spaces: \"no break\"."},
		{"x", []string{strings.Repeat("é", 25)}, ""},
		{"x", []string{strings.Repeat("y", 26)}, `Twitch tags are 25 characters at most: "` + strings.Repeat("y", 26) + `".`},
	} {
		if got := s.CheckInfo(c.title, c.tags); got != c.want {
			t.Errorf("CheckInfo(%q, %q) = %q, want %q", c.title, c.tags, got, c.want)
		}
	}
}

func TestInfoAMissingScopeIsSpottedWhateverItsCase(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signInForInfo(t, s)
	f.state(func() {
		f.patch = func(call) reply { return reply{401, map[string]any{"message": "MISSING SCOPE"}, nil} }
	})
	if got := setInfo(t, s, server.InfoChange{Title: "x"}); got["code"] != "needs_permission" {
		t.Fatalf("save: %v", got)
	}
	// /scope/i without u: a long s isn't an s
	f.state(func() {
		f.patch = func(call) reply { return reply{401, map[string]any{"message": "missing ſcope"}, nil} }
	})
	if got := setInfo(t, s, server.InfoChange{Title: "x"}); got["code"] != "not_signed_in" {
		t.Fatalf("save: %v", got)
	}
}

func TestInfoReadsWhatTwitchSendsBack(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	f.state(func() {
		f.channel = map[string]any{"broadcaster_id": "1001", "broadcaster_name": "Example_User", "game_id": "", "title": nil, "tags": []any{1, nil, "x"}}
	})
	want := map[string]any{"title": "", "category": nil, "tags": []any{"1", "null", "x"}, "where": "#Example_User"}
	if got := getInfo(t, s); !reflect.DeepEqual(got["info"], want) {
		t.Fatalf("info: %v", got)
	}
	f.state(func() { f.channel = map[string]any{"broadcaster_id": "1001", "game_id": 7, "tags": "x"} })
	want = map[string]any{"title": "", "category": map[string]any{"id": "7", "name": ""}, "tags": []any{}, "where": "#"}
	if got := getInfo(t, s); !reflect.DeepEqual(got["info"], want) {
		t.Fatalf("info: %v", got)
	}
	f.state(func() { f.channel = map[string]any{"broadcaster_id": "2002"} })
	if got := getInfo(t, s); !reflect.DeepEqual(got, map[string]any{"ok": false, "code": "rejected", "reason": "Twitch returned no channel for this sign-in."}) {
		t.Fatalf("no channel: %v", got)
	}

	f.state(func() {
		f.search = func(call) reply {
			return reply{200, map[string]any{"data": []any{map[string]any{"id": 5}, nil, map[string]any{"id": "", "name": "empty"}}}, nil}
		}
	})
	if got := search(t, s, "x"); !reflect.DeepEqual(got, map[string]any{"ok": true, "categories": []any{map[string]any{"id": "5", "name": ""}}}) {
		t.Fatalf("search: %v", got)
	}
	f.state(func() { f.search = func(call) reply { return reply{200, map[string]any{"data": "nope"}, nil} } })
	if got := search(t, s, "x"); !reflect.DeepEqual(got, map[string]any{"ok": true, "categories": []any{}}) {
		t.Fatalf("search: %v", got)
	}
	f.state(func() { f.search = func(call) reply { return reply{500, nil, nil} } })
	if got := search(t, s, "x"); !reflect.DeepEqual(got, map[string]any{"ok": false, "code": "rejected", "reason": "Twitch answered HTTP 500."}) {
		t.Fatalf("search: %v", got)
	}
}

func TestInfoSignedOutWinsOverTheRateLimitHold(t *testing.T) {
	f := newFake(t, 1)
	s := makeSender(t, f).sender
	signIn(t, s)
	f.state(func() { f.search = func(call) reply { return reply{429, nil, nil} } })
	if got := search(t, s, "x"); got["code"] != "rate_limited" {
		t.Fatalf("search: %v", got)
	}
	setup(t, s, "signout", "{}")
	if got := getInfo(t, s); got["code"] != "not_signed_in" {
		t.Fatalf("get: %v", got)
	}
}

func TestInfoADeadRefreshTokenIsASignOutWithTheReason(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	f.state(func() { f.refresh = "someone else's" })
	r.clock.add(5 * time.Hour) // past the access token's 4 hours
	got := getInfo(t, s)
	if got["code"] != "not_signed_in" || got["reason"] == "Not signed in to Twitch." || got["reason"] == "" {
		t.Fatalf("get: %v", got)
	}
	if got["reason"] != s.session.Problem() {
		t.Fatalf("reason %q, problem %q", got["reason"], s.session.Problem())
	}
}
