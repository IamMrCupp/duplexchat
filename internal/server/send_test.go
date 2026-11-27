package server

// multichat test/sendapi.test.js at 29a9d2c: the send cases.

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/targets"
)

func results(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	r, ok := out["results"].(map[string]any)
	if !ok {
		t.Fatalf("no results: %v", out)
	}
	return r
}

func TestSendOnePlatform(t *testing.T) {
	twitch := newFakeSender()
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch}})
	code, out := a.send(map[string]any{"target": "twitch", "text": "  hello chat  "}, nil)
	if code != 200 || !reflect.DeepEqual(out, map[string]any{"results": map[string]any{"twitch": map[string]any{"ok": true, "id": "fake-1"}}}) {
		t.Fatalf("%d %v", code, out)
	}
	if twitch.count() != 1 || twitch.sent[0].text != "hello chat" || *twitch.sent[0].target.Channel != "examplechannel" {
		t.Fatalf("sent %+v", twitch.sent)
	}
}

func TestSendBothWhereOneFailsWithThePlatformsReason(t *testing.T) {
	youtube := newFakeSender()
	youtube.reply = func(string, targets.Target) (SendResult, error) {
		return SendResult{Code: "liveChatDisabled", Reason: "The owner disabled live chat."}, nil
	}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender(), "youtube": youtube}})
	code, out := a.send(map[string]any{"target": "both", "text": "hi"}, nil)
	r := results(t, out)
	if code != 200 || !reflect.DeepEqual(r["twitch"], map[string]any{"ok": true, "id": "fake-1"}) ||
		!reflect.DeepEqual(r["youtube"], map[string]any{"ok": false, "code": "liveChatDisabled", "reason": "The owner disabled live chat."}) {
		t.Fatalf("%d %v", code, out)
	}
}

func TestSendNotSignedInNoChatConnectedPlatformNotBuilt(t *testing.T) {
	twitch := &fakeSender{as: "example_user"} // not signed in
	youtube := newFakeSender()
	a := startAPI(Options{
		Senders: map[string]Sender{"twitch": twitch, "youtube": youtube},
		Targets: func() map[string]targets.Target {
			return map[string]targets.Target{"twitch": fixedTargets()["twitch"], "youtube": {Choices: []targets.Choice{}}}
		},
	})
	_, out := a.send(map[string]any{"target": "both", "text": "hi"}, nil)
	r := results(t, out)
	if r["twitch"].(map[string]any)["code"] != "not_signed_in" || r["youtube"].(map[string]any)["code"] != "no_target" {
		t.Fatalf("%v", r)
	}
	if r["twitch"].(map[string]any)["reason"] != "Not signed in to Twitch — open /setup." || r["youtube"].(map[string]any)["reason"] != "No YouTube chat is connected in the panel." {
		t.Fatalf("reasons %v", r)
	}
	if twitch.count()+youtube.count() != 0 {
		t.Fatal("something was sent")
	}
	bare := startAPI(Options{})
	_, out = bare.send(map[string]any{"target": "twitch", "text": "hi"}, nil)
	if got := results(t, out)["twitch"]; !reflect.DeepEqual(got, map[string]any{"ok": false, "code": "unavailable", "reason": "Sending to Twitch isn't in this build."}) {
		t.Fatalf("bare: %v", got)
	}
}

