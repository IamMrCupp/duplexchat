package server

// multichat test/infoapi.test.js at 29a9d2c: the stream info panel's HTTP
// surface against fake senders. What a save sends, what it refuses, the
// gate, and presets. (Case 14, the cleaners on their own, is in
// internal/info.)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/info"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// fakeInfo is helpers.js fakeInfoSender: signed in as example_user, keeps
// what it was asked to save, answers however the test says.
type fakeInfo struct {
	fakeSender
	needsPermission bool
	limitTitle      int
	reply           func(InfoChange) (*jsjson.Object, error)
	start           *jsjson.Object

	smu   sync.Mutex
	saves []InfoChange
}

type fakeSearch struct{ *fakeInfo }
type fakeList struct{ *fakeInfo }

func (f fakeSearch) SearchCategories(_ context.Context, q string) (*jsjson.Object, error) {
	return parseObj(fmt.Sprintf(`{"ok":true,"categories":[{"id":"509670","name":"Example Category %s"}]}`, q)), nil
}

func (f fakeList) Categories(context.Context) (*jsjson.Object, error) {
	return parseObj(`{"ok":true,"categories":[{"id":"28","name":"Example Science"}]}`), nil
}

func parseObj(s string) *jsjson.Object {
	v, err := jsjson.Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return v.(*jsjson.Object)
}

func newFakeInfo(limitTitle int) *fakeInfo {
	return &fakeInfo{fakeSender: fakeSender{signedIn: true, as: "example_user"}, limitTitle: limitTitle}
}

func (f *fakeInfo) InfoStatus() InfoStatus {
	st := InfoStatus{Configured: true, SignedIn: f.signedIn, NeedsPermission: f.needsPermission,
		Ready: f.signedIn && !f.needsPermission, Limits: jsjson.NewObject().Set("title", f.limitTitle)}
	if f.signedIn {
		st.As = "example_user"
	} else {
		st.Detail = "Not signed in."
	}
	return st
}

func (f *fakeInfo) CheckInfo(title string, _ []string) string {
	if utf8.RuneCountInString(title) > f.limitTitle {
		return fmt.Sprintf("Titles are %d characters at most.", f.limitTitle)
	}
	if title == "" {
		return "A title is needed."
	}
	return ""
}

func (f *fakeInfo) GetInfo(_ context.Context, want string) (*jsjson.Object, error) {
	i := f.start
	if i == nil {
		i = parseObj(`{"title":"Old title","category":{"id":"100","name":"Old Category"},"tags":["old"],"where":"#examplechannel"}`)
	}
	i = i.Copy()
	if want != "" {
		i.Set("key", want)
	}
	return jsjson.NewObject().Set("ok", true).Set("info", i), nil
}

func (f *fakeInfo) SetInfo(_ context.Context, c InfoChange) (*jsjson.Object, error) {
	f.smu.Lock()
	f.saves = append(f.saves, c)
	f.smu.Unlock()
	if f.reply != nil {
		return f.reply(c)
	}
	return jsjson.NewObject().Set("ok", true), nil
}

func (f *fakeInfo) saved() []InfoChange {
	f.smu.Lock()
	defer f.smu.Unlock()
	return append([]InfoChange(nil), f.saves...)
}

// infoApp is helpers.js startApp with the info API: the send API's gate,
// presets kept in memory as server.js keeps them in config.json.
type infoApp struct {
	srv  *Server
	mu   sync.Mutex
	kept any
}

func startInfo(o Options, presets any) *infoApp {
	a := &infoApp{kept: presets}
	o.Pages, o.Port, o.Token = fstest.MapFS{}, 3131, "tok-123"
	if o.Targets == nil {
		o.Targets = fixedTargets
	}
	o.GetPresets = func() any { a.mu.Lock(); defer a.mu.Unlock(); return a.kept }
	o.SetPresets = func(l []info.Preset) {
		b, _ := json.Marshal(l)
		var v any
		json.Unmarshal(b, &v)
		a.mu.Lock()
		a.kept = v
		a.mu.Unlock()
	}
	a.srv = New(o)
	return a
}

