package viewers

// What the source's tests leave open, pinned to the source's behaviour.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/hub"
)

// Only a live number is kept through a failed poll; anything else is
// replaced by "can't tell" straight away.
func TestAFailureAfterANonLiveStateIsUnknownAtOnce(t *testing.T) {
	var mu sync.Mutex
	answer := Twitch{State: "ok", Live: map[string]float64{}}
	w := newWorld(func([]string) (Twitch, error) { mu.Lock(); defer mu.Unlock(); return answer, nil }, nil)
	w.add(tw("examplechannel"))
	if got := w.vc.Refresh(bg).Twitch.State; got != "offline" {
		t.Fatal(got)
	}
	mu.Lock()
	answer = Twitch{State: "error"}
	mu.Unlock()
	if got := w.vc.Refresh(bg).Twitch; !reflect.DeepEqual(got, hub.Count{State: "unknown", Note: "Couldn't reach Twitch."}) {
		t.Fatalf("%+v", got)
	}
}

// A Twitch source whose reader has no login yet: a chip, but nothing to ask.
func TestATwitchSourceWithoutALoginIsUnknownAndAsksNobody(t *testing.T) {
	w := newWorld(nil, nil)
	w.add(tw(""))
	if got := w.vc.Refresh(bg).Twitch; !reflect.DeepEqual(got, hub.Count{State: "unknown"}) || len(w.twCalls) != 0 {
		t.Fatalf("%+v, %d calls", got, len(w.twCalls))
	}
}

// Signed out with no note of its own: the poller's own words.
func TestSignedOutWithoutANoteGetsTheDefault(t *testing.T) {
	w := newWorld(func([]string) (Twitch, error) { return Twitch{State: "signed_out"}, nil }, nil)
	w.add(tw("examplechannel"))
	if got := w.vc.Refresh(bg).Twitch; got.Note != "Sign in to Twitch on /setup to show viewers." {
		t.Fatalf("%+v", got)
	}
}

// A change in the note alone is a change: the bar's tooltip says why.
func TestANoteOnlyChangeIsAnnounced(t *testing.T) {
	var mu sync.Mutex
	note := "first"
	w := newWorld(func([]string) (Twitch, error) {
		mu.Lock()
		defer mu.Unlock()
		return Twitch{State: "signed_out", Note: note}, nil
	}, nil)
	w.add(tw("examplechannel"))
	w.vc.Refresh(bg)
	mu.Lock()
	note = "second"
	mu.Unlock()
	w.vc.Refresh(bg)
	if len(w.changes) != 2 || w.changes[1].Twitch.Note != "second" {
		t.Fatalf("%+v", w.changes)
	}
}

// The key comes first in the query, as the source builds it.
func TestFetchQueryOrderWithAKey(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	FetchYouTube(context.Background(), FetchOptions{VideoID: "v", APIKey: "FAKE KEY", Origin: srv.URL})
	if q != "key=FAKE%20KEY&prettyPrint=false" {
		t.Fatal(q)
	}
}

func TestPreferOfficialAsksTheAPIFirstOnlyWhenSwitchedOn(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	note := func(s string) { mu.Lock(); asked = append(asked, s); mu.Unlock() }
	var on bool
	var answer *YouTube
	f := PreferOfficial(func() bool { return on },
		func(_ context.Context, id string) (*YouTube, error) { note("api " + id); return answer, nil },
		func(_ context.Context, s Source) (*YouTube, error) { note("free " + s.VideoID); return live(3), nil })
	src := yt("FAKEvideo01")
	if got, _ := f(bg, src); !reflect.DeepEqual(got, live(3)) || !reflect.DeepEqual(asked, []string{"free FAKEvideo01"}) {
		t.Fatalf("off: %+v %v", got, asked)
	}
	on, asked, answer = true, nil, live(152)
	if got, _ := f(bg, src); !reflect.DeepEqual(got, live(152)) || !reflect.DeepEqual(asked, []string{"api FAKEvideo01"}) {
		t.Fatalf("on: %+v %v", got, asked)
	}
	asked, answer = nil, nil // can't tell: the free path answers
	if got, _ := f(bg, src); !reflect.DeepEqual(got, live(3)) || !reflect.DeepEqual(asked, []string{"api FAKEvideo01", "free FAKEvideo01"}) {
		t.Fatalf("fallback: %+v %v", got, asked)
	}
	asked, answer = nil, notLive // not live is an answer, not a fallback
	if got, _ := f(bg, src); !reflect.DeepEqual(got, notLive) || len(asked) != 1 {
		t.Fatalf("not live: %+v %v", got, asked)
	}
}
