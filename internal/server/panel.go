package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/IamMrCupp/duplexchat/internal/sources"
)

// Panel is what the control panel's buttons do.
type Panel interface {
	AddSource(link string) (sources.Entry, error)
	RemoveSource(id string) bool
	ReconnectSource(id string) bool
	Test()
	ClearAll()
}

var (
	sourceRE    = regexp.MustCompile(`^/api/sources/([a-f0-9]+)$`)
	reconnectRE = regexp.MustCompile(`^/api/sources/([a-f0-9]+)/reconnect$`)
)

// maxBody is the panel's request body limit (multichat readBody).
const maxBody = 64 << 10

// panel answers the panel's routes; false means none matched. Every one is
// gated — the gate runs before the body is read (multichat server.js).
func (s *Server) panel(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	post, del := r.Method == http.MethodPost, r.Method == http.MethodDelete
	write := post && (p == "/api/sources" || p == "/api/test" || p == "/api/clear" || reconnectRE.MatchString(p)) ||
		del && sourceRE.MatchString(p)
	if !write {
		return false
	}
	if !s.guard(w, r, "") {
		return true
	}
	ok := map[string]bool{"ok": true}
	switch {
	case post && p == "/api/sources":
		s.addSource(w, r)
	case del:
		code := http.StatusNotFound
		if s.opts.Panel.RemoveSource(sourceRE.FindStringSubmatch(p)[1]) {
			code = http.StatusOK
		}
		writeJSON(w, code, ok) // multichat answers {ok:true} either way
	case post && p == "/api/test":
		s.opts.Panel.Test()
		writeJSON(w, http.StatusOK, ok)
	case post && p == "/api/clear":
		s.opts.Panel.ClearAll()
		writeJSON(w, http.StatusOK, ok)
	default: // reconnect
		code := http.StatusNotFound
		if s.opts.Panel.ReconnectSource(reconnectRE.FindStringSubmatch(p)[1]) {
			code = http.StatusOK
		}
		writeJSON(w, code, ok)
	}
	return true
}

type errBody struct {
	Error string `json:"error"`
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusBadRequest, errBody{"body too large"})
			return
		}
		panic(err)
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{"bad json"})
		return
	}
	obj, _ := body.(map[string]any) // null or not an object: no url (multichat 500s on null)
	link := strings.TrimSpace(jsString(obj["url"]))
	if link == "" {
		writeJSON(w, http.StatusBadRequest, errBody{"That link is empty"})
		return
	}
	e, err := s.opts.Panel.AddSource(link)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Source struct {
			ID       string `json:"id"`
			URL      string `json:"url"`
			Platform string `json:"platform"`
		} `json:"source"`
	}{Source: struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		Platform string `json:"platform"`
	}{e.ID, e.URL, e.Platform}})
}

// jsString is String(v || ”) for a decoded JSON value.
func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
	case float64:
		if x != 0 {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
	case nil:
	default:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
	}
	return ""
}
