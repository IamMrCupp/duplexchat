package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/IamMrCupp/duplexchat/internal/gate"
	"github.com/IamMrCupp/duplexchat/internal/info"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/oauth"
)

// The stream info panel's HTTP surface (multichat lib/infoapi.js at
// 29a9d2c): one form for title, category and tags, saved to Twitch and
// YouTube. It changes what the streamer's channels say, so every route goes
// through the same gate as sending. Nothing here returns a token or a
// client secret.
//
// Presets are named copies of the form kept in config.json. They hold what
// the form holds (a title, category names and IDs, tags) and never a secret.

// InfoStatus is a platform's readiness for stream info.
type InfoStatus struct {
	Configured, SignedIn bool
	As                   string // "" is null
	NeedsPermission      bool
	Ready                bool
	Detail               string
	Limits               *jsjson.Object // nil is {}
	Quota                *jsjson.Object // nil leaves it out
}

// InfoChange is a save, as the form gave it.
type InfoChange struct {
	Title      string
	Tags       []string
	CategoryID *string // nil: leave the category alone; "": clear it
	Expect     *string // the broadcast the page was showing, if it said
}

// InfoSender is a sender that takes part in stream info. A result is the
// platform's answer as an object: {ok: true, ...} or a failure
// {ok: false, code, reason[, retryAfter]}. An error (or a panic) is logged
// and the page is told only that the platform couldn't be reached.
type InfoSender interface {
	InfoStatus() InfoStatus
	GetInfo(ctx context.Context, want string) (*jsjson.Object, error) // want "" is the default
	SetInfo(ctx context.Context, change InfoChange) (*jsjson.Object, error)
}

// InfoChecker says why the platform would refuse a title or tags, or "".
type InfoChecker interface {
	CheckInfo(title string, tags []string) string
}

// CategorySearcher searches the platform's categories (Twitch: thousands).
type CategorySearcher interface {
	SearchCategories(ctx context.Context, query string) (*jsjson.Object, error)
}

// CategoryLister lists them (YouTube: a short fixed list).
type CategoryLister interface {
	Categories(ctx context.Context) (*jsjson.Object, error)
}

func (s *Server) infoSender(p string) InfoSender {
	if s.disabled() != "" {
		return nil
	}
	snd, _ := s.opts.Senders[p].(InfoSender)
	return snd
}

func failObj(code, reason string, retryAfter any) *jsjson.Object {
	o := jsjson.NewObject().Set("ok", false).Set("code", code).Set("reason", reason)
	if retryAfter != nil {
		o.Set("retryAfter", retryAfter)
	}
	return o
}

// infoStatus is the panel's per-platform state.
func (s *Server) infoStatus() *jsjson.Object {
	off := s.disabled()
	sending := s.platformStatus()
	warnings := map[string]string{"twitch": sending.Twitch.Warning, "youtube": sending.YouTube.Warning}
	out := jsjson.NewObject()
	for _, p := range platforms {
		snd := s.infoSender(p)
		st := InfoStatus{Detail: off}
		if snd != nil {
			st = snd.InfoStatus()
		} else if off == "" {
			st.Detail = fmt.Sprintf("Stream info for %s isn't in this build.", label[p])
		}
		var as any
		if st.As != "" {
			as = st.As
		}
		limits := st.Limits
		if limits == nil {
			limits = jsjson.NewObject()
		}
		warning := ""
		if snd != nil {
			warning = warnings[p]
		}
		e := jsjson.NewObject().
			Set("available", snd != nil).Set("configured", st.Configured).Set("signedIn", st.SignedIn).
			Set("as", as).Set("needsPermission", st.NeedsPermission).Set("ready", st.Ready).
			Set("detail", st.Detail).Set("limits", limits).
			// the same "wrong channel?" warning the dock shows: this panel
			// edits the signed-in channel's stream, whichever chat is read
			Set("warning", warning)
		if st.Quota != nil {
			e.Set("quota", st.Quota)
		}
		categories := "none"
		if snd != nil {
			if _, ok := snd.(CategorySearcher); ok {
				categories = "search"
			} else if _, ok := snd.(CategoryLister); ok {
				categories = "list"
			}
		}
		e.Set("categories", categories)
		out.Set(p, e)
	}
	return out
}

// ask runs one platform call; whatever goes wrong is that platform's answer.
func (s *Server) ask(p string, work func(InfoSender) (*jsjson.Object, error)) (out *jsjson.Object) {
	snd := s.infoSender(p)
	if snd == nil {
		if off := s.disabled(); off != "" {
			return failObj("sending_disabled", off, nil)
		}
		return failObj("unavailable", fmt.Sprintf("Stream info for %s isn't in this build.", label[p]), nil)
	}
	defer func() {
		if v := recover(); v != nil {
			s.opts.Log("info:"+p, fmt.Errorf("panic: %v", v))
			out = failObj("error", fmt.Sprintf("Couldn't reach %s.", label[p]), nil)
		}
	}()
	r, err := work(snd)
	if err != nil {
		s.opts.Log("info:"+p, err)
		return failObj("error", fmt.Sprintf("Couldn't reach %s.", label[p]), nil)
	}
	if r != nil {
		if ok, _ := r.Get("ok"); jsjson.Truthy(ok) {
			return r
		}
	}
	code, reason := "failed", fmt.Sprintf("%s refused.", label[p])
	var retryAfter any
	if r != nil {
		if v, _ := r.Get("code"); jsjson.Truthy(v) {
			code = jsjson.ToString(v)
		}
		if v, _ := r.Get("reason"); jsjson.Truthy(v) {
			reason = jsjson.ToString(v)
		}
		if v, _ := r.Get("retryAfter"); jsjson.Truthy(v) {
			retryAfter = v
		}
	}
	return failObj(code, reason, retryAfter)
}

