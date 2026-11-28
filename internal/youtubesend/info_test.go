package youtubesend

// Stream info, test/youtube-send.test.js 24–33 at 29a9d2c.

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/server"
)

func plainMap(t *testing.T, o *jsjson.Object) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(jsjson.Stringify(o, ""), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (r *rig) getInfo(t *testing.T, want string) map[string]any {
	t.Helper()
	o, err := r.sender.GetInfo(t.Context(), want)
	if err != nil {
		t.Fatal(err)
	}
	return plainMap(t, o)
}

func (r *rig) setInfo(t *testing.T, c server.InfoChange) map[string]any {
	t.Helper()
	o, err := r.sender.SetInfo(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return plainMap(t, o)
}

func (r *rig) categories(t *testing.T) map[string]any {
	t.Helper()
	o, err := r.sender.Categories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return plainMap(t, o)
}

func strp(s string) *string { return &s }

func callKeys(calls []call) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, c.key)
	}
	return out
}

// 24
func TestInfoFindsTheLiveBroadcastAndItsTagsInTwoCalls(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() { w.active = []any{bc("FAKElive001", "Live title", map[string]any{"categoryId": "22"})} })
	before := len(r.g.apiCalls())
	got := r.getInfo(t, "")
	want := map[string]any{
		"title": "Live title", "category": map[string]any{"id": "22", "name": ""}, "tags": []any{"old", "two words"}, "tagsKnown": true,
		"where": "YouTube (live)", "key": "FAKElive001", "choices": []any{map[string]any{"id": "FAKElive001", "label": "Live title (live)"}},
	}
	if got["ok"] != true || !reflect.DeepEqual(got["info"], want) {
		t.Fatalf("info: %v", got)
	}
	calls := r.g.apiCalls()[before:]
	if !reflect.DeepEqual(callKeys(calls), []string{"GET /youtube/v3/liveBroadcasts", "GET /youtube/v3/videos"}) {
		t.Fatalf("calls %v", callKeys(calls))
	}
	if !reflect.DeepEqual(calls[0].query, url.Values{"part": {"id,snippet"}, "broadcastStatus": {"active"}, "broadcastType": {"all"}, "maxResults": {"50"}}) {
		t.Fatalf("broadcasts query %v", calls[0].query)
	}
	if !reflect.DeepEqual(calls[1].query, url.Values{"part": {"snippet"}, "id": {"FAKElive001"}}) {
		t.Fatalf("videos query %v", calls[1].query)
	}
	st := r.sender.InfoStatus()
	limits, quota := plainMap(t, st.Limits), plainMap(t, st.Quota)
	if !st.Ready || st.NeedsPermission || limits["title"] != 100.0 || quota["saveTitle"] != 3.0 || quota["saveTags"] != 52.0 {
		t.Fatalf("status %+v %v %v", st, limits, quota)
	}
}

// 25
func TestInfoNothingLiveMeansTheSoonestUpcomingNothingAtAllSaysSoAndStops(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() {
		w.upcoming = []any{
			bc("FAKElater01", "Later", map[string]any{"scheduledStartTime": "2026-12-01T18:00:00Z"}),
			bc("FAKEsooner1", "Sooner", map[string]any{"scheduledStartTime": "2026-11-24T18:00:00Z"}),
		}
	})
	info := r.getInfo(t, "")["info"].(map[string]any)
	if info["key"] != "FAKEsooner1" || info["where"] != "YouTube (upcoming)" {
		t.Fatalf("info %v", info)
	}
	var ids []any
	for _, c := range info["choices"].([]any) {
		ids = append(ids, c.(map[string]any)["id"])
	}
	if !reflect.DeepEqual(ids, []any{"FAKEsooner1", "FAKElater01"}) {
		t.Fatalf("choices %v", ids)
	}
	// the page can ask for the other one
	if got := r.getInfo(t, "FAKElater01")["info"].(map[string]any)["key"]; got != "FAKElater01" {
		t.Fatalf("want: %v", got)
	}

	r.edit(func() { w.upcoming = nil })
	before := len(r.g.apiCalls())
	got := r.getInfo(t, "")
	if got["ok"] != false || got["code"] != "no_broadcast" || !strings.Contains(got["reason"].(string), "No live or upcoming broadcast") {
		t.Fatalf("none: %v", got)
	}
	var statuses []string
	for _, c := range r.g.apiCalls()[before:] {
		statuses = append(statuses, c.query.Get("broadcastStatus"))
	}
	if !reflect.DeepEqual(statuses, []string{"active", "upcoming"}) {
		t.Fatalf("asked %v", statuses)
	}
	if got := r.setInfo(t, server.InfoChange{Title: "x", Expect: strp("FAKEsooner1")}); got["code"] != "no_broadcast" {
		t.Fatalf("save: %v", got)
	}
	if n := len(r.puts()); n != 0 {
		t.Fatalf("%d puts", n)
	}
}