func (a *infoApp) keptPresets() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, _ := json.Marshal(a.kept)
	return string(b)
}

type resp struct {
	status int
	json   map[string]any
	text   string
}

// do sends a request as the info page would, with these headers on top; a
// header set to "" is removed.
func (a *infoApp) do(method, path string, headers map[string]string, body string) resp {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:3131"
	r.Header.Set("X-Dock-Token", "tok-123")
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://127.0.0.1:3131")
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		switch {
		case k == "host":
			r.Host = v
		case v == "":
			r.Header.Del(k)
		default:
			r.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, r)
	out := resp{status: rec.Code, text: rec.Body.String()}
	json.Unmarshal(rec.Body.Bytes(), &out.json)
	return out
}

func (a *infoApp) get(path string, headers ...map[string]string) resp {
	var h map[string]string
	if len(headers) > 0 {
		h = headers[0]
	}
	return a.do(http.MethodGet, path, h, "")
}

func (a *infoApp) post(path string, body any, headers ...map[string]string) resp {
	var h map[string]string
	if len(headers) > 0 {
		h = headers[0]
	}
	b, ok := body.(string)
	if !ok {
		raw, _ := json.Marshal(body)
		b = string(raw)
	}
	return a.do(http.MethodPost, path, h, b)
}

func form(extra map[string]any) map[string]any {
	f := map[string]any{"platforms": []any{"twitch", "youtube"}, "title": "Fixing an example board", "tags": []any{"repair", "soldering"}}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

type pair struct {
	app             *infoApp
	twitch, youtube *fakeInfo
}

func both(o Options, presets any, tw, yt func(*fakeInfo)) pair {
	twitch, youtube := newFakeInfo(140), newFakeInfo(100)
	if tw != nil {
		tw(twitch)
	}
	if yt != nil {
		yt(youtube)
	}
	o.Senders = map[string]Sender{"twitch": fakeSearch{twitch}, "youtube": fakeList{youtube}}
	return pair{startInfo(o, presets), twitch, youtube}
}

func strp(s string) *string { return &s }

func TestInfoStateWhoIsReadyWhatTheLimitsArePresetsAndNothingSecret(t *testing.T) {
	presets := []any{map[string]any{"name": "Repair night", "title": "Fixing things", "tags": []any{"repair"}}}
	b := both(Options{}, presets, func(f *fakeInfo) { f.needsPermission = true }, nil)
	r := b.app.get("/api/info/state")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.text)
	}
	pl := r.json["platforms"].(map[string]any)
	tw, yt := pl["twitch"].(map[string]any), pl["youtube"].(map[string]any)
	if fmt.Sprint(tw["available"], tw["signedIn"], tw["needsPermission"], tw["ready"]) != "true true true false" {
		t.Errorf("twitch %v", tw)
	}
	if yt["ready"] != true || yt["limits"].(map[string]any)["title"] != 100.0 || yt["categories"] != "list" || tw["categories"] != "search" {
		t.Errorf("youtube %v, twitch categories %v", yt, tw["categories"])
	}
	if p, _ := json.Marshal(r.json["presets"]); string(p) != `[{"category":{},"name":"Repair night","tags":["repair"],"title":"Fixing things"}]` {
		t.Errorf("presets %s", p)
	}
	if regexp.MustCompile(`(?i)token|secret`).MatchString(r.text) {
		t.Errorf("leaks: %s", r.text)
	}
	// the wire order is the source's
	if !strings.Contains(r.text, `"twitch":{"available":true,"configured":true,"signedIn":true,"as":"example_user","needsPermission":true,"ready":false,"detail":"","limits":{"title":140},"warning":"","categories":"search"}`) {
		t.Errorf("order: %s", r.text)
	}
}