func TestSendRateLimitCarriesRetryAfterAThrowingSenderDoesNotLeak(t *testing.T) {
	twitch := newFakeSender()
	twitch.reply = func(string, targets.Target) (SendResult, error) {
		return SendResult{Code: "rate_limited", Reason: "Twitch rate limit hit.", RetryAfter: 12}, nil
	}
	youtube := newFakeSender()
	youtube.reply = func(string, targets.Target) (SendResult, error) {
		return SendResult{}, errors.New("boom with secret-looking detail FAKE_TOKEN_abc")
	}
	var logged []string
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch, "youtube": youtube}, Log: func(kind string, what any) {
		logged = append(logged, fmt.Sprint(kind, ": ", what))
	}})
	_, out := a.send(map[string]any{"target": "both", "text": "hi"}, nil)
	r := results(t, out)
	if !reflect.DeepEqual(r["twitch"], map[string]any{"ok": false, "code": "rate_limited", "reason": "Twitch rate limit hit.", "retryAfter": float64(12)}) {
		t.Fatalf("twitch %v", r["twitch"])
	}
	if !reflect.DeepEqual(r["youtube"], map[string]any{"ok": false, "code": "error", "reason": "Couldn't reach YouTube."}) {
		t.Fatalf("youtube %v", r["youtube"])
	}
	if strings.Contains(fmt.Sprint(out), "FAKE_TOKEN_abc") {
		t.Fatal("the error leaked")
	}
	if len(logged) != 1 || !strings.HasPrefix(logged[0], "send:youtube: ") {
		t.Fatalf("logged %v", logged)
	}
	// a panicking sender is the same: logged, not shown, not a crash
	youtube.reply = func(string, targets.Target) (SendResult, error) { panic("FAKE_TOKEN_panic") }
	code, out := a.send(map[string]any{"target": "youtube", "text": "hi"}, nil)
	if code != 200 || results(t, out)["youtube"].(map[string]any)["code"] != "error" || strings.Contains(fmt.Sprint(out), "FAKE_TOKEN") {
		t.Fatalf("panic: %d %v", code, out)
	}
	// an empty result is a refusal with the default words
	twitch.reply = func(string, targets.Target) (SendResult, error) { return SendResult{}, nil }
	_, out = a.send(map[string]any{"target": "twitch", "text": "hi"}, nil)
	if got := results(t, out)["twitch"]; !reflect.DeepEqual(got, map[string]any{"ok": false, "code": "failed", "reason": "The platform refused the message."}) {
		t.Fatalf("empty: %v", got)
	}
}

func TestSendBadBodies(t *testing.T) {
	twitch := newFakeSender()
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch}})
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"target": "discord", "text": "hi"}, "bad_target"},
		{map[string]any{"target": "twitch", "text": "   "}, "empty"},
		{map[string]any{"target": "twitch"}, "empty"},
		{map[string]any{"target": "twitch", "text": 42}, "empty"},
		{map[string]any{"target": "twitch", "text": strings.Repeat("x", 501)}, "too_long"},
	} {
		if code, out := a.send(c.body, nil); code != 400 || out["code"] != c.code {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}
	for _, raw := range []string{"{not json", "null", "42", `"text"`, "[]", "true"} {
		if code, out := a.do(http.MethodPost, "/api/send", nil, raw); code != 400 || out["code"] != "bad_request" {
			t.Errorf("%s: %d %v", raw, code, out)
		}
	}
	if code, _ := a.send(map[string]any{"target": "twitch", "text": strings.Repeat("x", 500)}, nil); code != 200 {
		t.Errorf("500 characters: %d", code)
	}
	if code, _ := a.send(map[string]any{"target": "twitch", "text": "a\r\nb"}, nil); code != 200 || twitch.sent[1].text != "a b" {
		t.Errorf("line breaks: %d %+v", code, twitch.sent)
	}
	if twitch.count() != 2 {
		t.Fatalf("sent %d", twitch.count())
	}
	// JavaScript's trim: a BOM and a no-break space go, U+0085 stays
	a.send(map[string]any{"target": "twitch", "text": "\ufeff\u00a0hi\u3000"}, nil)
	a.send(map[string]any{"target": "twitch", "text": "hi\u0085"}, nil)
	if twitch.sent[2].text != "hi" || twitch.sent[3].text != "hi\u0085" {
		t.Errorf("trim: %q %q", twitch.sent[2].text, twitch.sent[3].text)
	}
}

