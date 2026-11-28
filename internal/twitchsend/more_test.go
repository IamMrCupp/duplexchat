package twitchsend

// Beyond the source's cases: the parts of lib/twitch-send.js its tests don't
// reach, each pinned so a change to it fails here.

import (
	"context"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/server"
)

func (s *Sender) pendingNow() *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	p := *s.pending
	return &p
}

// The poll interval is Twitch's (at least a second, 5 s when it doesn't say)
// and the code lives expires_in seconds (30 minutes when it doesn't say).
func TestDeviceFlowIntervalAndExpiryDefaults(t *testing.T) {
	f := newFake(t, 1)
	r := makeSender(t, f, func(o *Options) { o.PollInterval = 0 })
	s := r.sender
	for _, c := range []struct {
		reply             map[string]any
		interval, expires float64
	}{
		{map[string]any{"interval": 5, "expires_in": 1800}, 5000, 1800e3},
		{map[string]any{"interval": 0.5, "expires_in": 600}, 1000, 600e3},
		{map[string]any{"interval": "2.5"}, 2500, 1800e3},
		{map[string]any{"interval": 0, "expires_in": "nope"}, 5000, 1800e3},
		{map[string]any{}, 5000, 1800e3},
	} {
		body := map[string]any{"device_code": "FAKE_DEVICE_CODE", "user_code": "ABCDEFGH"}
		for k, v := range c.reply {
			body[k] = v
		}
		f.state(func() { f.device = func() reply { return reply{200, body, nil} } })
		if code, _ := setup(t, s, "signin", ""); code != 200 {
			t.Fatalf("%v: %d", c.reply, code)
		}
		p := s.pendingNow()
		if p.intervalMs != c.interval || p.expiresAt != float64(r.clock.ms.Load())+c.expires {
			t.Errorf("%v: interval %v, expires in %v", c.reply, p.intervalMs, p.expiresAt-float64(r.clock.ms.Load()))
		}
		if st := state(t, s); st["pending"].(map[string]any)["verificationUri"] != "" {
			t.Errorf("no verification_uri reads as %v", st["pending"])
		}
	}
	if d := delay(2147483648); d != time.Millisecond {
		t.Errorf("an overlong interval is %v, as setTimeout makes it 1 ms", d)
	}
}

// slow_down adds five seconds to the interval and keeps polling.
func TestDeviceFlowSlowDown(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	s := r.sender
	f.state(func() { f.deny = "slow_down" })
	setup(t, s, "signin", "")
	waitFor(t, "a slow_down", func() bool { p := s.pendingNow(); return p != nil && p.intervalMs == 5+5000 })
	if e := state(t, s)["pendingError"]; e != "" {
		t.Fatalf("slow_down ended the flow: %v", e)
	}
}

// Twitch gave tokens but won't say whose: no sign-in.
func TestDeviceFlowWhoFailure(t *testing.T) {
	for name, users := range map[string]func(call) reply{
		"500 with data": func(call) reply {
			return reply{500, map[string]any{"data": []any{map[string]any{"id": "1001"}}}, nil}
		},
		"no id":   func(call) reply { return reply{200, map[string]any{"data": []any{map[string]any{"login": "x"}}}, nil} },
		"no data": func(call) reply { return reply{200, map[string]any{"data": []any{}}, nil} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, 0)
			r := makeSender(t, f)
			f.state(func() { f.users = users })
			setup(t, r.sender, "signin", "")
			waitFor(t, "an error", func() bool { return state(t, r.sender)["pendingError"] != "" })
			if e := state(t, r.sender)["pendingError"]; e != "Signed in, but Twitch wouldn't say as whom. Start again." || r.sender.Status().SignedIn || r.saved(t) != nil {
				t.Fatalf("%v, saved %v", e, r.saved(t))
			}
		})
	}
}

// A sign-in cancelled while Twitch is issuing its tokens doesn't land, and
// doesn't go on to ask who signed in.
func TestDeviceFlowCancelledWhileTokensAreIssued(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	s := r.sender
	hold, seen := make(chan struct{}), make(chan struct{})
	f.state(func() { f.grantHold, f.grantSeen = hold, seen })
	setup(t, s, "signin", "")
	<-seen
	setup(t, s, "cancel", "")
	close(hold)
	time.Sleep(50 * time.Millisecond)
	if s.Status().SignedIn || r.saved(t) != nil || f.count("GET /helix/users") != 0 {
		t.Fatalf("a cancelled sign-in landed: %v, %d user calls", r.saved(t), f.count("GET /helix/users"))
	}
}

// A new sign-in landing while the old one's refresh is in flight wins: the
// late refresh answer isn't written over it.
func TestASignInLandingDuringARefreshWins(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	hold, seen := make(chan struct{}), make(chan struct{})
	f.state(func() { f.hold, f.seen = hold, seen })
	r.clock.add((14400 - 60) * time.Second)
	sending := make(chan server.SendResult)
	go func() { got, _ := s.Send(context.Background(), "x", target); sending <- got }()
	<-seen
	// a fresh device sign-in lands
	if code, _ := setup(t, s, "signin", ""); code != 200 {
		t.Fatal(code)
	}
	waitFor(t, "the new sign-in", func() bool { return s.pendingNow() == nil })
	latest := f.get(func() string { return f.access })
	if r.saved(t)["accessToken"] != latest {
		t.Fatalf("the new sign-in isn't stored: %v", r.saved(t))
	}
	close(hold)
	<-sending
	if got := r.saved(t)["accessToken"]; got != latest {
		t.Fatalf("stored %v, want the new sign-in's %s", got, latest)
	}
}

// The channel lookup's own 401 (after its one refresh) is a dead sign-in.
func TestLookup401SignsOut(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	s := r.sender
	signIn(t, s)
	f.state(func() {
		f.users = func(c call) reply { return invalid }
	})
	if got, _ := s.Send(t.Context(), "x", target); got != (server.SendResult{Code: "not_signed_in", Reason: Expired}) {
		t.Fatalf("%+v", got)
	}
	if r.saved(t) != nil || f.refreshes() != 1 {
		t.Fatalf("saved %v, %d refreshes", r.saved(t), f.refreshes())
	}
}

// Signing out forgets the looked-up channels: the next sign-in looks again.
func TestSignOutForgetsLookedUpChannels(t *testing.T) {
	f := newFake(t, 0)
	s := makeSender(t, f).sender
	signIn(t, s)
	s.Send(t.Context(), "x", target)
	setup(t, s, "signout", "")
	signIn(t, s)
	f.state(func() {
		f.users = func(c call) reply {
			return reply{200, map[string]any{"data": []any{map[string]any{"id": "9002", "login": c.query.Get("login")}}}, nil}
		}
	})
	s.Send(t.Context(), "y", target)
	posts := f.find(func(c call) bool { return c.key == "POST /helix/chat/messages" })
	if got := posts[len(posts)-1].body["broadcaster_id"]; got != "9002" {
		t.Fatalf("posted to %v after a fresh sign-in", got)
	}
}

// Tokens without a client ID aren't a sign-in: they can't be refreshed.
func TestNoClientIDMeansSignedOut(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	signIn(t, r.sender)
	r.cfg.set("") // config.json edited by hand
	if got := r.sender.Status(); got != (server.SenderStatus{Detail: "Needs a Twitch client ID."}) {
		t.Fatalf("%+v", got)
	}
}
