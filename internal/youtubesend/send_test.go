package youtubesend

// test/youtube-send.test.js at 29a9d2c, the sending cases (1–19, 23, 34
// and 35), 1:1 against the fake Google. The viewer (20–22) and stream info
// (24–33) cases come with Phase 3.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

func send(t *testing.T, s *Sender, text string, tg targets.Target) server.SendResult {
	t.Helper()
	got, err := s.Send(t.Context(), text, tg)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 1
func TestAuthorizationURLIsTheDocumentedRequestWithPKCE(t *testing.T) {
	c := setup(t, false)
	if got := c.sender.Status(); got != (server.SenderStatus{Configured: true}) {
		t.Fatalf("before: %+v", got)
	}
	auth := c.begin(t)
	if auth.Scheme+"://"+auth.Host+auth.Path != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Fatalf("auth URL %s", auth)
	}
	// the parameters, in this order
	want := regexp.MustCompile(`^client_id=000000000000-fakefakefakefake\.apps\.googleusercontent\.com&redirect_uri=http%3A%2F%2F127\.0\.0\.1%3A` +
		fmt.Sprint(c.app.port) + `&response_type=code&scope=https%3A%2F%2Fwww\.googleapis\.com%2Fauth%2Fyoutube\.force-ssl&state=[A-Za-z0-9_-]{32}&code_challenge=[A-Za-z0-9_-]{43}&code_challenge_method=S256&access_type=offline&prompt=consent$`)
	if !want.MatchString(auth.RawQuery) {
		t.Fatalf("query %s", auth.RawQuery)
	}
	q := auth.Query()
	if strings.Contains(auth.RawQuery, clientSecret) {
		t.Fatal("the secret is in the URL")
	}

	back := c.comeBack(t, nil, "state", q.Get("state"), "code", c.code())
	if back.status != 302 || back.header.Get("Location") != "/setup" || back.header.Get("Cache-Control") != "no-store" || back.header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("back: %d %v", back.status, back.header)
	}
	// the verifier sent to the token endpoint hashes to the challenge sent to the browser
	sum := sha256.Sum256([]byte(c.g.get(func() string { return c.g.verifier })))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Fatal("verifier doesn't match the challenge")
	}
	exchange := c.g.find(func(x call) bool { return x.key == "POST /token" })[0]
	if fmt.Sprint(keys(exchange.body)) != "[client_id client_secret code code_verifier grant_type redirect_uri]" ||
		exchange.body["grant_type"] != "authorization_code" || exchange.body["redirect_uri"] != q.Get("redirect_uri") {
		t.Fatalf("exchange %v", exchange.body)
	}
	if !strings.HasPrefix(exchange.raw, "client_id=") || !strings.Contains(exchange.raw, "&client_secret=FAKE_CLIENT_SECRET_do_not_use&code=FAKE_AUTH_CODE&code_verifier=") {
		t.Fatalf("exchange order %s", exchange.raw)
	}

	if got := c.sender.Status(); got != (server.SenderStatus{Configured: true, SignedIn: true, As: "Example Channel", ID: channelID}) {
		t.Fatalf("after: %+v", got)
	}
	saved := c.saved(t)
	if saved["refreshToken"] != c.g.get(func() string { return c.g.refresh }) || saved["clientSecret"] != clientSecret {
		t.Fatalf("saved %v", saved)
	}
	if fi, _ := os.Stat(c.file); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// 2
func TestTheCallbackOnlyHonoursAStateThisServerIssuedOnce(t *testing.T) {
	c := setup(t, false)
	// nothing pending
	if r := c.comeBack(t, nil, "state", "made-up", "code", c.code()); r.status != 400 {
		t.Fatalf("made up: %d", r.status)
	}
	good := c.begin(t).Query().Get("state")
	wrong := good[:len(good)-1] + "A"
	if strings.HasSuffix(good, "A") {
		wrong = good[:len(good)-1] + "B"
	}
	if r := c.comeBack(t, nil, "state", wrong, "code", c.code()); r.status != 400 || c.g.count("POST /token") != 0 || c.sender.Status().SignedIn {
		t.Fatalf("wrong state: %d", r.status)
	}
	// the right state from a foreign Host
	if r := c.comeBack(t, map[string]string{"host": fmt.Sprintf("evil.example:%d", c.app.port)}, "state", good, "code", c.code()); r.status != 403 {
		t.Fatalf("foreign Host: %d", r.status)
	}
	// the real one works, and can't be replayed
	if r := c.comeBack(t, nil, "state", good, "code", c.code()); r.status != 302 {
		t.Fatalf("real: %d %s", r.status, r.text)
	}
	if r := c.comeBack(t, nil, "state", good, "code", c.code()); r.status != 400 || c.g.count("POST /token") != 1 {
		t.Fatalf("replay: %d", r.status)
	}
	// an ordinary visit to the panel's address is not ours
	for _, p := range []string{"/", "/?code=x"} {
		if r := c.app.request(t, "GET", p, "", nil); r.status != 404 {
			t.Fatalf("%s: %d", p, r.status)
		}
	}
}

