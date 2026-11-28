package youtubesend

// test/youtube-send.test.js at 29a9d2c, the viewer cases (20–22): the
// official count through videos.list, and the gated switch between it and
// the free path.

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/viewers"
)

func liveDetails(d map[string]any) func(call) reply {
	return func(call) reply {
		return reply{200, map[string]any{"items": []any{map[string]any{"id": video, "liveStreamingDetails": d}}}}
	}
}

func (r *rig) used(t *testing.T) float64 {
	return r.quota(t)["usedTodayEstimate"].(float64)
}

// 20
func TestViewersViaTheAPIOneVideosListAPollCountedAgainstTheQuotaEstimate(t *testing.T) {
	c := setup(t, true)
	ctx := context.Background()
	before := c.used(t)
	c.g.state(func() {
		c.g.video = liveDetails(map[string]any{"actualStartTime": "2026-11-20T10:00:00Z", "concurrentViewers": "152", "activeLiveChatId": "FAKE_LIVE_CHAT_ID"})
	})
	if got, err := c.sender.Viewers(ctx, video); err != nil || !reflect.DeepEqual(got, &viewers.YouTube{Live: true, Count: 152}) {
		t.Fatalf("%+v %v", got, err)
	}
	if c.used(t) != before+1 {
		t.Fatalf("used %v, was %v", c.used(t), before)
	}
	calls := c.g.apiCalls()
	if q := calls[len(calls)-1].query; q.Encode() != "id="+video+"&part=liveStreamingDetails" {
		t.Fatalf("query %v", q)
	}
	// not started yet, and ended
	c.g.state(func() { c.g.video = liveDetails(map[string]any{"scheduledStartTime": "2030-01-01T00:00:00Z"}) })
	if got, _ := c.sender.Viewers(ctx, video); !reflect.DeepEqual(got, &viewers.YouTube{}) {
		t.Fatalf("scheduled: %+v", got)
	}
	c.g.state(func() {
		c.g.video = liveDetails(map[string]any{"actualStartTime": "2026-11-20T10:00:00Z", "actualEndTime": "2026-11-20T12:00:00Z"})
	})
	if got, _ := c.sender.Viewers(ctx, video); !reflect.DeepEqual(got, &viewers.YouTube{}) {
		t.Fatalf("ended: %+v", got)
	}
	// live but the owner hid the count: can't tell, so the caller falls back to the free path
	c.g.state(func() { c.g.video = liveDetails(map[string]any{"actualStartTime": "2026-11-20T10:00:00Z"}) })
	if got, _ := c.sender.Viewers(ctx, video); got != nil {
		t.Fatalf("hidden: %+v", got)
	}
	// still never a read of the chat
	for _, x := range c.g.apiCalls() {
		if x.method == "GET" && strings.Contains(x.path, "liveChat") {
			t.Fatalf("read chat: %s", x.key)
		}
	}
}

// 21
func TestViewersViaTheAPINoAnswerWhenSignedOutOrOutOfQuotaAndNothingIsSpentAsking(t *testing.T) {
	c := setup(t, false)
	ctx := context.Background()
	n0 := c.g.total()
	if got, _ := c.sender.Viewers(ctx, video); got != nil || c.g.total() != n0 {
		t.Fatalf("%+v, %d calls", got, c.g.total()-n0)
	}
	c.signIn(t)
	if got, _ := c.sender.Viewers(ctx, ""); got != nil {
		t.Fatalf("%+v", got)
	}
	c.g.state(func() {
		c.g.video = func(call) reply {
			return gErr(403, "quotaExceeded", "The request cannot be completed because you have exceeded your quota.")
		}
	})
	if got, _ := c.sender.Viewers(ctx, video); got != nil {
		t.Fatalf("%+v", got)
	}
	n := c.g.total()
	if got, _ := c.sender.Viewers(ctx, video); got != nil || c.g.total() != n {
		t.Fatalf("%+v, %d more calls", got, c.g.total()-n) // quota gone: no further calls until the reset
	}
	if got := send(t, c.sender, "x", target); got.Code != "quota_exhausted" {
		t.Fatalf("%+v", got)
	}
}

// 22
func TestViewerSourceFreeUnlessSwitchedThroughTheGatedSetupRoute(t *testing.T) {
	g := newGoogle(t)
	var mu sync.Mutex
	source := "free"
	sender := New(Options{
		Store: secrets.New(filepath.Join(t.TempDir(), "secrets.json"), secrets.Options{}), Endpoints: g.endpoints(),
		GetViewerSource: func() string { mu.Lock(); defer mu.Unlock(); return source },
		SetViewerSource: func(s string) { mu.Lock(); source = s; mu.Unlock() },
	})
	app := startApp(t, func(o *server.Options) { o.Senders = map[string]server.Sender{"youtube": sender} })
	cur := func() string { mu.Lock(); defer mu.Unlock(); return source }
	if v, _ := sender.SetupState(app.port).Get("viewerSource"); v != "free" {
		t.Fatalf("%v", v)
	}
	if r := app.post(t, "/api/setup/youtube/viewers", map[string]any{"source": "api"}, map[string]string{"x-dock-token": "wrong"}); r.status != 403 {
		t.Fatalf("wrong token: %d", r.status)
	}
	if r := app.post(t, "/api/setup/youtube/viewers", map[string]any{"source": "everything"}, nil); r.json["code"] != "bad_source" || cur() != "free" {
		t.Fatalf("%v, source %s", r.json, cur())
	}
	if r := app.post(t, "/api/setup/youtube/viewers", map[string]any{"source": "api"}, nil); r.status != 200 || cur() != "api" {
		t.Fatalf("%d, source %s", r.status, cur())
	}
	st := app.get(t, "/api/setup/state")
	yt := st.json["platforms"].(map[string]any)["youtube"].(map[string]any)
	if yt["viewerSource"] != "api" || yt["viewerPollCost"] != float64(1) {
		t.Fatalf("%v", yt)
	}
	app.post(t, "/api/setup/youtube/viewers", map[string]any{"source": "free"}, nil)
	if cur() != "free" {
		t.Fatal(cur())
	}
}

// Anything but "api" is the free source, as the source's setupState says.
func TestViewerSourceAnythingButAPIIsFree(t *testing.T) {
	g := newGoogle(t)
	for in, want := range map[string]string{"api": "api", "free": "free", "API": "free", "": "free"} {
		s := New(Options{Store: secrets.New(filepath.Join(t.TempDir(), "secrets.json"), secrets.Options{}), Endpoints: g.endpoints(),
			GetViewerSource: func() string { return in }})
		if v, _ := s.SetupState(0).Get("viewerSource"); v != want {
			t.Errorf("%q: %v", in, v)
		}
	}
}
