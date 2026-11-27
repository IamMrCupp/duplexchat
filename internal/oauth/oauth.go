// Package oauth is what the Twitch and YouTube senders share
// (lib/oauth-session.js at 29a9d2c): the HTTP helpers, and the life of an
// OAuth sign-in on disk. It reads the tokens, refreshes them once when
// they're close to expiring, retries one call after a 401, and forgets them
// when the platform says they're dead.
package oauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

// DefaultTimeout bounds one call, body included.
const DefaultTimeout = 10 * time.Second

// ErrSignedOut means there's no usable sign-in.
var ErrSignedOut = errors.New("signed out")

// Fail is the failure shape every sender returns:
// {ok: false, code, reason, ...extra}.
func Fail(code, reason string, extra *jsjson.Object) *jsjson.Object {
	o := jsjson.NewObject().Set("ok", false).Set("code", code).Set("reason", reason)
	if extra != nil {
		for _, k := range extra.Keys() {
			v, _ := extra.Get(k)
			o.Set(k, v)
		}
	}
	return o
}

// Response is an answer: its status, its body as JSON (nil when it's empty
// or isn't JSON), and its headers.
type Response struct {
	Status int
	JSON   any
	Header http.Header
}

// Object is the body when it's a JSON object, else an empty one
// (`r.json || {}` as the senders read it).
func (r Response) Object() *jsjson.Object {
	if o, ok := r.JSON.(*jsjson.Object); ok {
		return o
	}
	return jsjson.NewObject()
}

// Fetch makes one call. timeout 0 is DefaultTimeout; client nil is
// http.DefaultClient. A network failure or a timeout is an error.
func Fetch(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	var parsed any // a body that's empty, not JSON or cut off reads as nil
	if body, err := io.ReadAll(resp.Body); err == nil {
		if v, err := jsjson.Parse(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))); err == nil {
			parsed = v
		}
	}
	return Response{Status: resp.StatusCode, JSON: parsed, Header: resp.Header}, nil
}

// Param is one form field. Forms keep their order, as URLSearchParams does.
type Param struct{ Key, Value string }

// Form is URLSearchParams#toString: application/x-www-form-urlencoded,
// leaving only ASCII letters, digits and *-._ as they are and writing a
// space as +. (url.Values sorts its keys and escapes * and ~ differently.)
func Form(params []Param) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		formEscape(&b, p.Key)
		b.WriteByte('=')
		formEscape(&b, p.Value)
	}
	return b.String()
}

func formEscape(b *strings.Builder, s string) {
	const hex = "0123456789ABCDEF"
	for _, c := range []byte(strings.ToValidUTF8(s, "�")) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
}

// PostForm posts params as a form.
func PostForm(ctx context.Context, client *http.Client, url string, params []Param, timeout time.Duration) (Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(Form(params)))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	return Fetch(ctx, client, req, timeout)
}