// 3
func TestSignInCancelledExpiredBadCodeNoRefreshTokenNoChannel(t *testing.T) {
	c := setup(t, false)
	try := func(mutate func(), params ...string) string {
		t.Helper()
		auth := c.begin(t)
		if mutate != nil {
			mutate()
		}
		r := c.comeBack(t, nil, append([]string{"state", auth.Query().Get("state")}, params...)...)
		if r.status != 400 || c.sender.Status().SignedIn {
			t.Fatalf("%v: %d", params, r.status)
		}
		if !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") || r.header.Get("Cache-Control") != "no-store" || r.header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("page headers %v", r.header)
		}
		e := c.setupState(t)["signinError"].(string)
		if !strings.Contains(r.text, "<p>"+esc(e)+"</p>") {
			t.Fatalf("page doesn't say %q: %s", e, r.text)
		}
		return e
	}
	for _, c2 := range []struct {
		want   string
		mutate func()
		params []string
	}{
		{"Google sign-in was cancelled.", nil, []string{"error", "access_denied"}},
		{"Google refused the sign-in.", nil, []string{"error", "<script>alert(1)</script>"}},
		{"That sign-in took too long. Start again.", func() { c.clock.add(11 * time.Minute) }, []string{"code", c.code()}},
		{"Google wouldn't finish the sign-in: Bad Request", nil, []string{"code", "WRONG_CODE"}},
		{"Google didn't return a refresh token, so the sign-in wouldn't last. Start again.", func() { c.g.state(func() { c.g.noRefresh = true }) }, []string{"code", c.code()}},
		{"Signed in, but that Google account has no YouTube channel to chat as.", func() { c.g.state(func() { c.g.noRefresh, c.g.noChannel = false, true }) }, []string{"code", c.code()}},
	} {
		if got := try(c2.mutate, c2.params...); got != c2.want {
			t.Errorf("%v: %q, want %q", c2.params, got, c2.want)
		}
	}
	if _, has := c.saved(t)["refreshToken"]; has {
		t.Fatal("a refresh token was saved")
	}
	// the page escapes what it shows
	if got := esc(`<a href="x">'&'</a>`); got != "&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;" {
		t.Fatalf("esc: %s", got)
	}
}

// 4
func TestSendOneLookupPerBroadcastThenInsertNeverARead(t *testing.T) {
	c := setup(t, true)
	before := len(c.g.apiCalls()) // the channels.list from sign-in
	for _, text := range []string{"hello chat", "second"} {
		if got := send(t, c.sender, text, target); got != (server.SendResult{OK: true, ID: "fake-yt-msg-1"}) {
			t.Fatalf("%q: %+v", text, got)
		}
	}
	calls := c.g.apiCalls()[before:]
	var ks []string
	for _, x := range calls {
		ks = append(ks, x.key)
	}
	if fmt.Sprint(ks) != "[GET /youtube/v3/videos POST /youtube/v3/liveChat/messages POST /youtube/v3/liveChat/messages]" {
		t.Fatalf("calls %v", ks)
	}
	if calls[0].query.Encode() != "id=FAKEvideo01&part=liveStreamingDetails" || calls[1].query.Encode() != "part=snippet" {
		t.Fatalf("queries %v %v", calls[0].query, calls[1].query)
	}
	if calls[1].raw != `{"snippet":{"liveChatId":"FAKE_LIVE_CHAT_ID","type":"textMessageEvent","textMessageDetails":{"messageText":"hello chat"}}}` {
		t.Fatalf("body %s", calls[1].raw)
	}
	if calls[1].header.Get("authorization") != "Bearer "+c.g.get(func() string { return c.g.access }) {
		t.Fatal("authorization")
	}
	if len(c.g.find(func(x call) bool { return x.method == "GET" && strings.Contains(x.path, "liveChat") })) != 0 {
		t.Fatal("chat was read through the API")
	}
	// 1 (channels) + 1 (videos) + 20 + 20
	q := c.quota(t)
	if q["usedTodayEstimate"] != 42.0 || q["perSend"] != 20.0 || q["perBroadcastLookup"] != 1.0 || q["dailyDefault"] != 10000.0 {
		t.Fatalf("quota %v", q)
	}
}