func TestSendGating(t *testing.T) {
	twitch := newFakeSender()
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch}})
	body := map[string]any{"target": "twitch", "text": "should never arrive"}
	for _, c := range []struct {
		code    string
		headers map[string]string
	}{
		{"no_token", map[string]string{"X-Dock-Token": ""}},
		{"bad_token", map[string]string{"X-Dock-Token": "not-the-token"}},
		{"bad_host", map[string]string{"Host": "evil.example:3131"}},
		{"bad_host", map[string]string{"Host": "192.168.1.20:3131"}},
		{"bad_origin", map[string]string{"Origin": "https://evil.example"}},
		{"bad_origin", map[string]string{"Origin": "null"}},
		{"bad_origin", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"proxied", map[string]string{"X-Forwarded-For": "203.0.113.9"}},
	} {
		if code, out := a.send(body, c.headers); code != 403 || out["code"] != c.code {
			t.Errorf("%s: %d %v", c.code, code, out)
		}
	}
	if code, out := a.do(http.MethodPost, "/api/send", map[string]string{"Origin": ""}, `{"target":"twitch","text":"x"}`); code != 403 || out["code"] != "bad_origin" {
		t.Errorf("no Origin: %d %v", code, out)
	}
	peer := "192.168.1.20:50000"
	lan := startAPI(Options{Senders: map[string]Sender{"twitch": twitch}, Peer: &peer})
	if code, out := lan.send(body, nil); code != 403 || out["code"] != "not_loopback" {
		t.Errorf("LAN: %d %v", code, out)
	}
	if twitch.count() != 0 {
		t.Fatal("a refused send arrived")
	}
}

func TestReplyTargetASendOnlyGoesWhereThePageSaid(t *testing.T) {
	twitch := newFakeSender()
	current := map[string]targets.Target{"twitch": fixedTargets()["twitch"], "youtube": {Choices: []targets.Choice{}}}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch}, Targets: func() map[string]targets.Target { return current }})
	shown := fixedTargets()["twitch"].Key

	// the source list changed under the page: another channel is now the target
	moved := fixedTargets()["twitch"]
	moved.SourceID, moved.Channel, moved.Label, moved.Key = "cccc3333", str("someoneelse"), "#someoneelse", "cccc3333:someoneelse"
	current = map[string]targets.Target{"twitch": moved, "youtube": {Choices: []targets.Choice{}}}
	code, out := a.send(map[string]any{"target": "twitch", "text": "meant for my chat", "expect": map[string]any{"twitch": shown}}, nil)
	if want := map[string]any{"ok": false, "code": "target_changed", "reason": "The Twitch reply target is now #someoneelse — check it and send again."}; code != 200 || !reflect.DeepEqual(results(t, out)["twitch"], want) {
		t.Fatalf("%d %v", code, out)
	}
	// a page that says nothing about where it thinks it's sending is refused too
	for _, expect := range []any{map[string]any{}, map[string]any{"twitch": 42}, "twitch", []any{shown}} {
		_, out := a.send(map[string]any{"target": "twitch", "text": "x", "expect": expect}, nil)
		if results(t, out)["twitch"].(map[string]any)["code"] != "target_changed" {
			t.Errorf("expect %v: %v", expect, out)
		}
	}
	if twitch.count() != 0 {
		t.Fatal("sent to a target the page didn't show")
	}
	// the pinned source went away: no target, and no hop to another source
	current = map[string]targets.Target{"twitch": {Choices: []targets.Choice{{ID: "cccc3333", Label: "#someoneelse"}}}, "youtube": {Choices: []targets.Choice{}}}
	_, out = a.send(map[string]any{"target": "twitch", "text": "x", "expect": map[string]any{"twitch": shown}}, nil)
	if want := map[string]any{"ok": false, "code": "no_target", "reason": "No Twitch reply target is chosen — pick one in the dock."}; !reflect.DeepEqual(results(t, out)["twitch"], want) {
		t.Fatalf("%v", out)
	}
	tw := a.platform("twitch")
	if tw["connected"] != false || tw["target"] != nil || !reflect.DeepEqual(tw["choices"], []any{map[string]any{"id": "cccc3333", "label": "#someoneelse"}}) {
		t.Fatalf("session %v", tw)
	}
	if twitch.count() != 0 {
		t.Fatal("hopped")
	}
}