func TestInfoStateASenderThatOnlySendsHasNoStreamInfo(t *testing.T) {
	app := startInfo(Options{Senders: map[string]Sender{"twitch": newFakeSender()}}, nil)
	r := app.get("/api/info/state")
	pl := r.json["platforms"].(map[string]any)
	if pl["twitch"].(map[string]any)["available"] != false || pl["youtube"].(map[string]any)["available"] != false {
		t.Fatalf("%v", pl)
	}
	save := app.post("/api/info/save", form(map[string]any{"platforms": []any{"twitch"}}))
	if c := save.json["results"].(map[string]any)["twitch"].(map[string]any)["code"]; c != "unavailable" {
		t.Fatalf("%v", save.json)
	}
}

func TestInfoCurrentWhatThePlatformHasNowPerPlatform(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	r := b.app.get("/api/info/current?platform=twitch")
	if r.json["ok"] != true || r.json["info"].(map[string]any)["title"] != "Old title" {
		t.Fatalf("%v", r.json)
	}
	r = b.app.get("/api/info/current?platform=youtube&want=FAKEbroadcast2")
	if r.json["info"].(map[string]any)["key"] != "FAKEbroadcast2" {
		t.Fatalf("%v", r.json)
	}
	// a want that isn't an ID never reaches the sender
	r = b.app.get("/api/info/current?platform=youtube&want=..%2Fx")
	if _, has := r.json["info"].(map[string]any)["key"]; has {
		t.Fatalf("%v", r.json)
	}
	if r := b.app.get("/api/info/current?platform=kick"); r.status != 400 {
		t.Fatalf("%d", r.status)
	}
}

func TestInfoCategoriesTwitchIsSearchedYouTubeIsAListAnEmptySearchAsksNobody(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	if r := b.app.get("/api/info/categories?platform=twitch&q=solder"); r.text != `{"ok":true,"categories":[{"id":"509670","name":"Example Category solder"}]}` {
		t.Fatal(r.text)
	}
	if r := b.app.get("/api/info/categories?platform=twitch"); r.text != `{"ok":true,"categories":[]}` {
		t.Fatal(r.text)
	}
	if r := b.app.get("/api/info/categories?platform=youtube"); r.text != `{"ok":true,"categories":[{"id":"28","name":"Example Science"}]}` {
		t.Fatal(r.text)
	}
}

func TestInfoSaveOneFormToBothPlatformsEachToldItsOwnCategoryAndBroadcast(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	r := b.app.post("/api/info/save", form(map[string]any{
		"title": "  Fixing an\nexample board ", "tags": []any{"repair", " Repair ", "", "soldering"},
		"category": map[string]any{"twitch": map[string]any{"id": "509670", "name": "Example Category"}, "youtube": map[string]any{"id": "28", "name": "Example Science"}},
		"expect":   map[string]any{"youtube": "FAKEbroadcast1"},
	}))
	if r.status != 200 || r.text != `{"results":{"twitch":{"ok":true},"youtube":{"ok":true}}}` {
		t.Fatalf("%d %s", r.status, r.text)
	}
	tags := []string{"repair", "soldering"}
	if got := b.twitch.saved(); !reflect.DeepEqual(got, []InfoChange{{Title: "Fixing an example board", Tags: tags, CategoryID: strp("509670")}}) {
		t.Errorf("twitch %+v", got)
	}
	if got := b.youtube.saved(); !reflect.DeepEqual(got, []InfoChange{{Title: "Fixing an example board", Tags: tags, CategoryID: strp("28"), Expect: strp("FAKEbroadcast1")}}) {
		t.Errorf("youtube %+v", got)
	}
}

func TestInfoSaveCategoryLeftAloneClearedOrRefusedAsNonsensePerPlatform(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	b.app.post("/api/info/save", form(map[string]any{"category": map[string]any{"twitch": nil}}))
	if c := b.twitch.saved()[0].CategoryID; c == nil || *c != "" { // null clears
		t.Fatalf("twitch %v", c)
	}
	if c := b.youtube.saved()[0].CategoryID; c != nil { // absent leaves it alone
		t.Fatalf("youtube %v", *c)
	}
	r := b.app.post("/api/info/save", form(map[string]any{"category": map[string]any{"twitch": map[string]any{"id": "not an id!"}, "youtube": map[string]any{"id": "28"}}}))
	res := r.json["results"].(map[string]any)
	if res["twitch"].(map[string]any)["code"] != "bad_category" || res["youtube"].(map[string]any)["ok"] != true || len(b.twitch.saved()) != 1 {
		t.Fatalf("%v, %d saves", res, len(b.twitch.saved()))
	}
}