// 5
func TestSendNotLiveYet(t *testing.T) {
	c := setup(t, true)
	before := c.g.total()
	if got := send(t, c.sender, "x", targets.Target{}); got.Code != "not_live" || got.Reason != "The reader hasn't found a live broadcast yet, so there's no chat to reply in." || c.g.total() != before {
		t.Fatalf("no video: %+v", got)
	}
	c.g.state(func() {
		c.g.video = func(call) reply {
			return reply{200, map[string]any{"items": []any{map[string]any{"id": video, "liveStreamingDetails": map[string]any{"scheduledStartTime": "2030-01-01T00:00:00Z"}}}}}
		}
	})
	if got := send(t, c.sender, "x", target); got != (server.SendResult{Code: "not_live", Reason: "That broadcast has no active live chat — it isn't live yet, or it has ended."}) || c.g.count("POST /youtube/v3/liveChat/messages") != 0 {
		t.Fatalf("scheduled: %+v", got)
	}
	// not cached: once it goes live the next send works
	c.g.state(func() { c.g.video = nil })
	if got := send(t, c.sender, "x", target); !got.OK {
		t.Fatalf("live: %+v", got)
	}
}

// 6
func TestSendYouTubesOwnReasonComesBack(t *testing.T) {
	c := setup(t, true)
	script := func(r reply) { c.g.state(func() { c.g.insert = func(call) reply { return r } }) }
	script(gErr(403, "liveChatDisabled", "The live chat is disabled by the owner."))
	if got := send(t, c.sender, "x", target); got != (server.SendResult{Code: "liveChatDisabled", Reason: "The live chat is disabled by the owner."}) {
		t.Fatalf("%+v", got)
	}
	script(gErr(403, "forbidden", "You do not have the permissions required to create the specified live chat message."))
	if got := send(t, c.sender, "x", target); got.Reason != "You do not have the permissions required to create the specified live chat message." {
		t.Fatalf("%+v", got)
	}
	script(gErr(400, "messageTextInvalid", "The message text is not valid."))
	if got := send(t, c.sender, "x", target); got.Code != "messageTextInvalid" {
		t.Fatalf("%+v", got)
	}
	script(gErr(403, "rateLimitExceeded", "The user has posted too many chat messages in a given timeframe."))
	if got := send(t, c.sender, "x", target); got.Code != "rateLimitExceeded" {
		t.Fatalf("%+v", got)
	}
	if n := c.g.count("POST /youtube/v3/liveChat/messages"); n != 4 { // one attempt each, no retries
		t.Fatalf("%d inserts", n)
	}
	script(reply{500, nil})
	if got := send(t, c.sender, "x", target); got != (server.SendResult{Code: "rejected", Reason: "YouTube answered HTTP 500."}) {
		t.Fatalf("%+v", got)
	}
	// an envelope with only a top-level message
	script(reply{400, map[string]any{"error": map[string]any{"message": "Bad thing."}}})
	if got := send(t, c.sender, "x", target); got != (server.SendResult{Code: "rejected", Reason: "Bad thing."}) {
		t.Fatalf("%+v", got)
	}
}

// 7
func TestAnEndedChatIsForgotten(t *testing.T) {
	c := setup(t, true)
	send(t, c.sender, "x", target)
	c.g.state(func() {
		c.g.insert = func(call) reply { return gErr(403, "liveChatEnded", "The specified live chat is no longer live.") }
	})
	if got := send(t, c.sender, "x", target); got.Code != "liveChatEnded" {
		t.Fatalf("%+v", got)
	}
	c.g.state(func() { c.g.insert = nil })
	send(t, c.sender, "x", target)
	if n := c.g.count("GET /youtube/v3/videos"); n != 2 {
		t.Fatalf("%d lookups", n)
	}
	// liveChatNotFound too
	c.g.state(func() {
		c.g.insert = func(call) reply {
			return gErr(404, "liveChatNotFound", "The live chat that you are trying to retrieve cannot be found.")
		}
	})
	send(t, c.sender, "x", target)
	c.g.state(func() { c.g.insert = nil })
	send(t, c.sender, "x", target)
	if n := c.g.count("GET /youtube/v3/videos"); n != 3 {
		t.Fatalf("%d lookups", n)
	}
}

