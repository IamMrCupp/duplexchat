// Package gate guards every route that can change something: the panel's
// controls now, sending and signing in later (multichat lib/gate.js at
// 29a9d2c). A request passes only when
//
//  1. it came from this machine: a loopback peer and no proxy headers;
//  2. its Host names this server on a loopback name, which stops DNS
//     rebinding (a hostile page re-pointing its own name at 127.0.0.1 still
//     sends its own name in Host);
//  3. its Origin, and Sec-Fetch-Site when sent, are this server, which stops
//     another site's page posting here;
//  4. it carries the per-process session token, which only this server's own
//     pages can read.
package gate

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// LoopbackNames are the Host/Origin names that count as this machine.
var LoopbackNames = []string{"127.0.0.1", "localhost", "[::1]"}

var v4RE = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$`)

// IsLoopbackAddress: ::1, or any IPv4 127.x.x.x, plain or IPv4-mapped.
func IsLoopbackAddress(addr string) bool {
	if addr == "" {
		return false
	}
	if addr == "::1" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(addr), "::ffff:") {
		addr = addr[7:]
	}
	m := v4RE.FindStringSubmatch(addr)
	if m == nil || m[1] != "127" {
		return false
	}
	for _, o := range m[1:] {
		if n, _ := strconv.Atoi(o); n > 255 {
			return false
		}
	}
	return true
}

// HostAllowed: Host is a loopback name with this server's port.
func HostAllowed(host string, port int) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	for _, n := range LoopbackNames {
		if h == n+":"+strconv.Itoa(port) {
			return true
		}
	}
	return false
}

// OriginAllowed: Origin is this server, plain http on a loopback name.
func OriginAllowed(origin string, port int) bool {
	o := strings.ToLower(strings.TrimSpace(origin))
	for _, n := range LoopbackNames {
		if o == "http://"+n+":"+strconv.Itoa(port) {
			return true
		}
	}
	return false
}

// NewToken is 32 random bytes, base64url without padding (43 characters).
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenMatches compares in constant time once the lengths agree.
func TokenMatches(given, expected string) bool {
	if expected == "" || len(given) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}

// Origin modes.
const (
	Require   = "require"    // Origin must be present and ours: state-changing requests
	IfPresent = "if-present" // Origin may be absent (a same-origin GET); ours if sent
	Skip      = "skip"       // not checked: the OAuth redirect arrives cross-site
)

// Options for one check.
type Options struct {
	Port   int
	Origin string  // Require when empty
	Token  *string // the expected session token; nil when the route can't carry one
	Peer   *string // test seam: the peer address; nil → the connection's
}

// Denial is a refusal: always 403, with a code the pages act on.
type Denial struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
	Error  string `json:"error"`
}

func deny(code, msg string) *Denial {
	return &Denial{Status: http.StatusForbidden, Code: code, Error: msg}
}

// header returns a header as Node sees it: absent, or every value joined
// with ", " (so a repeated Origin or token can't match).
func header(h http.Header, name string) (string, bool) {
	v, ok := h[http.CanonicalHeaderKey(name)]
	if !ok {
		return "", false
	}
	return strings.Join(v, ", "), true
}

// Check returns nil when the request may proceed, in the source's order:
// peer, proxy headers, Host, Origin and Sec-Fetch-Site, token.
func Check(r *http.Request, o Options) *Denial {
	mode := o.Origin
	if mode == "" {
		mode = Require
	}
	peer := ""
	if o.Peer != nil {
		peer = *o.Peer
	} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if !IsLoopbackAddress(peer) {
		return deny("not_loopback", "Sending only works from this machine.")
	}
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		if _, ok := r.Header[h]; ok {
			return deny("proxied", "Sending is refused through a proxy.")
		}
	}
	if !HostAllowed(r.Host, o.Port) {
		return deny("bad_host", "Unexpected Host header.")
	}
	if mode != Skip {
		origin, ok := header(r.Header, "Origin")
		if !ok {
			if mode == Require {
				return deny("bad_origin", "Missing Origin header.")
			}
		} else if !OriginAllowed(origin, o.Port) {
			return deny("bad_origin", "Cross-origin request refused.")
		}
		if site, ok := header(r.Header, "Sec-Fetch-Site"); ok && site != "same-origin" && !(site == "none" && mode == IfPresent) {
			return deny("bad_origin", "Cross-site request refused.")
		}
	}
	if o.Token != nil {
		given, ok := header(r.Header, "X-Dock-Token")
		if !ok || given == "" {
			return deny("no_token", "Missing dock token.")
		}
		if !TokenMatches(given, *o.Token) {
			return deny("bad_token", "Wrong dock token.")
		}
	}
	return nil
}
