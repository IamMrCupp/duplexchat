package targets

// multichat test/targets.test.js at 29a9d2c, case for case.

import (
	"encoding/json"
	"reflect"
	"testing"
)

type world struct {
	sources []Source
	pins    *Pins
	changes int
}

func newWorld() *world {
	w := &world{}
	w.pins = New(func() []Source { return w.sources })
	return w
}

type extra struct{ channel, videoID, owner string }

func (w *world) add(id, platform, url string, x extra) int {
	s := Source{ID: id, Platform: platform, URL: url, Channel: x.channel}
	if s.Channel == "" {
		s.Channel = platform
	}
	if platform == "twitch" {
		s.Reader.Channel = x.channel
	} else {
		s.Reader.VideoID, s.Reader.OwnerChannelID = x.videoID, x.owner
	}
	w.sources = append(w.sources, s)
	w.pins.Added(s)
	return len(w.sources) - 1
}

func (w *world) remove(i int) {
	s := w.sources[i]
	w.sources = append(w.sources[:i], w.sources[i+1:]...)
	w.pins.Removed(s)
}

// set is targets.set with the source's onChange counted.
func (w *world) set(platform, id string) bool {
	ok := w.pins.Set(platform, id)
	if ok {
		w.changes++
	}
	return ok
}

func (w *world) get() map[string]Target { return View(w.sources, w.pins.Snapshot()) }