// 8
func TestQuotaExhaustedSaysWhenItResetsAndStopsCalling(t *testing.T) {
	c := setup(t, true)
	c.g.state(func() {
		c.g.insert = func(call) reply {
			return gErr(403, "quotaExceeded", "The request cannot be completed because you have exceeded your quota.")
		}
	})
	untilReset := MsUntilPacificMidnight(float64(c.clock.ms.Load()))
	got := send(t, c.sender, "x", target)
	if got.Code != "quota_exhausted" || !regexp.MustCompile(`^YouTube API quota is used up for today — it resets at midnight Pacific, in about \d+(h \d+)?m\.$`).MatchString(got.Reason) ||
		math.Abs(float64(got.RetryAfter)-untilReset/1000) > 1 {
		t.Fatalf("%+v (until reset %v)", got, untilReset)
	}
	if c.quota(t)["exhausted"] != true {
		t.Fatal("not exhausted")
	}
	c.g.state(func() { c.g.insert = nil })
	calls := c.g.total()
	if got := send(t, c.sender, "x", target); got.Code != "quota_exhausted" || c.g.total() != calls {
		t.Fatalf("while exhausted: %+v", got)
	}
	c.clock.add(time.Duration(untilReset+1000) * time.Millisecond) // past midnight Pacific
	if got := send(t, c.sender, "x", target); !got.OK || c.quota(t)["exhausted"] != false {
		t.Fatalf("after: %+v", got)
	}
	// dailyLimitExceeded is the same, and a lookup's refusal counts too
	c.g.state(func() {
		c.g.video = func(call) reply { return gErr(403, "dailyLimitExceeded", "Daily Limit Exceeded.") }
	})
	if got := send(t, c.sender, "x", vid("FAKEvideo02")); got.Code != "quota_exhausted" {
		t.Fatalf("lookup: %+v", got)
	}
}

// 9
func TestPacificMidnightArithmetic(t *testing.T) {
	ms := func(y int, m time.Month, d, h, min, s int) float64 {
		return float64(time.Date(y, m, d, h, min, s, 0, time.UTC).UnixMilli())
	}
	// 2026-01-15 07:59:30 UTC is 23:59:30 PST the day before
	for _, c := range []struct{ at, want float64 }{
		{ms(2026, 1, 15, 7, 59, 30), 30000},
		{ms(2026, 7, 15, 7, 0, 0), 86400000}, // 00:00:00 PDT
		{ms(2026, 7, 15, 19, 0, 0), 43200000},
		{ms(2026, 7, 15, 19, 0, 0) + 999, 43200000}, // the wall clock's seconds, not its milliseconds
	} {
		if got := MsUntilPacificMidnight(c.at); got != c.want {
			t.Errorf("%v: %v, want %v", c.at, got, c.want)
		}
	}
	// Node's own answers around both 2026 clock changes: the wall clock is
	// read as it is, so the count is up to an hour off on those days
	for at, want := range map[float64][2]any{
		1772963999000: {79201000.0, "2026-03-08"}, 1772964000000: {75600000.0, "2026-03-08"},
		1772958600000: {84600000.0, "2026-03-08"}, 1793521800000: {81000000.0, "2026-11-01"},
		1793525400000: {81000000.0, "2026-11-01"}, 1793527201000: {79199000.0, "2026-11-01"},
		1798761599999: {28801000.0, "2026-12-31"},
	} {
		if got, day := MsUntilPacificMidnight(at), pacificDay(at); got != want[0] || day != want[1] {
			t.Errorf("%v: %v %s, want %v", at, got, day, want)
		}
	}
	if d := pacificDay(ms(2026, 1, 15, 7, 59, 30)); d != "2026-01-14" {
		t.Errorf("day %s", d)
	}
	for ms, want := range map[float64]string{0: "1m", 29999: "1m", 90000: "2m", 3570000: "1h 0m", 5430000: "1h 31m", 86400000: "24h 0m"} {
		if got := inHoursMinutes(ms); got != want {
			t.Errorf("inHoursMinutes(%v) = %s, want %s", ms, got, want)
		}
	}
}