// plain is a parsed value as encoding/json gives it, which the info
// package's cleaners take.
func plain(v any) any {
	var out any
	json.Unmarshal(jsjson.Stringify(v, ""), &out)
	return out
}

func presetsValue(list []info.Preset) any {
	if list == nil {
		list = []info.Preset{}
	}
	b, err := marshalNoEscape(list)
	if err != nil {
		panic(err)
	}
	v, _ := jsjson.Parse(b)
	return v
}

func (s *Server) presets() []info.Preset {
	if s.opts.GetPresets == nil {
		return info.CleanPresets(nil)
	}
	return info.CleanPresets(s.opts.GetPresets())
}

func (s *Server) setPresets(list []info.Preset) {
	if s.opts.SetPresets != nil {
		s.opts.SetPresets(list)
	}
}

func platformParam(w http.ResponseWriter, q oauth.Query) (string, bool) {
	p, _ := q.Lookup("platform")
	if p != "twitch" && p != "youtube" {
		apiJSON(w, http.StatusBadRequest, apiError{"platform must be twitch or youtube", "bad_platform"})
		return "", false
	}
	return p, true
}

func objectOrEmpty(v any) *jsjson.Object {
	if o, ok := v.(*jsjson.Object); ok && o != nil {
		return o
	}
	return jsjson.NewObject()
}

// info answers /api/info/*; false means the path isn't one of ours.
func (s *Server) info(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/info/") {
		return false
	}
	q := oauth.ParseQuery(r.URL.RawQuery)
	ctx := context.WithoutCancel(r.Context())
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost

	switch {
	case get && p == "/api/info/state":
		if !s.gated(w, r, gate.IfPresent, true) {
			return true
		}
		notices := []any{}
		if off := s.disabled(); off != "" {
			notices = append(notices, off)
		}
		apiRaw(w, http.StatusOK, jsjson.Stringify(jsjson.NewObject().
			Set("platforms", s.infoStatus()).Set("presets", presetsValue(s.presets())).Set("notices", notices), ""))
		return true

	// What the platform has now. Costs YouTube quota, so the page asks when
	// it opens and when told to, not on a timer.
	case get && p == "/api/info/current":
		if !s.gated(w, r, gate.IfPresent, true) {
			return true
		}
		platform, ok := platformParam(w, q)
		if !ok {
			return true
		}
		want, _ := q.Lookup("want")
		if !info.ValidID(want) {
			want = ""
		}
		res := s.ask(platform, func(snd InfoSender) (*jsjson.Object, error) { return snd.GetInfo(ctx, want) })
		apiRaw(w, http.StatusOK, jsjson.Stringify(res, ""))
		return true

	case get && p == "/api/info/categories":
		if !s.gated(w, r, gate.IfPresent, true) {
			return true
		}
		platform, ok := platformParam(w, q)
		if !ok {
			return true
		}
		raw, _ := q.Lookup("q")
		query := info.Clip(raw, 100)
		res := s.ask(platform, func(snd InfoSender) (*jsjson.Object, error) {
			if cs, ok := snd.(CategorySearcher); ok {
				if query == "" {
					return jsjson.NewObject().Set("ok", true).Set("categories", []any{}), nil
				}
				return cs.SearchCategories(ctx, query)
			}
			if cl, ok := snd.(CategoryLister); ok {
				return cl.Categories(ctx)
			}
			return failObj("unavailable", fmt.Sprintf("%s has no categories to pick from.", label[platform]), nil), nil
		})
		apiRaw(w, http.StatusOK, jsjson.Stringify(res, ""))
		return true

	case post && p == "/api/info/save":
		if !s.gated(w, r, gate.Require, true) {
			return true
		}
		b, ok := s.infoBody(w, r)
		if !ok {
			return true
		}
		s.infoSave(ctx, w, b)
		return true

	case post && p == "/api/info/presets":
		if !s.gated(w, r, gate.Require, true) {
			return true
		}
		b, ok := s.infoBody(w, r)
		if !ok {
			return true
		}
		preset := info.CleanPreset(plain(b))
		if preset == nil {
			apiJSON(w, http.StatusBadRequest, apiError{"A preset needs a name.", "bad_preset"})
			return true
		}
		list := s.presets()
		at := -1
		for i, x := range list {
			if strings.ToLower(x.Name) == strings.ToLower(preset.Name) {
				at = i
				break
			}
		}
		if at < 0 && len(list) >= info.MaxPresets {
			apiJSON(w, http.StatusBadRequest, apiError{fmt.Sprintf("%d presets at most — delete one first.", info.MaxPresets), "too_many"})
			return true
		}
		if at < 0 {
			list = append(list, *preset)
		} else {
			list[at] = *preset
		}
		s.setPresets(list)
		apiRaw(w, http.StatusOK, jsjson.Stringify(jsjson.NewObject().Set("presets", presetsValue(list)), ""))
		return true

	case post && p == "/api/info/presets/delete":
		if !s.gated(w, r, gate.Require, true) {
			return true
		}
		b, ok := s.infoBody(w, r)
		if !ok {
			return true
		}
		name, _ := b.Get("name")
		drop := strings.ToLower(info.Clip(name, 40))
		list := []info.Preset{}
		for _, x := range s.presets() {
			if strings.ToLower(x.Name) != drop {
				list = append(list, x)
			}
		}
		s.setPresets(list)
		apiRaw(w, http.StatusOK, jsjson.Stringify(jsjson.NewObject().Set("presets", presetsValue(list)), ""))
		return true
	}

	// Anything else under /api/info/ is nobody's: refuse it here, gated, so
	// an unknown path can't fall through to an ungated handler later.
	mode := gate.Require
	if get {
		mode = gate.IfPresent
	}
	if !s.gated(w, r, mode, true) {
		return true
	}
	apiJSON(w, http.StatusNotFound, apiError{"not found", "not_found"})
	return true
}

