package twitchsend

// test/twitch-send.test.js at 29a9d2c, the viewer cases (20 and 21): Get
// Streams with the dock's sign-in, against the fake Twitch.

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/viewers"
)

// 20
func TestViewersGetStreamsWithTheDocksSignInOfflineChannelsAreSimplyAbsent(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	ctx := context.Background()
	got, err := r.sender.Viewers(ctx, []string{"examplechannel"})
	if err != nil || !reflect.DeepEqual(got, viewers.Twitch{State: "signed_out", Note: "Sign in to Twitch on /setup to show viewers."}) {
		t.Fatalf("%+v %v", got, err)
	}
	if f.callCount() != 0 { // not signed in: nothing is asked
		t.Fatalf("%d calls", f.callCount())
	}
	signIn(t, r.sender)
	got, _ = r.sender.Viewers(ctx, []string{"examplechannel", "second"})
	if !reflect.DeepEqual(got, viewers.Twitch{State: "ok", Live: map[string]float64{"examplechannel": 42}}) {
		t.Fatalf("%+v", got)
	}
	calls := f.find(func(c call) bool { return c.key == "GET /helix/streams" })
	c := calls[len(calls)-1]
	if c.rawQuery != "user_login=examplechannel&user_login=second" {
		t.Errorf("query %s", c.rawQuery)
	}
	if c.header.Get("authorization") != "Bearer "+f.get(func() string { return f.access }) || c.header.Get("client-id") != clientID {
		t.Errorf("headers %v", c.header)
	}
	f.state(func() { f.streams = func(call) reply { return reply{200, map[string]any{"data": []any{}}, nil} } })
	if got, _ := r.sender.Viewers(ctx, []string{"examplechannel"}); !reflect.DeepEqual(got, viewers.Twitch{State: "ok", Live: map[string]float64{}}) {
		t.Fatalf("%+v", got)
	}
	f.state(func() {
		f.streams = func(call) reply {
			return reply{200, map[string]any{"data": []any{
				map[string]any{"user_login": "ExampleChannel", "type": "live", "viewer_count": 7},
				map[string]any{"user_login": "rerun", "type": "", "viewer_count": 3},
			}}, nil}
		}
	})
	if got, _ := r.sender.Viewers(ctx, []string{"examplechannel"}); !reflect.DeepEqual(got.Live, map[string]float64{"examplechannel": 7}) {
		t.Fatalf("%+v", got)
	}
}

// 21
func TestViewersRateLimitHoldsOffADeadTokenSaysSignedOutErrorsAreErrors(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	ctx := context.Background()
	signIn(t, r.sender)
	reset := fmt.Sprint(r.clock.now().UnixMilli()/1000 + 20)
	f.state(func() {
		f.streams = func(call) reply {
			return reply{429, map[string]any{"status": 429, "message": "Too Many Requests"}, map[string]string{"Ratelimit-Reset": reset}}
		}
	})
	if got, _ := r.sender.Viewers(ctx, []string{"examplechannel"}); got.State != "error" {
		t.Fatalf("%+v", got)
	}
	n := f.count("GET /helix/streams")
	if got, _ := r.sender.Viewers(ctx, []string{"examplechannel"}); got.State != "error" || f.count("GET /helix/streams") != n {
		t.Fatalf("%+v, %d calls", got, f.count("GET /helix/streams")) // held locally
	}
	r.clock.add(21 * time.Second)
	f.state(func() { f.streams = func(call) reply { return reply{503, nil, nil} } })
	if got, _ := r.sender.Viewers(ctx, []string{"examplechannel"}); !reflect.DeepEqual(got, viewers.Twitch{State: "error", Note: "Twitch answered HTTP 503."}) {
		t.Fatalf("%+v", got)
	}
	f.state(func() { f.streams = nil; f.refresh = "FAKE_REFRESH_GONE" })
	r.clock.add(14400 * time.Second)
	if got, _ := r.sender.Viewers(ctx, []string{"examplechannel"}); got.State != "signed_out" {
		t.Fatalf("%+v", got)
	}
}

// The parts of viewers() the source's tests don't reach: a 401 from Get
// Streams itself, the 100-login cap, and a network failure.
func TestViewersEdges(t *testing.T) {
	f := newFake(t, 0)
	r := makeSender(t, f)
	ctx := context.Background()
	signIn(t, r.sender)
	f.state(func() {
		f.streams = func(call) reply {
			return reply{200, map[string]any{"data": []any{nil, 5, map[string]any{"user_login": "a", "type": "live", "viewer_count": "x"}, map[string]any{"user_login": "b", "type": "live", "viewer_count": "12"}}}, nil}
		}
	})
	if got, _ := r.sender.Viewers(ctx, []string{"a", "b"}); !reflect.DeepEqual(got.Live, map[string]float64{"b": 12}) {
		t.Fatalf("%+v", got)
	}
	many := make([]string, 120)
	for i := range many {
		many[i] = fmt.Sprintf("c%d", i)
	}
	r.sender.Viewers(ctx, many)
	calls := f.find(func(c call) bool { return c.key == "GET /helix/streams" })
	if q := calls[len(calls)-1].query["user_login"]; len(q) != 100 || q[99] != "c99" {
		t.Fatalf("%d logins asked", len(q))
	}
	f.state(func() { f.streams = func(call) reply { return reply{200, map[string]any{"nope": true}, nil} } })
	if got, _ := r.sender.Viewers(ctx, []string{"a"}); !reflect.DeepEqual(got, viewers.Twitch{State: "error", Note: "Twitch answered HTTP 200."}) {
		t.Fatalf("%+v", got)
	}
	// a 401 that survives the refresh and retry: the sign-in is dead
	f.state(func() { f.streams = func(call) reply { return invalid } })
	if got, _ := r.sender.Viewers(ctx, []string{"a"}); !reflect.DeepEqual(got, viewers.Twitch{State: "signed_out", Note: Expired}) || r.sender.Status().SignedIn {
		t.Fatalf("%+v, signed in %v", got, r.sender.Status().SignedIn)
	}
	// unreachable: Twitch drops the connection
	f.state(func() { f.streams = nil })
	signIn(t, r.sender)
	r3 := makeSender(t, f)
	signIn(t, r3.sender)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/helix/streams" {
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		f.serve(w, req)
	})
	if got, _ := r3.sender.Viewers(ctx, []string{"a"}); !reflect.DeepEqual(got, viewers.Twitch{State: "error", Note: "Couldn't reach Twitch."}) {
		t.Fatalf("%+v", got)
	}
}