func TestInfoSaveOneSideFailingDoesNotHideTheOtherSideWorking(t *testing.T) {
	b := both(Options{}, nil, nil, func(f *fakeInfo) {
		f.reply = func(InfoChange) (*jsjson.Object, error) {
			return parseObj(`{"ok":false,"code":"quota_exhausted","reason":"YouTube API quota is used up for today.","retryAfter":3600}`), nil
		}
	})
	r := b.app.post("/api/info/save", form(nil))
	if r.status != 200 || r.text != `{"results":{"twitch":{"ok":true},"youtube":{"ok":false,"code":"quota_exhausted","reason":"YouTube API quota is used up for today.","retryAfter":3600}}}` {
		t.Fatalf("%d %s", r.status, r.text)
	}
	if len(b.twitch.saved()) != 1 || len(b.youtube.saved()) != 1 {
		t.Fatal("not both asked")
	}
}

func TestInfoSaveATitleThatFitsOnePlatformAndNotTheOtherSavesToNeither(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	title := strings.Repeat("x", 120) // fine for the fake Twitch (140), too long for the fake YouTube (100)
	r := b.app.post("/api/info/save", form(map[string]any{"title": title}))
	if r.status != 400 || r.json["code"] != "invalid" || r.json["platform"] != "youtube" || !strings.Contains(r.json["error"].(string), "Nothing was saved") {
		t.Fatalf("%d %s", r.status, r.text)
	}
	if len(b.twitch.saved())+len(b.youtube.saved()) != 0 {
		t.Fatal("saved")
	}
	// the same title to Twitch alone is fine
	r = b.app.post("/api/info/save", form(map[string]any{"title": title, "platforms": []any{"twitch"}}))
	if r.json["results"].(map[string]any)["twitch"].(map[string]any)["ok"] != true {
		t.Fatal(r.text)
	}
}

func TestInfoSaveBadBodies(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	for _, c := range []struct {
		body any
		code string
	}{
		{form(map[string]any{"platforms": []any{}}), "bad_platforms"},
		{form(map[string]any{"platforms": []any{"kick"}}), "bad_platforms"},
		{form(map[string]any{"title": "   "}), "invalid"},
	} {
		if r := b.app.post("/api/info/save", c.body); r.json["code"] != c.code {
			t.Errorf("%v: %s", c.body, r.text)
		}
	}
	for _, raw := range []string{"null", "{nope"} {
		if r := b.app.post("/api/info/save", raw); r.status != 400 {
			t.Errorf("%s: %d", raw, r.status)
		}
	}
	if len(b.twitch.saved()) != 0 {
		t.Fatal("saved")
	}
}

func TestInfoSaveASenderThatThrowsDoesNotLeakItsErrorANoteAndACostComeThrough(t *testing.T) {
	var logged []string
	var lmu sync.Mutex
	b := both(Options{Log: func(k string, v any) { lmu.Lock(); logged = append(logged, k); lmu.Unlock() }}, nil,
		func(f *fakeInfo) {
			f.reply = func(InfoChange) (*jsjson.Object, error) { return nil, errors.New("FAKE_ACCESS_TOKEN in a stack trace") }
		},
		func(f *fakeInfo) {
			f.reply = func(InfoChange) (*jsjson.Object, error) {
				return parseObj(`{"ok":true,"note":"Title saved. Tags were not.","cost":1,"accessToken":"FAKE_SHOULD_NOT_PASS"}`), nil
			}
		})
	r := b.app.post("/api/info/save", form(nil))
	if r.text != `{"results":{"twitch":{"ok":false,"code":"error","reason":"Couldn't reach Twitch."},"youtube":{"ok":true,"note":"Title saved. Tags were not.","cost":1}}}` {
		t.Fatal(r.text)
	}
	if strings.Contains(r.text, "FAKE_") || !reflect.DeepEqual(logged, []string{"info:twitch"}) {
		t.Fatalf("%s, logged %v", r.text, logged)
	}
	// a panic is the same as a throw
	b.twitch.reply = func(InfoChange) (*jsjson.Object, error) { panic("FAKE_ACCESS_TOKEN") }
	if r := b.app.post("/api/info/save", form(map[string]any{"platforms": []any{"twitch"}})); r.text != `{"results":{"twitch":{"ok":false,"code":"error","reason":"Couldn't reach Twitch."}}}` {
		t.Fatal(r.text)
	}
}

