package youtubesend

// Stream info beyond the source's cases: each answer word for word, and the
// edges of what Google sends back.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/server"
)

func TestInfoStatusSaysWhatIsMissing(t *testing.T) {
	r := setup(t, false)
	if st := r.sender.InfoStatus(); !st.Configured || st.SignedIn || st.Ready || st.Detail != "Not signed in to YouTube." {
		t.Fatalf("signed out: %+v", st)
	}
	r.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": ""}, nil)
	if st := r.sender.InfoStatus(); st.Configured || st.Detail != "YouTube is off until a Google client is set up — open /setup." {
		t.Fatalf("no client: %+v", st)
	}
	if q := plainMap(t, r.sender.InfoStatus().Quota); !reflect.DeepEqual(q, map[string]any{"load": 2.0, "saveTitle": 3.0, "saveTags": 52.0, "dailyDefault": 10000.0, "exhausted": false}) {
		t.Fatalf("quota %v", q)
	}
	if l := plainMap(t, r.sender.InfoStatus().Limits); !reflect.DeepEqual(l, map[string]any{"title": 100.0, "tagsTotal": 500.0}) {
		t.Fatalf("limits %v", l)
	}
}

func TestInfoStatusAfterGoogleEndedTheSignIn(t *testing.T) {
	r := setup(t, true)
	withBroadcasts(t, r)
	r.g.state(func() { r.g.refresh, r.g.access = "ROTATED_AWAY", "SOMETHING_ELSE" })
	got := r.getInfo(t, "")
	if got["code"] != "not_signed_in" || got["reason"] != Expired {
		t.Fatalf("get: %v", got)
	}
	if st := r.sender.InfoStatus(); st.Detail != Expired {
		t.Fatalf("status %+v", st)
	}
}

func TestInfoNotSignedInNeverReachesGoogle(t *testing.T) {
	r := setup(t, false)
	withBroadcasts(t, r)
	want := map[string]any{"ok": false, "code": "not_signed_in", "reason": "Not signed in to YouTube."}
	calls := r.g.total()
	for name, got := range map[string]map[string]any{
		"get":        r.getInfo(t, ""),
		"set":        r.setInfo(t, server.InfoChange{Title: "x", Expect: strp("FAKElive001")}),
		"categories": r.categories(t),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", name, got)
		}
	}
	if r.g.total() != calls {
		t.Fatal("a signed-out call reached Google")
	}
}

func TestInfoSignedOutWinsOverTheQuotaHold(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() {
		w.active = []any{bc("FAKElive001", "Live title", nil)}
		w.refuse = func(call) reply { return gErr(403, "quotaExceeded", "quota") }
	})
	if got := r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"old", "two words"}, Expect: strp("FAKElive001")}); got["code"] != "quota_exhausted" {
		t.Fatalf("set: %v", got)
	}
	r.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil)
	if got := r.getInfo(t, ""); got["code"] != "not_signed_in" {
		t.Fatalf("get: %v", got)
	}
}