func TestLengthEachPlatformsOwnLimitCheckedForBothBeforeAnythingIsSent(t *testing.T) {
	twitch := &fakeSender{signedIn: true, as: "example_user", maxLength: 500}
	youtube := &fakeSender{signedIn: true, as: "example_user", maxLength: 200}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch, "youtube": youtube}})
	long := strings.Repeat("x", 201)
	// fits Twitch, not YouTube: a "both" must not land on Twitch alone
	code, out := a.send(map[string]any{"target": "both", "text": long}, nil)
	want := map[string]any{"error": "Too long for YouTube — 200 characters at most, this is 201. Nothing was sent.", "code": "too_long", "platform": "youtube", "limit": float64(200), "length": float64(201)}
	if code != 400 || !reflect.DeepEqual(out, want) || twitch.count()+youtube.count() != 0 {
		t.Fatalf("%d %v", code, out)
	}
	if _, out := a.send(map[string]any{"target": "youtube", "text": long}, nil); out["platform"] != "youtube" || youtube.count() != 0 {
		t.Fatalf("%v", out)
	}
	// the same text to Twitch alone is fine, and 200 exactly fits both
	if code, _ := a.send(map[string]any{"target": "twitch", "text": long}, nil); code != 200 {
		t.Fatal("twitch alone")
	}
	if code, _ := a.send(map[string]any{"target": "both", "text": strings.Repeat("x", 200)}, nil); code != 200 {
		t.Fatal("200 to both")
	}
	if twitch.count() != 2 || youtube.count() != 1 {
		t.Fatalf("sent %d %d", twitch.count(), youtube.count())
	}
	// counted in characters, not UTF-16 units
	if code, _ := a.send(map[string]any{"target": "youtube", "text": strings.Repeat("😀", 200)}, nil); code != 200 {
		t.Fatal("200 emoji")
	}
}

// The send half of sendapi.test.js 14: a wrong-channel warning doesn't stop
// a reply.
func TestWrongChannelStillSends(t *testing.T) {
	all := fixedTargets()
	y := all["youtube"]
	y.OwnerID = str("UCfakefakefakefakefakeBB")
	all["youtube"] = y
	youtube := &fakeSender{signedIn: true, as: "Example Channel (@example2960)", id: "UCfakefakefakefakefakeAA"}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": newFakeSender(), "youtube": youtube}, Targets: func() map[string]targets.Target { return all }})
	_, out := a.send(map[string]any{"target": "youtube", "text": "hello"}, nil)
	if results(t, out)["youtube"].(map[string]any)["ok"] != true {
		t.Fatalf("%v", out)
	}
}

func TestSendWhileDisabledSaysWhy(t *testing.T) {
	const why = "Sending is off: the sending lock is contested."
	twitch := newFakeSender()
	a := startAPI(Options{Senders: map[string]Sender{"twitch": twitch}, Disabled: func() string { return why }})
	_, out := a.send(map[string]any{"target": "twitch", "text": "hi"}, nil)
	if got := results(t, out)["twitch"]; !reflect.DeepEqual(got, map[string]any{"ok": false, "code": "sending_disabled", "reason": why}) || twitch.count() != 0 {
		t.Fatalf("%v", got)
	}
}

// Both platforms answer in a fixed order, and are asked at the same time.
func TestBothAreSentTogether(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 2)
	slow := func(name string) *fakeSender {
		f := newFakeSender()
		f.reply = func(string, targets.Target) (SendResult, error) {
			started <- name
			<-release
			return SendResult{OK: true}, nil
		}
		return f
	}
	a := startAPI(Options{Senders: map[string]Sender{"twitch": slow("twitch"), "youtube": slow("youtube")}})
	done := make(chan string)
	go func() {
		rec := a
		_, out := rec.send(map[string]any{"target": "both", "text": "hi"}, nil)
		b, _ := marshalNoEscape(out)
		done <- string(b)
	}()
	<-started
	<-started // both are in flight before either answers
	close(release)
	if got := <-done; got != `{"results":{"twitch":{"ok":true},"youtube":{"ok":true}}}` {
		t.Fatalf("%s", got)
	}
}