// 26
func TestInfoTitleAndCategoryGoThroughTheBroadcastFor1Unit(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() {
		w.active = []any{bc("FAKElive001", "Live title", map[string]any{"scheduledEndTime": "2026-11-23T22:00:00Z", "categoryId": "22"})}
	})
	got := r.setInfo(t, server.InfoChange{Title: "Fixing an example board", CategoryID: strp("28"), Tags: []string{"old", "two words"}, Expect: strp("FAKElive001")})
	if !reflect.DeepEqual(got, map[string]any{"ok": true, "cost": 1.0}) {
		t.Fatalf("save: %v", got)
	}
	puts := r.puts()
	if len(puts) != 1 || puts[0].key != "PUT /youtube/v3/liveBroadcasts" || !reflect.DeepEqual(puts[0].query, url.Values{"part": {"snippet"}}) {
		t.Fatalf("puts %v", callKeys(puts))
	}
	if want := `{"id":"FAKElive001","snippet":{"title":"Fixing an example board","description":"Example description for FAKElive001",` +
		`"scheduledStartTime":"2026-11-23T18:00:00Z","scheduledEndTime":"2026-11-23T22:00:00Z","categoryId":"28"}}`; puts[0].raw != want {
		t.Fatalf("body %s", puts[0].raw)
	}
	// no category picked: the one it has is sent back, not dropped
	r.setInfo(t, server.InfoChange{Title: "Again", Tags: []string{"old", "two words"}, Expect: strp("FAKElive001")})
	if got := r.puts()[1].body["snippet"].(map[string]any)["categoryId"]; got != "22" {
		t.Fatalf("category %v", got)
	}
}

// 27
func TestInfoChangedTagsGoThroughTheVideoFor50UnitsKeepingItsDescription(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() { w.active = []any{bc("FAKElive001", "Live title", nil)} })
	got := r.setInfo(t, server.InfoChange{Title: "Fixing an example board", Tags: []string{"repair", "soldering"}, Expect: strp("FAKElive001")})
	if !reflect.DeepEqual(got, map[string]any{"ok": true, "cost": 50.0}) {
		t.Fatalf("save: %v", got)
	}
	puts := r.puts()
	if len(puts) != 1 || puts[0].key != "PUT /youtube/v3/videos" || !reflect.DeepEqual(puts[0].query, url.Values{"part": {"snippet"}}) {
		t.Fatalf("puts %v", callKeys(puts))
	}
	if want := `{"id":"FAKElive001","snippet":{"title":"Fixing an example board","categoryId":"22","tags":["repair","soldering"],` +
		`"description":"Example video description","defaultLanguage":"en"}}`; puts[0].raw != want {
		t.Fatalf("body %s", puts[0].raw)
	}
	// a video with no category yet, and none picked: refused before the 50 units
	r.edit(func() {
		w.videos = func(id string) any {
			return map[string]any{"id": id, "snippet": map[string]any{"title": "t", "tags": []any{}}}
		}
	})
	if got := r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"new"}, Expect: strp("FAKElive001")}); got["code"] != "no_category" {
		t.Fatalf("no category: %v", got)
	}
	if n := len(r.puts()); n != 1 {
		t.Fatalf("%d puts", n)
	}
}

// 28
func TestInfoASaveOnlyLandsOnTheBroadcastThePageWasShowing(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() { w.active = []any{bc("FAKEnewlive", "A different stream", nil)} })
	for _, expect := range []*string{strp("FAKEoldlive"), nil, strp("")} {
		got := r.setInfo(t, server.InfoChange{Title: "x", Expect: expect})
		if got["code"] != "target_changed" || !strings.Contains(got["reason"].(string), "A different stream") {
			t.Fatalf("expect %v: %v", expect, got)
		}
	}
	if n := len(r.puts()); n != 0 {
		t.Fatalf("%d puts", n)
	}
}