// 10
func TestRefreshBeforeExpiryOnceForConcurrentSendsAnd401Retry(t *testing.T) {
	c := setup(t, true)
	c.clock.add((3599 - 120) * time.Second)
	var wg sync.WaitGroup
	results := make([]server.SendResult, 3)
	for i, text := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], _ = c.sender.Send(context.Background(), text, target) }()
	}
	wg.Wait()
	for _, r := range results {
		if !r.OK {
			t.Fatalf("%+v", results)
		}
	}
	refreshes := c.g.refreshes()
	if len(refreshes) != 1 || fmt.Sprint(keys(refreshes[0].body)) != "[client_id client_secret grant_type refresh_token]" {
		t.Fatalf("refreshes %v", refreshes)
	}
	if !regexp.MustCompile(`^client_id=[^&]+&client_secret=[^&]+&grant_type=refresh_token&refresh_token=[^&]+$`).MatchString(refreshes[0].raw) {
		t.Fatalf("refresh order %s", refreshes[0].raw)
	}
	saved := c.saved(t)
	if saved["accessToken"] != c.g.get(func() string { return c.g.access }) || saved["refreshToken"] != c.g.get(func() string { return c.g.refresh }) {
		t.Fatalf("saved %v", saved) // Google keeps the same refresh token
	}
	c.g.state(func() { c.g.access = "FAKE_YT_ACCESS_REVOKED" })
	if got := send(t, c.sender, "d", target); !got.OK || len(c.g.refreshes()) != 2 {
		t.Fatalf("401: %+v, %d refreshes", got, len(c.g.refreshes()))
	}
}

// 11
func TestADeadRefreshTokenSignsOutKeepsTheClientAndSaysSo(t *testing.T) {
	c := setup(t, true)
	c.g.state(func() { c.g.refresh = "FAKE_YT_REFRESH_GONE" })
	c.clock.add(3600 * time.Second)
	if got := send(t, c.sender, "x", target); got != (server.SendResult{Code: "not_signed_in", Reason: Expired}) {
		t.Fatalf("%+v", got)
	}
	if got := c.sender.Status(); got != (server.SenderStatus{Configured: true, Detail: Expired}) {
		t.Fatalf("%+v", got)
	}
	if got := c.saved(t); !reflect.DeepEqual(got, map[string]any{"clientId": clientID, "clientSecret": clientSecret}) {
		t.Fatalf("saved %v", got)
	}
}

// 12
func TestSignOutRevokesAndChangingTheClientForgetsEverything(t *testing.T) {
	c := setup(t, true)
	refresh := c.g.get(func() string { return c.g.refresh })
	if r := c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil); r.status != 200 {
		t.Fatalf("signout %d", r.status)
	}
	if fmt.Sprint(c.g.revoked) != "["+refresh+"]" || !reflect.DeepEqual(c.saved(t), map[string]any{"clientId": clientID, "clientSecret": clientSecret}) {
		t.Fatalf("revoked %v, saved %v", c.g.revoked, c.saved(t))
	}
	if got := send(t, c.sender, "x", target); got.Code != "not_signed_in" {
		t.Fatalf("%+v", got)
	}

	c.signIn(t)
	// a blank secret on the same client keeps it, and keeps the sign-in
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": ""}, nil)
	if !c.sender.Status().SignedIn {
		t.Fatal("a blank secret signed out")
	}
	if r := c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": "bad id with spaces"}, nil); r.json["code"] != "bad_client_id" {
		t.Fatalf("%v", r.json)
	}
	if r := c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": strings.Repeat("é", 201)}, nil); r.json["code"] != "bad_client_secret" {
		t.Fatalf("%v", r.json)
	}
	other := map[string]any{"clientId": "111111111111-otherfakeclient.apps.googleusercontent.com", "clientSecret": "FAKE_OTHER_SECRET"}
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": other["clientId"], "clientSecret": other["clientSecret"]}, nil)
	if c.sender.Status().SignedIn || !reflect.DeepEqual(c.saved(t), other) {
		t.Fatalf("other client: %v", c.saved(t))
	}
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": ""}, nil)
	if c.saved(t) != nil {
		t.Fatalf("removed client: %v", c.saved(t))
	}
}