func TestInfoGoogleUnreachableIsAPlainAnswer(t *testing.T) {
	r := setup(t, true)
	withBroadcasts(t, r)
	r.sender.ep.API = "http://127.0.0.1:1"
	want := map[string]any{"ok": false, "code": "unreachable", "reason": "Couldn't reach YouTube."}
	for name, got := range map[string]map[string]any{
		"get":        r.getInfo(t, ""),
		"set":        r.setInfo(t, server.InfoChange{Title: "x", Expect: strp("FAKElive001")}),
		"categories": r.categories(t),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestInfoChecksWordForWord(t *testing.T) {
	r := setup(t, false)
	for _, c := range []struct {
		title string
		tags  []string
		want  string
	}{
		{"", nil, "YouTube needs a title."},
		{strings.Repeat("é", 100), nil, ""},
		{strings.Repeat("😀", 101), nil, "YouTube titles are 100 characters at most."},
		{"a > b", nil, "YouTube titles can't contain < or >."},
		{"a < b", nil, "YouTube titles can't contain < or >."},
		{"x", []string{"ok", "a<b"}, "YouTube tags can't contain < > or a comma."},
		{"x", []string{"a>b"}, "YouTube tags can't contain < > or a comma."},
		{"x", []string{"a,b"}, "YouTube tags can't contain < > or a comma."},
		{"x", []string{strings.Repeat("y", 498), "z"}, ""},
		{"x", []string{strings.Repeat("y", 497), "a\tb"}, "YouTube tags add up to 500 characters at most."},
	} {
		if got := r.sender.CheckInfo(c.title, c.tags); got != c.want {
			t.Errorf("CheckInfo(%q…, %d tags) = %q, want %q", c.title[:min(len(c.title), 8)], len(c.tags), got, c.want)
		}
	}
	for tags, want := range map[string]int{"": 0, "a": 1, "a\tb": 5, "a b": 5, "a,é": 3} {
		list := []string{}
		if tags != "" {
			list = strings.SplitN(tags, ",", -1)
		}
		if got := TagsLength(list); got != want {
			t.Errorf("TagsLength(%q) = %d, want %d", list, got, want)
		}
	}
}

func TestInfoReadsWhatGoogleSendsBack(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	info := func() map[string]any {
		t.Helper()
		got := r.getInfo(t, "")
		i, _ := got["info"].(map[string]any)
		if i == nil {
			t.Fatalf("get: %v", got)
		}
		return i
	}
	// the video's category wins; the broadcast's when the video has none
	r.edit(func() { w.active = []any{bc("FAKElive001", "", map[string]any{"categoryId": "10"})} })
	if i := info(); !reflect.DeepEqual(i["category"], map[string]any{"id": "22", "name": ""}) || i["title"] != "" ||
		!reflect.DeepEqual(i["choices"], []any{map[string]any{"id": "FAKElive001", "label": "Untitled (live)"}}) {
		t.Fatalf("info %v", i)
	}
	r.edit(func() {
		w.videos = func(id string) any {
			return map[string]any{"id": id, "snippet": map[string]any{"tags": []any{1.0, "a", nil}}}
		}
	})
	if i := info(); !reflect.DeepEqual(i["category"], map[string]any{"id": "10", "name": ""}) || !reflect.DeepEqual(i["tags"], []any{"1", "a", "null"}) {
		t.Fatalf("info %v", i)
	}
	r.edit(func() { w.videos = nil })
	if i := info(); !reflect.DeepEqual(i["category"], map[string]any{"id": "10", "name": ""}) {
		t.Fatalf("info %v", i)
	}
	r.edit(func() { w.active = []any{bc("FAKElive001", "t", nil)} })
	if i := info(); i["category"] != nil {
		t.Fatalf("info %v", i)
	}

	// a broadcast without a snippet isn't one; live but none usable is none
	r.edit(func() {
		w.active = []any{map[string]any{"id": "FAKEnosnip1"}}
		w.upcoming = []any{bc("FAKEsooner1", "Sooner", nil)}
	})
	if got := r.getInfo(t, ""); got["code"] != "no_broadcast" {
		t.Fatalf("no snippet: %v", got)
	}
	// the source's comparator reads (null).snippet once there are two to sort
	r.edit(func() {
		w.active = nil
		w.upcoming = []any{nil, bc("FAKEsooner1", "Sooner", nil)}
	})
	if got := r.getInfo(t, ""); got["code"] != "unreachable" {
		t.Fatalf("null among two: %v", got)
	}
	r.edit(func() { w.upcoming = []any{nil} })
	if got := r.getInfo(t, ""); got["code"] != "no_broadcast" {
		t.Fatalf("null alone: %v", got)
	}
	r.edit(func() { w.upcoming = []any{"x", bc("FAKEsooner1", "Sooner", nil)} })
	if i := info(); i["key"] != "FAKEsooner1" {
		t.Fatalf("info %v", i)
	}
}

func TestInfoSavesSendBackWhatTheyRead(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() {
		w.active = []any{map[string]any{"id": "FAKElive001", "snippet": map[string]any{"scheduledStartTime": nil}}}
		w.videos = func(id string) any {
			return map[string]any{"id": id, "snippet": map[string]any{"categoryId": "22", "tags": []any{"old"}, "defaultAudioLanguage": "en-US"}}
		}
	})
	expect := strp("FAKElive001")
	if got := r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"new"}, Expect: expect}); got["ok"] != true {
		t.Fatalf("tags: %v", got)
	}
	if got := r.puts()[0].raw; got != `{"id":"FAKElive001","snippet":{"title":"x","categoryId":"22","tags":["new"],"description":"","defaultAudioLanguage":"en-US"}}` {
		t.Fatalf("video body %s", got)
	}
	// unchanged tags: the broadcast, with a null start kept and no category to send
	if got := r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"old"}, Expect: expect}); !reflect.DeepEqual(got, map[string]any{"ok": true, "cost": 1.0}) {
		t.Fatalf("title: %v", got)
	}
	if got := r.puts()[1].raw; got != `{"id":"FAKElive001","snippet":{"title":"x","description":"","scheduledStartTime":null}}` {
		t.Fatalf("broadcast body %s", got)
	}
	r.edit(func() { w.active = []any{map[string]any{"id": "FAKElive001", "snippet": map[string]any{"title": "t"}}} })
	r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"old"}, Expect: expect, CategoryID: strp("")})
	if got := r.puts()[2].raw; got != `{"id":"FAKElive001","snippet":{"title":"x","description":""}}` {
		t.Fatalf("broadcast body %s", got)
	}
	// an empty category on the video is none
	r.edit(func() {
		w.videos = func(id string) any { return map[string]any{"id": id, "snippet": map[string]any{"categoryId": ""}} }
	})
	if got := r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"new"}, Expect: expect}); got["code"] != "no_category" ||
		got["reason"] != "YouTube needs a category to save tags — pick one." {
		t.Fatalf("no category: %v", got)
	}
	// no video and no tags: nothing to say about tags
	r.edit(func() { w.videos = nil })
	if got := r.setInfo(t, server.InfoChange{Title: "x", Expect: expect}); !reflect.DeepEqual(got, map[string]any{"ok": true, "cost": 1.0}) {
		t.Fatalf("no video, no tags: %v", got)
	}
	// the tag save refused, in Google's words
	r.edit(func() {
		w.videos = defaultVideo
		w.refuse = func(call) reply { return gErr(400, "invalidTags", "The tags are invalid.") }
	})
	if got := r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"new"}, Expect: expect}); got["code"] != "invalidTags" || got["reason"] != "The tags are invalid." {
		t.Fatalf("refused: %v", got)
	}
	// the page was showing something else
	r.edit(func() { w.active = []any{map[string]any{"id": "FAKEnewlive", "snippet": map[string]any{}}} })
	if got := r.setInfo(t, server.InfoChange{Title: "x", Expect: expect}); got["reason"] != `The YouTube broadcast is now "Untitled" — check it and save again.` {
		t.Fatalf("changed: %v", got)
	}
}

func TestInfoCategoryNamesFallBackToTheID(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() {
		w.cats = []any{nil, map[string]any{"id": "1", "snippet": map[string]any{}}, map[string]any{"id": "2"}, map[string]any{"snippet": map[string]any{"title": "no id"}}}
	})
	if got := r.categories(t); !reflect.DeepEqual(got["categories"], []any{map[string]any{"id": "1", "name": "1"}}) {
		t.Fatalf("categories: %v", got)
	}
	// asked once, even when quota runs out later
	r.edit(func() {
		w.active = []any{bc("FAKElive001", "t", nil)}
		w.refuse = func(call) reply { return gErr(403, "quotaExceeded", "quota") }
	})
	r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"new"}, Expect: strp("FAKElive001")})
	if got := r.categories(t); got["ok"] != true {
		t.Fatalf("cached: %v", got)
	}
}