func TestInfoGatingEveryRouteRefusesWhatTheSendEndpointRefuses(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	cases := []struct {
		code    string
		headers map[string]string
	}{
		{"no_token", map[string]string{"X-Dock-Token": ""}},
		{"bad_token", map[string]string{"X-Dock-Token": "not-the-token"}},
		{"bad_host", map[string]string{"host": "evil.example:3131"}},
		{"bad_host", map[string]string{"host": "192.168.1.20:3131"}},
		{"bad_origin", map[string]string{"Origin": "https://evil.example"}},
		{"bad_origin", map[string]string{"Origin": "null"}},
		{"bad_origin", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"proxied", map[string]string{"X-Forwarded-For": "203.0.113.9"}},
	}
	posts := []struct {
		path string
		body any
	}{{"/api/info/save", form(nil)}, {"/api/info/presets", map[string]any{"name": "x", "title": "y"}}, {"/api/info/presets/delete", map[string]any{"name": "x"}}, {"/api/info/nothing", map[string]any{}}}
	gets := []string{"/api/info/state", "/api/info/current?platform=twitch", "/api/info/categories?platform=twitch&q=a", "/api/info/nothing"}
	for _, c := range cases {
		for _, p := range posts {
			if r := b.app.post(p.path, p.body, c.headers); r.status != 403 || r.json["code"] != c.code {
				t.Errorf("%s POST %s: %d %s", c.code, p.path, r.status, r.text)
			}
		}
		for _, p := range gets {
			if r := b.app.get(p, c.headers); r.status != 403 || r.json["code"] != c.code {
				t.Errorf("%s GET %s: %d %s", c.code, p, r.status, r.text)
			}
		}
	}
	// a write with no Origin at all, and any route with no token at all
	if r := b.app.post("/api/info/save", form(nil), map[string]string{"Origin": ""}); r.json["code"] != "bad_origin" {
		t.Errorf("no Origin: %s", r.text)
	}
	for _, p := range gets {
		if r := b.app.get(p, map[string]string{"X-Dock-Token": ""}); r.json["code"] != "no_token" {
			t.Errorf("no token %s: %s", p, r.text)
		}
	}

	// the right token from the wrong machine
	lan := startInfo(Options{Senders: map[string]Sender{"twitch": fakeSearch{b.twitch}}, Peer: strp("192.168.1.20")}, nil)
	if r := lan.post("/api/info/save", form(nil)); r.json["code"] != "not_loopback" {
		t.Errorf("lan save: %s", r.text)
	}
	if r := lan.get("/api/info/state"); r.json["code"] != "not_loopback" {
		t.Errorf("lan state: %s", r.text)
	}

	if len(b.twitch.saved()) != 0 || b.app.keptPresets() != "null" {
		t.Fatalf("%d saves, presets %s", len(b.twitch.saved()), b.app.keptPresets())
	}
	// unknown paths under /api/info/ are answered here, gated; others fall through
	if r := b.app.get("/api/info/nothing"); r.status != 404 || r.json["code"] != "not_found" {
		t.Errorf("%d %s", r.status, r.text)
	}
	if r := b.app.get("/api/infox"); r.status != 404 || r.text != "not found" {
		t.Errorf("%d %q", r.status, r.text)
	}
}