// 13
func TestOverHTTPGatedAndNoSecretInAnyResponse(t *testing.T) {
	c := setup(t, false)
	var seen []string
	keep := func(r response) response { seen = append(seen, r.text+fmt.Sprint(r.header)); return r }
	if r := c.app.post(t, "/api/setup/youtube/signin", map[string]any{}, map[string]string{"x-dock-token": "wrong"}); r.json["code"] != "bad_token" {
		t.Fatalf("%v", r.json)
	}
	if r := c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": "x"}, map[string]string{"origin": "https://evil.example"}); r.json["code"] != "bad_origin" {
		t.Fatalf("%v", r.json)
	}
	if r := c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, map[string]string{"host": fmt.Sprintf("evil.example:%d", c.app.port)}); r.json["code"] != "bad_host" {
		t.Fatalf("%v", r.json)
	}

	start := keep(c.app.post(t, "/api/setup/youtube/signin", map[string]any{}, nil))
	state := regexp.MustCompile(`state=([^&]+)`).FindStringSubmatch(start.json["url"].(string))[1]
	keep(c.comeBack(t, nil, "state", state, "code", c.code()))
	st := keep(c.app.get(t, "/api/setup/state")).json["platforms"].(map[string]any)["youtube"].(map[string]any)
	if st["signedIn"] != true || st["as"] != "Example Channel" || st["hasSecret"] != true || st["redirectUri"] != c.app.origin {
		t.Fatalf("state %v", st)
	}
	keep(c.app.get(t, "/api/dock/session"))
	if r := keep(c.app.post(t, "/api/send", map[string]any{"target": "youtube", "text": "hello from the dock"}, nil)); r.text != `{"results":{"youtube":{"ok":true,"id":"fake-yt-msg-1"}}}` {
		t.Fatalf("send %s", r.text)
	}
	c.g.state(func() {
		c.g.insert = func(call) reply {
			return gErr(403, "quotaExceeded", "The request cannot be completed because you have exceeded your quota.")
		}
	})
	q := keep(c.app.post(t, "/api/send", map[string]any{"target": "youtube", "text": "x"}, nil)).json["results"].(map[string]any)["youtube"].(map[string]any)
	if q["code"] != "quota_exhausted" || q["retryAfter"].(float64) <= 0 {
		t.Fatalf("quota %v", q)
	}
	keep(c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil))

	all := strings.Join(seen, "\n")
	for _, secret := range []string{"FAKE_YT_ACCESS_", "FAKE_YT_REFRESH_", clientSecret, c.code(), c.g.get(func() string { return c.g.verifier })} {
		if strings.Contains(all, secret) {
			t.Errorf("%s in a response", secret)
		}
	}
}

// holdRefresh holds Google's answer to the next refresh until release.
func (c *rig) holdRefresh() (release func(), seen chan struct{}) {
	hold := make(chan struct{})
	seen = make(chan struct{})
	c.g.state(func() { c.g.hold, c.g.seen = hold, seen })
	return func() { close(hold) }, seen
}

// 14
func TestARefreshInFlightWhenISignOutDoesNotSignMeBackIn(t *testing.T) {
	c := setup(t, true)
	release, seen := c.holdRefresh()
	c.clock.add((3599 - 60) * time.Second)
	sending := make(chan server.SendResult)
	go func() { r, _ := c.sender.Send(context.Background(), "x", target); sending <- r }()
	<-seen
	c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil)
	release()
	if got := <-sending; got.Code != "not_signed_in" {
		t.Fatalf("%+v", got)
	}
	if c.sender.Status().SignedIn || !reflect.DeepEqual(c.saved(t), map[string]any{"clientId": clientID, "clientSecret": clientSecret}) ||
		c.g.count("POST /youtube/v3/liveChat/messages") != 0 || c.g.count("GET /youtube/v3/videos") != 0 {
		t.Fatalf("saved %v", c.saved(t))
	}
}