func (s *Server) infoBody(w http.ResponseWriter, r *http.Request) (*jsjson.Object, bool) {
	b, err := readJSON(r)
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiError{err.Error(), "bad_request"})
		return nil, false
	}
	return b, true
}

// infoSave is POST /api/info/save: one form to the platforms it names.
func (s *Server) infoSave(ctx context.Context, w http.ResponseWriter, b *jsjson.Object) {
	var wanted []string
	if list, ok := jsjson.Get(b, "platforms"); ok {
		if l, isList := list.([]any); isList {
			for _, p := range platforms {
				for _, x := range l {
					if x == p {
						wanted = append(wanted, p)
						break
					}
				}
			}
		}
	}
	if len(wanted) == 0 {
		apiJSON(w, http.StatusBadRequest, apiError{"Pick Twitch, YouTube or both.", "bad_platforms"})
		return
	}
	rawTitle, _ := b.Get("title")
	title := info.Clip(rawTitle, 500)
	rawTags, _ := b.Get("tags")
	tags := info.CleanTags(plain(rawTags))
	// Checked against every wanted platform before anything is saved, so a
	// title that fits one and not the other doesn't leave the two different.
	for _, p := range wanted {
		if c, ok := s.infoSender(p).(InfoChecker); ok {
			if why := c.CheckInfo(title, tags); why != "" {
				apiJSON(w, http.StatusBadRequest, struct {
					Error    string `json:"error"`
					Code     string `json:"code"`
					Platform string `json:"platform"`
				}{why + " Nothing was saved.", "invalid", p})
				return
			}
		}
	}
	category, expect := jsjson.NewObject(), jsjson.NewObject()
	if v, _ := b.Get("category"); jsjson.Truthy(v) {
		category = objectOrEmpty(v)
	}
	if v, _ := b.Get("expect"); jsjson.Truthy(v) {
		expect = objectOrEmpty(v)
	}
	results := make([]*jsjson.Object, len(wanted))
	var wg sync.WaitGroup
	for i, p := range wanted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			change := InfoChange{Title: title, Tags: tags}
			// absent: leave the category alone. null: clear it. {id}: set it.
			if c, has := category.Get(p); has {
				if c == nil {
					change.CategoryID = new(string)
				} else {
					cc := info.CleanCategory(plain(c))
					if cc == nil {
						results[i] = failObj("bad_category", fmt.Sprintf("That isn't a %s category.", label[p]), nil)
						return
					}
					change.CategoryID = &cc.ID
				}
			}
			if e, _ := expect.Get(p); e != nil {
				if es, isString := e.(string); isString {
					change.Expect = &es
				}
			}
			res := s.ask(p, func(snd InfoSender) (*jsjson.Object, error) { return snd.SetInfo(ctx, change) })
			if ok, _ := res.Get("ok"); jsjson.Truthy(ok) {
				out := jsjson.NewObject().Set("ok", true)
				if v, _ := res.Get("note"); jsjson.Truthy(v) {
					out.Set("note", v)
				}
				if v, _ := res.Get("cost"); jsjson.Truthy(v) {
					out.Set("cost", v)
				}
				res = out
			}
			results[i] = res
		}()
	}
	wg.Wait()
	out := jsjson.NewObject()
	for i, p := range wanted {
		out.Set(p, results[i])
	}
	apiRaw(w, http.StatusOK, jsjson.Stringify(jsjson.NewObject().Set("results", out), ""))
}