func TestInfoACopyThatDoesNotOwnSendingDoesNotTouchStreamInfoEither(t *testing.T) {
	b := both(Options{Disabled: func() string { return "Sending is off here: another copy (pid 4242) owns it." }}, nil, nil, nil)
	st := b.app.get("/api/info/state")
	tw := st.json["platforms"].(map[string]any)["twitch"].(map[string]any)
	if tw["available"] != false || !strings.Contains(tw["detail"].(string), "pid 4242") || !strings.Contains(st.json["notices"].([]any)[0].(string), "pid 4242") {
		t.Fatal(st.text)
	}
	if r := b.app.post("/api/info/save", form(nil)); r.json["results"].(map[string]any)["twitch"].(map[string]any)["code"] != "sending_disabled" {
		t.Fatal(r.text)
	}
	if r := b.app.get("/api/info/current?platform=twitch"); r.json["code"] != "sending_disabled" {
		t.Fatal(r.text)
	}
	if len(b.twitch.saved()) != 0 {
		t.Fatal("saved")
	}
}

func TestInfoPresetsKeptByNameReplacedWhateverTheCaseDeletedCappedAndNeverSentAnywhere(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	r := b.app.post("/api/info/presets", map[string]any{
		"name": "  Repair night ", "title": "Fixing an example board", "tags": []any{"repair", "repair", "soldering"},
		"category": map[string]any{
			"twitch": map[string]any{"id": "509670", "name": "Example Category"}, "youtube": map[string]any{"id": "bad id"}, "kick": map[string]any{"id": "1"},
		},
		"accessToken": "FAKE_SHOULD_NOT_BE_KEPT",
	})
	if r.status != 200 {
		t.Fatal(r.text)
	}
	if got := b.app.keptPresets(); got != `[{"category":{"twitch":{"id":"509670","name":"Example Category"}},"name":"Repair night","tags":["repair","soldering"],"title":"Fixing an example board"}]` {
		t.Fatal(got)
	}
	r = b.app.post("/api/info/presets", map[string]any{"name": "REPAIR NIGHT", "title": "New title"})
	if l := r.json["presets"].([]any); len(l) != 1 || l[0].(map[string]any)["title"] != "New title" {
		t.Fatal(r.text)
	}
	if r := b.app.post("/api/info/presets", map[string]any{"name": "   "}); r.json["code"] != "bad_preset" {
		t.Fatal(r.text)
	}
	if r := b.app.post("/api/info/presets/delete", map[string]any{"name": "repair night"}); r.text != `{"presets":[]}` {
		t.Fatal(r.text)
	}
	for i := 0; i < info.MaxPresets; i++ {
		if r := b.app.post("/api/info/presets", map[string]any{"name": fmt.Sprintf("p%d", i)}); r.status != 200 {
			t.Fatalf("%d: %s", i, r.text)
		}
	}
	if r := b.app.post("/api/info/presets", map[string]any{"name": "one too many"}); r.json["code"] != "too_many" {
		t.Fatal(r.text)
	}
	if r := b.app.post("/api/info/presets", map[string]any{"name": "P0", "title": "replacing is still fine"}); r.status != 200 {
		t.Fatal(r.text)
	}
	if len(b.twitch.saved())+len(b.youtube.saved()) != 0 {
		t.Fatal("sent somewhere")
	}
}

func TestInfoStateRepeatsTheDocksWrongChannelWarning(t *testing.T) {
	twitch, youtube := newFakeInfo(140), newFakeInfo(100)
	youtube.as, youtube.id = "Example Channel (@example2960)", "UCfakefakefakefakefakeAA"
	tg := func() map[string]targets.Target {
		t := fixedTargets()
		y := t["youtube"]
		y.OwnerID = strp("UCfakefakefakefakefakeBB")
		t["youtube"] = y
		return t
	}
	app := startInfo(Options{Senders: map[string]Sender{"twitch": fakeSearch{twitch}, "youtube": fakeList{youtube}}, Targets: tg}, nil)
	r := app.get("/api/info/state")
	pl := r.json["platforms"].(map[string]any)
	if w := pl["youtube"].(map[string]any)["warning"].(string); !strings.Contains(w, "@example2960) — not the channel this YouTube chat belongs to") {
		t.Fatal(w)
	}
	if w := pl["twitch"].(map[string]any)["warning"]; w != "" {
		t.Fatal(w)
	}
}