// 29
func TestInfoNoVideoUnderTheBroadcastsIDTitleSavedTagsLeftAlone(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() {
		w.videos = nil
		w.active = []any{bc("FAKElive001", "Live title", nil)}
	})
	info := r.getInfo(t, "")["info"].(map[string]any)
	if !reflect.DeepEqual(info["tags"], []any{}) || info["tagsKnown"] != false {
		t.Fatalf("info %v", info)
	}
	got := r.setInfo(t, server.InfoChange{Title: "New title", Tags: []string{"repair"}, Expect: strp("FAKElive001")})
	if got["ok"] != true || !strings.Contains(got["note"].(string), "Tags were not") {
		t.Fatalf("save: %v", got)
	}
	if k := callKeys(r.puts()); !reflect.DeepEqual(k, []string{"PUT /youtube/v3/liveBroadcasts"}) {
		t.Fatalf("puts %v", k)
	}
	// a video that answers under some other ID is not this broadcast's
	r.edit(func() {
		w.videos = func(string) any {
			return map[string]any{"id": "FAKEother01", "snippet": map[string]any{"tags": []any{"x"}}}
		}
	})
	if got := r.getInfo(t, "")["info"].(map[string]any)["tagsKnown"]; got != false {
		t.Fatalf("tagsKnown %v", got)
	}
}

// 30
func TestInfoGooglesRefusalsInItsWordsQuotaGoneStopsEverythingUntilMidnight(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() { w.active = []any{bc("FAKElive001", "Live title", nil)} })
	form := server.InfoChange{Title: "x", Tags: []string{"old", "two words"}, Expect: strp("FAKElive001")}
	r.edit(func() {
		w.refuse = func(call) reply {
			return gErr(400, "invalidScheduledStartTime", "The scheduled start time is invalid.")
		}
	})
	got := r.setInfo(t, form)
	if got["code"] != "invalidScheduledStartTime" || got["reason"] != "The scheduled start time is invalid." {
		t.Fatalf("refused: %v", got)
	}

	r.edit(func() {
		w.refuse = func(call) reply {
			return gErr(403, "quotaExceeded", "The request cannot be completed because you have exceeded your quota.")
		}
	})
	got = r.setInfo(t, form)
	if ra, _ := got["retryAfter"].(float64); got["code"] != "quota_exhausted" || !(ra > 0) {
		t.Fatalf("quota: %v", got)
	}
	before := len(r.g.apiCalls())
	if got := r.getInfo(t, ""); got["code"] != "quota_exhausted" {
		t.Fatalf("get: %v", got)
	}
	if got := r.setInfo(t, form); got["code"] != "quota_exhausted" {
		t.Fatalf("set: %v", got)
	}
	if got := r.categories(t); got["code"] != "quota_exhausted" {
		t.Fatalf("categories: %v", got)
	}
	if n := len(r.g.apiCalls()); n != before {
		t.Fatalf("called while out of quota: %d → %d", before, n)
	}
	if plainMap(t, r.sender.InfoStatus().Quota)["exhausted"] != true {
		t.Fatal("exhausted")
	}
	r.clock.add(time.Duration(MsUntilPacificMidnight(float64(r.clock.ms.Load())))*time.Millisecond + time.Second)

	// a dead sign-in on the read
	r.g.state(func() {
		r.g.refresh = "ROTATED_AWAY"
		r.g.access = "SOMETHING_ELSE"
	})
	if got := r.getInfo(t, ""); got["code"] != "not_signed_in" {
		t.Fatalf("get: %v", got)
	}
	if r.sender.Status().SignedIn {
		t.Fatal("still signed in")
	}
	if got := r.setInfo(t, form); got["code"] != "not_signed_in" {
		t.Fatalf("set: %v", got)
	}
}

// 31
func TestInfoCategoriesAreAskedForOnceOnlyTheAssignableOnes(t *testing.T) {
	r := setup(t, true)
	withBroadcasts(t, r)
	got := r.categories(t)
	want := []any{map[string]any{"id": "28", "name": "Science & Technology"}, map[string]any{"id": "20", "name": "Gaming"}}
	if got["ok"] != true || !reflect.DeepEqual(got["categories"], want) {
		t.Fatalf("categories: %v", got)
	}
	r.categories(t)
	calls := r.g.find(func(c call) bool { return c.key == "GET /youtube/v3/videoCategories" })
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].query, url.Values{"part": {"snippet"}, "regionCode": {"US"}}) {
		t.Fatalf("calls %d %v", len(calls), calls)
	}
}

