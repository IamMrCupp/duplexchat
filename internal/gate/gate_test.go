package gate

// multichat test/gate.test.js at 29a9d2c, case for case.

import (
	"net/http"
	"testing"
)

const port = 3131

var token = "fake-token-for-tests"

// req is the source's req(): a well-formed local request, with overrides.
// A nil value deletes that header.
func req(headers map[string]*string, peer string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3131/api/send", nil)
	r.RemoteAddr = "[" + peer + "]:50000"
	set := map[string]*string{"Host": s("127.0.0.1:3131"), "Origin": s("http://127.0.0.1:3131"), "X-Dock-Token": &token}
	for k, v := range headers {
		set[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range set {
		if v == nil {
			continue
		}
		if k == "Host" {
			r.Host = *v
			continue
		}
		r.Header[k] = []string{*v}
	}
	if set["Host"] == nil {
		r.Host = ""
	}
	return r
}

func s(v string) *string { return &v }

var opts = Options{Port: port, Origin: Require, Token: &token}

func code(d *Denial) string {
	if d == nil {
		return ""
	}
	return d.Code
}

func TestLoopbackAddresses(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "127.0.0.53", "::1", "::ffff:127.0.0.1"} {
		if !IsLoopbackAddress(a) {
			t.Errorf("%q should be loopback", a)
		}
	}
	for _, a := range []string{"192.168.1.20", "10.0.0.5", "::ffff:192.168.1.20", "fe80::1", "0.0.0.0", "", "127.0.0.1.evil", "1127.0.0.1", "127.0.0.256"} {
		if IsLoopbackAddress(a) {
			t.Errorf("%q shouldn't be loopback", a)
		}
	}
}

func TestWellFormedLocalRequestPasses(t *testing.T) {
	cases := []*http.Request{
		req(nil, "127.0.0.1"),
		req(map[string]*string{"Host": s("localhost:3131"), "Origin": s("http://localhost:3131")}, "127.0.0.1"),
		req(map[string]*string{"Host": s("[::1]:3131"), "Origin": s("http://[::1]:3131")}, "::1"),
		req(map[string]*string{"Sec-Fetch-Site": s("same-origin")}, "127.0.0.1"),
	}
	for i, r := range cases {
		if d := Check(r, opts); d != nil {
			t.Errorf("case %d refused: %+v", i, d)
		}
	}
}

func TestNonLoopbackPeerRefused(t *testing.T) {
	for _, peer := range []string{"192.168.1.20", "::ffff:10.0.0.5"} {
		if c := code(Check(req(nil, peer), opts)); c != "not_loopback" {
			t.Errorf("%s: %q", peer, c)
		}
	}
	r := req(nil, "127.0.0.1")
	r.RemoteAddr = "" // no address at all
	if c := code(Check(r, opts)); c != "not_loopback" {
		t.Errorf("no address: %q", c)
	}
}

func TestProxyRefused(t *testing.T) {
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		for _, v := range []string{"203.0.113.9", ""} { // present counts, even empty
			if c := code(Check(req(map[string]*string{h: s(v)}, "127.0.0.1"), opts)); c != "proxied" {
				t.Errorf("%s=%q: %q", h, v, c)
			}
		}
	}
}

func TestForeignHostRefused(t *testing.T) {
	for _, host := range []string{"evil.example:3131", "evil.example", "127.0.0.1:9999", "127.0.0.1", "127.0.0.1:3131.evil.example", "192.168.1.20:3131"} {
		if c := code(Check(req(map[string]*string{"Host": s(host)}, "127.0.0.1"), opts)); c != "bad_host" {
			t.Errorf("%s: %q", host, c)
		}
	}
	if c := code(Check(req(map[string]*string{"Host": nil}, "127.0.0.1"), opts)); c != "bad_host" {
		t.Errorf("no Host: %q", c)
	}
}

func TestCrossOriginRefused(t *testing.T) {
	for _, o := range []string{"https://evil.example", "http://evil.example:3131", "null", "https://127.0.0.1:3131", "http://127.0.0.1:9999", "http://127.0.0.1:3131.evil.example", ""} {
		if c := code(Check(req(map[string]*string{"Origin": s(o)}, "127.0.0.1"), opts)); c != "bad_origin" {
			t.Errorf("Origin %q: %q", o, c)
		}
	}
	for _, site := range []string{"cross-site", "same-site", "none"} {
		if c := code(Check(req(map[string]*string{"Sec-Fetch-Site": s(site)}, "127.0.0.1"), opts)); c != "bad_origin" {
			t.Errorf("Sec-Fetch-Site %q: %q", site, c)
		}
	}
	// a repeated Origin is joined, as Node does, and fails
	r := req(nil, "127.0.0.1")
	r.Header["Origin"] = []string{"http://127.0.0.1:3131", "http://127.0.0.1:3131"}
	if c := code(Check(r, opts)); c != "bad_origin" {
		t.Errorf("repeated Origin: %q", c)
	}
}

func TestOriginRequiredForWritesOptionalForReads(t *testing.T) {
	r := req(map[string]*string{"Origin": nil}, "127.0.0.1")
	if c := code(Check(r, opts)); c != "bad_origin" {
		t.Errorf("write without Origin: %q", c)
	}
	read := opts
	read.Origin = IfPresent
	if d := Check(r, read); d != nil {
		t.Errorf("read without Origin refused: %+v", d)
	}
	if c := code(Check(req(map[string]*string{"Origin": s("https://evil.example")}, "127.0.0.1"), read)); c != "bad_origin" {
		t.Errorf("read with evil Origin: %q", c)
	}
	// typing the URL: Sec-Fetch-Site none is fine for a read, not a write
	typed := req(map[string]*string{"Origin": nil, "Sec-Fetch-Site": s("none")}, "127.0.0.1")
	if d := Check(typed, read); d != nil {
		t.Errorf("typed-URL read refused: %+v", d)
	}
	skip := opts
	skip.Origin = Skip
	if d := Check(req(map[string]*string{"Origin": s("https://accounts.example"), "Sec-Fetch-Site": s("cross-site")}, "127.0.0.1"), skip); d != nil {
		t.Errorf("skip mode refused: %+v", d)
	}
}

func TestToken(t *testing.T) {
	cases := map[string]*string{"missing": nil, "empty": s("")}
	for name, v := range cases {
		if c := code(Check(req(map[string]*string{"X-Dock-Token": v}, "127.0.0.1"), opts)); c != "no_token" {
			t.Errorf("%s: %q", name, c)
		}
	}
	for _, v := range []string{"wrong", token + "x", token[:len(token)-1] + "X"} {
		if c := code(Check(req(map[string]*string{"X-Dock-Token": s(v)}, "127.0.0.1"), opts)); c != "bad_token" {
			t.Errorf("%q: %q", v, c)
		}
	}
	r := req(nil, "127.0.0.1")
	r.Header["X-Dock-Token"] = []string{token, token}
	if c := code(Check(r, opts)); c != "bad_token" {
		t.Errorf("repeated token: %q", c)
	}
	noToken := opts
	noToken.Token = nil
	if d := Check(req(map[string]*string{"X-Dock-Token": nil}, "127.0.0.1"), noToken); d != nil {
		t.Errorf("token not required, still refused: %+v", d)
	}
}

func TestTokensLongAndUnique(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || len(a) < 43 {
		t.Errorf("tokens %q %q", a, b)
	}
}

func TestPeerSeam(t *testing.T) {
	o := opts
	o.Peer = s("192.168.1.20")
	if c := code(Check(req(nil, "127.0.0.1"), o)); c != "not_loopback" {
		t.Errorf("seam: %q", c)
	}
}