func deref(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestTheOnlySourceOfAPlatformBecomesItsReplyTarget(t *testing.T) {
	w := newWorld()
	empty := map[string]Target{"twitch": {Choices: []Choice{}}, "youtube": {Choices: []Choice{}}}
	if got := w.get(); !reflect.DeepEqual(got, empty) {
		t.Fatalf("get = %+v", got)
	}
	w.add("aaaa1111", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	tw := w.get()["twitch"]
	if !tw.Pinned || deref(tw.Channel) != "examplechannel" || tw.Label != "#examplechannel" || tw.Key != "aaaa1111:examplechannel" {
		t.Fatalf("twitch = %+v", tw)
	}
	if w.get()["youtube"].Pinned {
		t.Fatal("youtube pinned")
	}
}

func TestASourceAddedLaterNeverTakesOverEvenALiveYouTubeOne(t *testing.T) {
	w := newWorld()
	w.add("bbbb2222", "youtube", "https://youtube.com/@example/live", extra{channel: "Example Channel"}) // mine, not live yet
	w.add("dddd4444", "youtube", "https://youtube.com/watch?v=SOMEONEELSE", extra{channel: "Someone Else", videoID: "SOMEONEELSE"})
	y := w.get()["youtube"]
	if y.SourceID != "bbbb2222" || y.VideoID != nil || y.Label != "Example Channel · not live yet" {
		t.Fatalf("youtube = %+v", y)
	}
	if want := []Choice{{"bbbb2222", "Example Channel"}, {"dddd4444", "Someone Else"}}; !reflect.DeepEqual(y.Choices, want) {
		t.Fatalf("choices = %v", y.Choices)
	}
	w.add("aaaa1111", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	w.add("cccc3333", "twitch", "https://twitch.tv/someoneelse", extra{channel: "someoneelse"})
	if c := deref(w.get()["twitch"].Channel); c != "examplechannel" {
		t.Fatalf("twitch channel = %v", c)
	}
}

func TestRemovingThePinnedSourceLeavesNoTargetItDoesNotHop(t *testing.T) {
	w := newWorld()
	mine := w.add("aaaa1111", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	w.add("cccc3333", "twitch", "https://twitch.tv/someoneelse", extra{channel: "someoneelse"})
	w.remove(mine)
	if got, want := w.get()["twitch"], (Target{Choices: []Choice{{"cccc3333", "#someoneelse"}}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("twitch = %+v", got)
	}
	// a third source arriving doesn't get pinned either: it isn't the only one
	w.add("eeee5555", "twitch", "https://twitch.tv/third", extra{channel: "third"})
	if w.get()["twitch"].Pinned {
		t.Fatal("third pinned")
	}
	// removing a source that isn't pinned changes nothing
	w2 := newWorld()
	w2.add("aaaa1111", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	other := w2.add("cccc3333", "twitch", "https://twitch.tv/someoneelse", extra{channel: "someoneelse"})
	w2.remove(other)
	if id := w2.get()["twitch"].SourceID; id != "aaaa1111" {
		t.Fatalf("sourceId = %q", id)
	}
}

func TestAnExplicitPickMovesTheTargetOnlyToARealSourceOfThatPlatform(t *testing.T) {
	w := newWorld()
	w.add("aaaa1111", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	w.add("cccc3333", "twitch", "https://twitch.tv/second", extra{channel: "second"})
	w.add("bbbb2222", "youtube", "https://youtube.com/@example/live", extra{})
	if !w.set("twitch", "cccc3333") || deref(w.get()["twitch"].Channel) != "second" || w.changes != 1 {
		t.Fatal("pick didn't move the target")
	}
	if w.set("twitch", "bbbb2222") || w.set("twitch", "nope") || w.set("discord", "aaaa1111") {
		t.Fatal("a bad pick was taken") // a YouTube source, no source, no platform
	}
	if deref(w.get()["twitch"].Channel) != "second" || w.changes != 1 {
		t.Fatal("a bad pick changed something")
	}
}

func TestTheKeyFollowsTheBroadcast(t *testing.T) {
	w := newWorld()
	y := w.add("bbbb2222", "youtube", "https://youtube.com/@example/live", extra{channel: "Example Channel"})
	before := w.get()["youtube"].Key
	w.sources[y].Reader.VideoID = "FAKEvideo01"
	after := w.get()["youtube"]
	if after.Key == before || after.Key != "bbbb2222:FAKEvideo01" || after.Label != "Example Channel · video FAKEvideo01" {
		t.Fatalf("after = %+v", after)
	}
}

func TestTheChoiceIsSavedByURLAndRestoredOnTheNextStart(t *testing.T) {
	w := newWorld()
	w.add("aaaa1111", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	w.add("cccc3333", "twitch", "https://twitch.tv/second", extra{channel: "second"})
	w.set("twitch", "cccc3333")
	if b, _ := json.Marshal(w.pins.ToConfig()); string(b) != `{"twitch":"https://twitch.tv/second"}` {
		t.Fatalf("toConfig = %s", b)
	}
	next := newWorld() // new run: new ids, same URLs
	next.add("ffff0001", "twitch", "https://twitch.tv/examplechannel", extra{channel: "examplechannel"})
	next.add("ffff0002", "twitch", "https://twitch.tv/second", extra{channel: "second"})
	next.pins.Restore(map[string]any{"twitch": "https://twitch.tv/second", "youtube": "https://youtube.com/gone"})
	if id := next.get()["twitch"].SourceID; id != "ffff0002" {
		t.Fatalf("restored %q", id)
	}
	next.pins.Restore(nil)
	next.pins.Restore(map[string]any{"twitch": float64(42)})
	if id := next.get()["twitch"].SourceID; id != "ffff0002" {
		t.Fatalf("after junk %q", id)
	}
}

func TestAYouTubeTargetCarriesWhoseStreamItIs(t *testing.T) {
	w := newWorld()
	w.add("bbbb2222", "youtube", "https://youtube.com/@example/live", extra{channel: "Example Channel", videoID: "FAKEvideo01", owner: "UCfakefakefakefakefakefa"})
	if o := deref(w.get()["youtube"].OwnerID); o != "UCfakefakefakefakefakefa" {
		t.Fatalf("ownerId = %v", o)
	}
	v := newWorld()
	v.add("bbbb2222", "youtube", "https://youtube.com/@example/live", extra{channel: "Example Channel", videoID: "FAKEvideo01"})
	if v.get()["youtube"].OwnerID != nil {
		t.Fatal("ownerId should be null")
	}
}

// name()'s fallbacks: a Twitch source whose reader hasn't said falls back to
// the source's channel, then "?"; a YouTube source still called "youtube"
// shows its URL.
func TestNames(t *testing.T) {
	for _, c := range []struct {
		s    Source
		want string
	}{
		{Source{Platform: "twitch", Channel: "fromstatus"}, "#fromstatus"},
		{Source{Platform: "twitch"}, "#?"},
		{Source{Platform: "youtube", Channel: "youtube", URL: "https://youtube.com/@x"}, "https://youtube.com/@x"},
		{Source{Platform: "youtube", URL: "https://youtube.com/@x"}, "https://youtube.com/@x"},
	} {
		if got := name(c.s); got != c.want {
			t.Errorf("name(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}