// 32
func TestInfoRulesCheckedBeforeAnyQuotaAndTheEstimateCountsWhatWasSpent(t *testing.T) {
	r := setup(t, true)
	s := r.sender
	title, tags := strings.Repeat("x", 100), []string{"repair", "two words"}
	check := func(title string, tags []string, want string) {
		t.Helper()
		got := s.CheckInfo(title, tags)
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("CheckInfo(%d chars, %d tags) = %q, want %q", len(title), len(tags), got, want)
		}
	}
	check(title, tags, "")
	check("", tags, "needs a title")
	check(strings.Repeat("x", 101), tags, "100")
	check("a <b> c", tags, "< or >")
	check(title, []string{"a,b"}, "comma")
	// the reference's own example: Foo-Baz is 7, "Foo Baz" is 9 with its quotes, plus the comma between
	if n := TagsLength([]string{"Foo-Baz", "Foo Baz"}); n != 17 {
		t.Errorf("TagsLength = %d", n)
	}
	check(title, []string{strings.Repeat("x", 500)}, "")
	check(title, []string{strings.Repeat("x", 499), "y"}, "500")

	w := withBroadcasts(t, r)
	r.edit(func() { w.active = []any{bc("FAKElive001", "Live title", nil)} })
	used := func() float64 { return r.quota(t)["usedTodayEstimate"].(float64) }
	start := used()
	r.getInfo(t, "")
	if d := used() - start; d != 2 {
		t.Fatalf("read spent %v", d)
	}
	r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"old", "two words"}, Expect: strp("FAKElive001")})
	if d := used() - start; d != 2+3 {
		t.Fatalf("title save spent %v", d-2)
	}
	r.setInfo(t, server.InfoChange{Title: "x", Tags: []string{"new"}, Expect: strp("FAKElive001")})
	if d := used() - start; d != 2+3+52 {
		t.Fatalf("tag save spent %v", d-5)
	}
}

// 33
func TestStreamInfoOverHTTPReadPickACategorySaveToTheShownBroadcast(t *testing.T) {
	r := setup(t, true)
	w := withBroadcasts(t, r)
	r.edit(func() { w.active = []any{bc("FAKElive001", "Live title", nil)} })
	var seen []string
	get := func(p string) response { res := r.app.get(t, p); seen = append(seen, res.text); return res }
	if st := get("/api/info/state"); st.json["platforms"].(map[string]any)["youtube"].(map[string]any)["ready"] != true {
		t.Fatalf("state %s", st.text)
	}
	cur := get("/api/info/current?platform=youtube")
	key := cur.json["info"].(map[string]any)["key"]
	if key != "FAKElive001" {
		t.Fatalf("current %s", cur.text)
	}
	if cats := get("/api/info/categories?platform=youtube"); len(cats.json["categories"].([]any)) != 2 {
		t.Fatalf("categories %s", cats.text)
	}
	res := r.app.post(t, "/api/info/save", map[string]any{
		"platforms": []any{"youtube"}, "title": "Fixing an example board", "tags": []any{"old", "two words"},
		"category": map[string]any{"youtube": map[string]any{"id": "28", "name": "Science & Technology"}}, "expect": map[string]any{"youtube": key},
	}, nil)
	seen = append(seen, res.text)
	if b, _ := json.Marshal(res.json["results"]); string(b) != `{"youtube":{"cost":1,"ok":true}}` {
		t.Fatalf("save %s", res.text)
	}
	if got := r.puts()[0].body["snippet"].(map[string]any)["categoryId"]; got != "28" {
		t.Fatalf("category %v", got)
	}
	// a page showing a broadcast that has since been replaced
	r.edit(func() { w.active = []any{bc("FAKEnewlive", "A different stream", nil)} })
	stale := r.app.post(t, "/api/info/save", map[string]any{"platforms": []any{"youtube"}, "title": "x", "tags": []any{}, "expect": map[string]any{"youtube": "FAKElive001"}}, nil)
	if code := stale.json["results"].(map[string]any)["youtube"].(map[string]any)["code"]; code != "target_changed" {
		t.Fatalf("stale %s", stale.text)
	}
	if n := len(r.puts()); n != 1 {
		t.Fatalf("%d puts", n)
	}
	all := strings.Join(seen, "\n")
	for _, secret := range []string{"FAKE_YT_ACCESS", "FAKE_YT_REFRESH", "FAKE_CLIENT_SECRET"} {
		if strings.Contains(all, secret) {
			t.Errorf("%s in a response", secret)
		}
	}
}