// What the source's cases leave open, pinned to lib/infoapi.js.

type quotaInfo struct{ fakeList }

func (q quotaInfo) InfoStatus() InfoStatus {
	st := q.fakeInfo.InfoStatus()
	st.Quota = parseObj(`{"usedTodayEstimate":3}`)
	return st
}

// A sender's quota rides along after the warning; a failure without a code
// or reason gets the source's words; a sender with no categories says so.
func TestInfoQuotaDefaultsAndNoCategories(t *testing.T) {
	yt := newFakeInfo(100)
	yt.reply = func(InfoChange) (*jsjson.Object, error) { return parseObj(`{"ok":false,"retryAfter":0}`), nil }
	bare := newFakeInfo(140)
	app := startInfo(Options{Senders: map[string]Sender{"twitch": bare, "youtube": quotaInfo{fakeList{yt}}}}, nil)
	st := app.get("/api/info/state")
	if !strings.Contains(st.text, `"warning":"","quota":{"usedTodayEstimate":3},"categories":"list"}`) ||
		!strings.Contains(st.text, `"twitch":{"available":true,`) || !strings.Contains(st.text, `"categories":"none"},"youtube"`) {
		t.Fatal(st.text)
	}
	if r := app.post("/api/info/save", form(map[string]any{"platforms": []any{"youtube"}})); r.text != `{"results":{"youtube":{"ok":false,"code":"failed","reason":"YouTube refused."}}}` {
		t.Fatal(r.text)
	}
	if r := app.get("/api/info/categories?platform=twitch&q=x"); r.text != `{"ok":false,"code":"unavailable","reason":"Twitch has no categories to pick from."}` {
		t.Fatal(r.text)
	}
	// a nil answer is a refusal too
	yt.reply = func(InfoChange) (*jsjson.Object, error) { return nil, nil }
	if r := app.post("/api/info/save", form(map[string]any{"platforms": []any{"youtube"}})); r.text != `{"results":{"youtube":{"ok":false,"code":"failed","reason":"YouTube refused."}}}` {
		t.Fatal(r.text)
	}
}

// The warning belongs to a platform that does stream info: one that only
// sends shows none here, even while the dock shows it.
func TestInfoNoWarningForASenderWithoutStreamInfo(t *testing.T) {
	yt := newFakeSender()
	yt.as, yt.id = "Example Channel (@example2960)", "UCfakefakefakefakefakeAA"
	tg := func() map[string]targets.Target {
		t := fixedTargets()
		y := t["youtube"]
		y.OwnerID = strp("UCfakefakefakefakefakeBB")
		t["youtube"] = y
		return t
	}
	app := startInfo(Options{Senders: map[string]Sender{"youtube": yt}, Targets: tg}, nil)
	if w := app.srv.platformStatus().YouTube.Warning; w == "" {
		t.Fatal("the dock shows no warning; the test proves nothing")
	}
	r := app.get("/api/info/state")
	if w := r.json["platforms"].(map[string]any)["youtube"].(map[string]any)["warning"]; w != "" {
		t.Fatal(w)
	}
}

// The tags a save carries are the form's, cleaned.
func TestInfoSaveCarriesTheFormsOwnTags(t *testing.T) {
	b := both(Options{}, nil, nil, nil)
	b.app.post("/api/info/save", form(map[string]any{"platforms": []any{"twitch"}, "tags": []any{" Night ", "night", "games"}}))
	if got := b.twitch.saved(); len(got) != 1 || !reflect.DeepEqual(got[0].Tags, []string{"Night", "games"}) {
		t.Fatalf("%+v", got)
	}
}