// 15
func TestARefreshInFlightWhenTheClientChangesDoesNotPutTheOldClientBack(t *testing.T) {
	c := setup(t, true)
	release, seen := c.holdRefresh()
	c.clock.add((3599 - 60) * time.Second)
	sending := make(chan server.SendResult)
	go func() { r, _ := c.sender.Send(context.Background(), "x", target); sending <- r }()
	<-seen
	other := map[string]any{"clientId": "111111111111-otherfakeclient.apps.googleusercontent.com", "clientSecret": "FAKE_OTHER_SECRET"}
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": other["clientId"], "clientSecret": other["clientSecret"]}, nil)
	release()
	if got := <-sending; got.OK {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(c.saved(t), other) || c.sender.Status().SignedIn || c.g.count("GET /youtube/v3/videos") != 0 {
		t.Fatalf("saved %v", c.saved(t)) // not the old client, and no tokens
	}
}

// 16
func TestTheClientChangingWhileTheBrowserIsAtGoogleCancelsThatSignIn(t *testing.T) {
	c := setup(t, false)
	auth := c.begin(t)
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": "111111111111-otherfakeclient.apps.googleusercontent.com", "clientSecret": "FAKE_OTHER_SECRET"}, nil)
	if r := c.comeBack(t, nil, "state", auth.Query().Get("state"), "code", c.code()); r.status != 400 || c.g.count("POST /token") != 0 {
		t.Fatalf("%d", r.status)
	}
	if _, has := c.saved(t)["refreshToken"]; has {
		t.Fatal("signed in")
	}
}

// 17
func TestCallbackANonASCIIOrWrongLengthStateGetsThe400Page(t *testing.T) {
	c := setup(t, false)
	good := c.begin(t).Query().Get("state")
	sameChars := strings.Repeat("é", len(good)) // same characters, more bytes
	for _, state := range []string{sameChars, good + "é", "😀", "x", good[1:], ""} {
		r := c.comeBack(t, nil, "state", state, "code", c.code())
		if r.status != 400 || !strings.Contains(r.text, "a sign-in this server started") || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
			t.Fatalf("%q: %d %s", state, r.status, r.text)
		}
	}
	if c.g.count("POST /token") != 0 || c.setupState(t)["signinError"] != "" {
		t.Fatal("a bad guess got further than the state check")
	}
	// a bad guess doesn't burn the pending sign-in
	if r := c.comeBack(t, nil, "state", good, "code", c.code()); r.status != 302 {
		t.Fatalf("real: %d", r.status)
	}
}

// 18
func TestLengthTwoHundredBeforeAnyQuotaIsSpent(t *testing.T) {
	c := setup(t, true)
	if c.sender.MaxLength() != 200 {
		t.Fatal("MaxLength")
	}
	before, used := c.g.total(), c.quota(t)["usedTodayEstimate"]
	if got := send(t, c.sender, strings.Repeat("x", 201), target); got != (server.SendResult{Code: "too_long", Reason: "YouTube live chat messages are 200 characters at most."}) {
		t.Fatalf("%+v", got)
	}
	if c.g.total() != before || c.quota(t)["usedTodayEstimate"] != used {
		t.Fatal("quota spent on a message that was too long")
	}
	if got := send(t, c.sender, strings.Repeat("😀", 200), target); !got.OK {
		t.Fatalf("200 code points: %+v", got)
	}
}

// fakeTwitch is helpers.js fakeSender with a 500 limit.
type fakeTwitch struct {
	mu   sync.Mutex
	sent int
}

func (f *fakeTwitch) Status() server.SenderStatus {
	return server.SenderStatus{Configured: true, SignedIn: true, As: "example_user"}
}
func (f *fakeTwitch) MaxLength() int { return 500 }
func (f *fakeTwitch) Send(context.Context, string, targets.Target) (server.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent++
	return server.SendResult{OK: true, ID: "fake-1"}, nil
}

// 19
func TestABothThatFitsTwitchButNotYouTubeSendsToNeither(t *testing.T) {
	c := setup(t, true)
	tw := &fakeTwitch{}
	a := startApp(t, func(o *server.Options) { o.Senders = map[string]server.Sender{"twitch": tw, "youtube": c.sender} })
	p := a.get(t, "/api/dock/session").json["platforms"].(map[string]any)
	if p["youtube"].(map[string]any)["maxLength"] != 200.0 || p["twitch"].(map[string]any)["maxLength"] != 500.0 {
		t.Fatalf("%v", p)
	}
	calls := c.g.total()
	r := a.post(t, "/api/send", map[string]any{"target": "both", "text": strings.Repeat("x", 300)}, nil)
	if r.status != 400 || r.json["code"] != "too_long" || r.json["platform"] != "youtube" || r.json["limit"] != 200.0 || tw.sent != 0 || c.g.total() != calls {
		t.Fatalf("%d %v", r.status, r.json)
	}
	r = a.post(t, "/api/send", map[string]any{"target": "both", "text": strings.Repeat("x", 200)}, nil)
	if r.text != `{"results":{"twitch":{"ok":true,"id":"fake-1"},"youtube":{"ok":true,"id":"fake-yt-msg-1"}}}` {
		t.Fatalf("%s", r.text)
	}
}

// 23
func TestAuthChangeIsAnnouncedNotAFailedCallback(t *testing.T) {
	c := setup(t, false)
	var mu sync.Mutex
	n := 0
	count := func() int { mu.Lock(); defer mu.Unlock(); return n }
	c.hookMu.Lock()
	c.hook = func() { mu.Lock(); n++; mu.Unlock(); panic("listener bug") }
	c.hookMu.Unlock()
	auth := c.begin(t)
	if r := c.comeBack(t, nil, "state", auth.Query().Get("state"), "error", "access_denied"); r.status != 400 || count() != 0 {
		t.Fatalf("cancelled at Google: %d, %d announcements", r.status, count())
	}
	c.signIn(t)
	if count() != 1 || !c.sender.Status().SignedIn { // the panicking listener didn't undo it
		t.Fatalf("%d announcements", count())
	}
	if r := c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil); r.status != 200 || count() != 2 {
		t.Fatalf("signout %d, %d announcements", r.status, count())
	}
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": clientID, "clientSecret": ""}, nil)
	if count() != 2 { // same client, secret kept: nothing changed
		t.Fatalf("%d announcements", count())
	}
	c.app.post(t, "/api/setup/youtube/client", map[string]any{"clientId": ""}, nil)
	if count() != 3 {
		t.Fatalf("%d announcements", count())
	}
}

// 34
func TestSignInRecordsTheHandleAndTheChannelID(t *testing.T) {
	c := setup(t, false)
	c.g.state(func() { c.g.customURL = "@example2960" })
	c.signIn(t)
	if c.saved(t)["handle"] != "@example2960" {
		t.Fatalf("%v", c.saved(t))
	}
	if got := c.sender.Status(); got != (server.SenderStatus{Configured: true, SignedIn: true, As: "Example Channel (@example2960)", ID: channelID}) {
		t.Fatalf("%+v", got)
	}
	st := c.app.get(t, "/api/setup/state").json["platforms"].(map[string]any)["youtube"].(map[string]any)
	if st["as"] != "Example Channel (@example2960)" || st["channelId"] != channelID || st["handle"] != "@example2960" {
		t.Fatalf("%v", st)
	}
	// signed out: the page is told nothing about the channel that was
	c.app.post(t, "/api/setup/youtube/signout", map[string]any{}, nil)
	st = c.app.get(t, "/api/setup/state").json["platforms"].(map[string]any)["youtube"].(map[string]any)
	if st["as"] != nil || st["channelId"] != "" || st["handle"] != "" {
		t.Fatalf("%v", st)
	}
	if _, has := c.saved(t)["handle"]; has {
		t.Fatal("handle kept")
	}
	// a custom URL that isn't a handle is not shown as one
	for _, odd := range []string{"/c/ExampleChannel", "examplechannel", "@has space", "", "@", "@a/b", "@" + strings.Repeat("😀", 31)} {
		c.g.state(func() { c.g.customURL = odd })
		c.signIn(t)
		if _, has := c.saved(t)["handle"]; has || c.sender.Status().As != "Example Channel" {
			t.Errorf("%q: %v", odd, c.saved(t))
		}
	}
	// 30 astral characters are 60 UTF-16 units: still a handle
	c.g.state(func() { c.g.customURL = "@" + strings.Repeat("😀", 30) })
	c.signIn(t)
	if _, has := c.saved(t)["handle"]; !has {
		t.Fatal("a 60-unit handle was dropped")
	}
}

// 35
func TestTheWrongChannelSaysWhoseNameRepliesGoOutUnder(t *testing.T) {
	c := setup(t, false)
	c.g.state(func() { c.g.customURL = "@example2960" })
	c.signIn(t)
	var mu sync.Mutex
	owner := "UCsomeoneelsesomeoneelse"
	a := startApp(t, func(o *server.Options) {
		o.Senders = map[string]server.Sender{"youtube": c.sender}
		o.Targets = func() map[string]targets.Target {
			all := fixedTargets()
			y := all["youtube"]
			mu.Lock()
			id := owner
			mu.Unlock()
			y.OwnerID = &id
			all["youtube"] = y
			return all
		}
	})
	w := a.get(t, "/api/dock/session").json["platforms"].(map[string]any)["youtube"].(map[string]any)["warning"].(string)
	if !strings.HasPrefix(w, "Signed in as Example Channel (@example2960) — not the channel this YouTube chat belongs to") {
		t.Fatalf("%q", w)
	}
	mu.Lock()
	owner = channelID
	mu.Unlock()
	if w := a.get(t, "/api/dock/session").json["platforms"].(map[string]any)["youtube"].(map[string]any)["warning"]; w != "" {
		t.Fatalf("%q", w)
	}
}
