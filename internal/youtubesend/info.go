package youtubesend

// Stream info (title, category, tags):
//
//	liveBroadcasts.list     1 unit   find the live (or next upcoming) broadcast
//	videos.list             1 unit   its tags: a broadcast resource has none
//	videoCategories.list    1 unit   once per run, for the category menu
//	liveBroadcasts.update   1 unit   title and category
//	videos.update          50 units  only when the tags changed
//	  https://developers.google.com/youtube/v3/live/docs/liveBroadcasts/list
//	  https://developers.google.com/youtube/v3/live/docs/liveBroadcasts/update
//	  https://developers.google.com/youtube/v3/docs/videos/update
//	  https://developers.google.com/youtube/v3/docs/videoCategories/list
//
// Both update calls delete any property the request leaves out, so each one
// sends back what it read for the fields it isn't changing.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
	"github.com/IamMrCupp/duplexchat/internal/oauth"
	"github.com/IamMrCupp/duplexchat/internal/server"
)

// liveBroadcasts.update: "snippet.title must be between 1 and 100
// characters long". Video resource: title "may contain all valid UTF-8
// characters except < and >"; snippet.tags[] "has a maximum length of 500
// characters", counting the commas between tags and two quotation marks for
// a tag with a space in it.
//
//	https://developers.google.com/youtube/v3/docs/videos#snippet.tags[]
const (
	InfoTitleLimit = 100
	InfoTagsTotal  = 500

	categoryRegion = "US"
)

var (
	_ server.InfoSender     = (*Sender)(nil)
	_ server.InfoChecker    = (*Sender)(nil)
	_ server.CategoryLister = (*Sender)(nil)
)

// TagsLength is how YouTube counts the tags against its 500.
func TagsLength(tags []string) int {
	n := 0
	for _, t := range tags {
		n += utf8.RuneCountInString(t)
		if strings.IndexFunc(t, jsjson.IsSpace) >= 0 {
			n += 2
		}
	}
	if len(tags) > 1 {
		n += len(tags) - 1
	}
	return n
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// InfoStatus is the stream info panel's view of the sign-in and the quota.
func (s *Sender) InfoStatus() server.InfoStatus {
	st := s.Status()
	detail := st.Detail
	switch {
	case !st.Configured:
		detail = "YouTube is off until a Google client is set up — open /setup."
	case !st.SignedIn && detail == "":
		detail = "Not signed in to YouTube."
	}
	s.mu.Lock()
	exhausted := s.now() < s.quotaBlockedUntil
	s.mu.Unlock()
	return server.InfoStatus{
		Configured: st.Configured, SignedIn: st.SignedIn, As: st.As,
		Ready: st.SignedIn, Detail: detail,
		Limits: ok("title", float64(InfoTitleLimit), "tagsTotal", float64(InfoTagsTotal)),
		Quota: ok(
			"load", float64(CostBroadcastsList+CostVideosList),
			"saveTitle", float64(CostBroadcastsList+CostVideosList+CostBroadcastsUpdate),
			"saveTags", float64(CostBroadcastsList+CostVideosList+CostVideosUpdate),
			"dailyDefault", float64(CostDailyDefault),
			"exhausted", exhausted,
		),
	}
}

// CheckInfo is what YouTube rejects, said before any quota is spent.
func (s *Sender) CheckInfo(title string, tags []string) string {
	if title == "" {
		return "YouTube needs a title."
	}
	if utf8.RuneCountInString(title) > InfoTitleLimit {
		return fmt.Sprintf("YouTube titles are %d characters at most.", InfoTitleLimit)
	}
	if strings.ContainsAny(title, "<>") {
		return "YouTube titles can't contain < or >."
	}
	for _, t := range tags {
		if strings.ContainsAny(t, "<>,") {
			return "YouTube tags can't contain < > or a comma."
		}
	}
	if TagsLength(tags) > InfoTagsTotal {
		return fmt.Sprintf("YouTube tags add up to %d characters at most.", InfoTagsTotal)
	}
	return ""
}

func failObj(code, reason string) *jsjson.Object {
	return ok("ok", false, "code", code, "reason", reason)
}

func resultObj(r server.SendResult) *jsjson.Object {
	o := failObj(r.Code, r.Reason)
	if r.RetryAfter != 0 { // the quota hold's, which is never 0 while it holds
		o.Set("retryAfter", float64(r.RetryAfter))
	}
	return o
}

// refused carries Google's answer out of a read, for info to return.
type refused struct{ answer *jsjson.Object }

func (r refused) Error() string { return "refused" }

// info runs one stream info call. Whatever goes wrong on the way is an
// answer, not an error.
func (s *Sender) info(work func() (*jsjson.Object, error)) (*jsjson.Object, error) {
	signedOut := func() *jsjson.Object {
		reason := s.session.Problem()
		if reason == "" {
			reason = "Not signed in to YouTube."
		}
		return failObj("not_signed_in", reason)
	}
	if !s.Status().SignedIn {
		return signedOut(), nil
	}
	s.mu.Lock()
	if s.now() < s.quotaBlockedUntil {
		r := s.quotaBlocked()
		s.mu.Unlock()
		return resultObj(r), nil
	}
	s.mu.Unlock()
	out, err := work()
	if err != nil {
		var no refused
		switch {
		case errors.As(err, &no):
			return no.answer, nil
		case errors.Is(err, oauth.ErrSignedOut):
			return signedOut(), nil
		}
		return failObj("unreachable", "Couldn't reach YouTube."), nil
	}
	return out, nil
}

// refusal is Google's refusal as an answer for info to return.
func (s *Sender) refusalObj(r oauth.Response) (*jsjson.Object, error) {
	res, err := s.refusal(r, "")
	if err != nil {
		return nil, err
	}
	return resultObj(res), nil
}

// read is a GET that either answers 200 or comes back refused.
func (s *Sender) read(ctx context.Context, pathAndQuery string, cost int) ([]any, error) {
	r, err := s.api(ctx, http.MethodGet, pathAndQuery, nil, cost)
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		answer, err := s.refusalObj(r)
		if err != nil {
			return nil, err
		}
		return nil, refused{answer}
	}
	items, _ := get(r.JSON, "items").([]any) // not a list: none
	return items, nil
}

type broadcast struct {
	id, state string
	snippet   any
}

// errNullBroadcast is the source's TypeError: sorting upcoming broadcasts
// reads (null).snippet.
var errNullBroadcast = errors.New("a null broadcast in the list")

// broadcasts are the signed-in channel's broadcasts a save could apply to:
// whatever is live now, or (when nothing is) whatever is scheduled, soonest
// first. The reference wants exactly one filter per call, so that is two
// calls at most. broadcastType=all includes the stream-key ("persistent")
// broadcast.
func (s *Sender) broadcasts(ctx context.Context) ([]broadcast, error) {
	list := func(status string) ([]any, error) {
		return s.read(ctx, "/liveBroadcasts?part=id,snippet&broadcastStatus="+status+"&broadcastType=all&maxResults=50", CostBroadcastsList)
	}
	state := "live"
	items, err := list("active")
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		state = "upcoming"
		if items, err = list("upcoming"); err != nil {
			return nil, err
		}
		if len(items) > 1 {
			for _, b := range items {
				if b == nil {
					return nil, errNullBroadcast
				}
			}
		}
		// localeCompare on ISO times: the same order as comparing the strings
		start := func(b any) string { return str(get(b, "snippet"), "scheduledStartTime", "") }
		sort.SliceStable(items, func(i, j int) bool { return start(items[i]) < start(items[j]) })
	}
	out := []broadcast{}
	for _, b := range items {
		if !jsjson.Truthy(b) || !truthy(b, "id") || !truthy(b, "snippet") {
			continue
		}
		out = append(out, broadcast{id: jsjson.ToString(get(b, "id")), state: state, snippet: get(b, "snippet")})
	}
	return out, nil
}

// current is the broadcast a save goes to, and the video that carries its
// tags. The reference says a broadcast "represents exactly one YouTube
// video" without saying the two share an ID, so that is checked here, not
// assumed: if videos.list has nothing under the broadcast's ID, tags are
// left alone.
func (s *Sender) current(ctx context.Context, want string) (all []broadcast, chosen broadcast, video any, err error) {
	all, err = s.broadcasts(ctx)
	if err != nil {
		return
	}
	if len(all) == 0 {
		err = refused{failObj("no_broadcast", "No live or upcoming broadcast on this channel. Create one in YouTube Studio first.")}
		return
	}
	chosen = all[0]
	if want != "" {
		for _, b := range all {
			if b.id == want {
				chosen = b
				break
			}
		}
	}
	videos, err := s.read(ctx, "/videos?part=snippet&id="+oauth.EncodeURIComponent(chosen.id), CostVideosList)
	if err != nil {
		return
	}
	for _, v := range videos {
		if jsjson.Truthy(v) && get(v, "id") == chosen.id && truthy(v, "snippet") {
			video = v
			break
		}
	}
	return
}

// videoTags is the video's tags as strings, [] when it has none.
func videoTags(video any) []string {
	tags := []string{}
	if l, isList := get(get(video, "snippet"), "tags").([]any); video != nil && isList {
		for _, t := range l {
			tags = append(tags, jsjson.ToString(t))
		}
	}
	return tags
}

func untitled(b broadcast) string { return str(b.snippet, "title", "Untitled") }

// GetInfo reads the broadcast want names (or the live or soonest one):
// {ok, info: {title, category, tags, tagsKnown, where, key, choices}}.
func (s *Sender) GetInfo(ctx context.Context, want string) (*jsjson.Object, error) {
	return s.info(func() (*jsjson.Object, error) {
		all, chosen, video, err := s.current(ctx, want)
		if err != nil {
			return nil, err
		}
		sn := chosen.snippet
		var categoryID any = ""
		if video != nil {
			categoryID = or(get(video, "snippet"), "categoryId", or(sn, "categoryId", ""))
		} else {
			categoryID = or(sn, "categoryId", "")
		}
		var category any
		if jsjson.Truthy(categoryID) {
			category = ok("id", jsjson.ToString(categoryID), "name", "")
		}
		tags := []any{}
		for _, t := range videoTags(video) {
			tags = append(tags, t)
		}
		choices := make([]any, len(all))
		for i, b := range all {
			choices[i] = ok("id", b.id, "label", untitled(b)+" ("+b.state+")")
		}
		return ok("ok", true, "info", ok(
			"title", str(sn, "title", ""),
			"category", category,
			"tags", tags,
			"tagsKnown", video != nil,
			"where", "YouTube ("+chosen.state+")",
			"key", chosen.id,
			"choices", choices,
		)), nil
	})
}

// copyIf sets k from v's own k when that's truthy.
func copyIf(o *jsjson.Object, v any, k string) {
	if x := get(v, k); jsjson.Truthy(x) {
		o.Set(k, x)
	}
}

// SetInfo saves to the broadcast the page was showing (c.Expect), and to
// nothing else. A nil or "" CategoryID leaves the category alone (YouTube
// has no "no category").
func (s *Sender) SetInfo(ctx context.Context, c server.InfoChange) (*jsjson.Object, error) {
	expect := ""
	if c.Expect != nil {
		expect = *c.Expect
	}
	categoryID := ""
	if c.CategoryID != nil {
		categoryID = *c.CategoryID
	}
	tags := make([]any, len(c.Tags))
	for i, t := range c.Tags {
		tags[i] = t
	}
	return s.info(func() (*jsjson.Object, error) {
		_, chosen, video, err := s.current(ctx, expect)
		if err != nil {
			return nil, err
		}
		if expect == "" || chosen.id != expect {
			return failObj("target_changed", `The YouTube broadcast is now "`+untitled(chosen)+`" — check it and save again.`), nil
		}
		sn := chosen.snippet
		if video != nil && !sameList(videoTags(video), c.Tags) {
			vs := get(video, "snippet")
			var category any = categoryID
			if categoryID == "" {
				category = get(vs, "categoryId")
			}
			if !jsjson.Truthy(category) {
				return failObj("no_category", "YouTube needs a category to save tags — pick one."), nil
			}
			snippet := ok("title", c.Title, "categoryId", jsjson.ToString(category), "tags", tags, "description", or(vs, "description", ""))
			copyIf(snippet, vs, "defaultLanguage")
			copyIf(snippet, vs, "defaultAudioLanguage")
			r, err := s.api(ctx, http.MethodPut, "/videos?part=snippet", ok("id", chosen.id, "snippet", snippet), CostVideosUpdate)
			if err != nil {
				return nil, err
			}
			if r.Status == 200 {
				return ok("ok", true, "cost", float64(CostVideosUpdate)), nil
			}
			return s.refusalObj(r)
		}
		var category any = categoryID
		if categoryID == "" {
			category = get(sn, "categoryId")
		}
		snippet := ok("title", c.Title, "description", or(sn, "description", ""))
		// undefined drops out of JSON.stringify; null stays
		if v, has := jsjson.Get(sn, "scheduledStartTime"); has {
			snippet.Set("scheduledStartTime", v)
		}
		copyIf(snippet, sn, "scheduledEndTime")
		if jsjson.Truthy(category) {
			snippet.Set("categoryId", jsjson.ToString(category))
		}
		r, err := s.api(ctx, http.MethodPut, "/liveBroadcasts?part=snippet", ok("id", chosen.id, "snippet", snippet), CostBroadcastsUpdate)
		if err != nil {
			return nil, err
		}
		if r.Status != 200 {
			return s.refusalObj(r)
		}
		out := ok("ok", true, "cost", float64(CostBroadcastsUpdate))
		if video == nil && len(c.Tags) > 0 {
			out.Set("note", "Title saved. Tags were not: YouTube has no video for this broadcast yet.")
		}
		return out, nil
	})
}

// Categories is the category menu. Asked once per run; categories rarely
// change.
func (s *Sender) Categories(ctx context.Context) (*jsjson.Object, error) {
	s.mu.Lock()
	cached := s.categoryList
	s.mu.Unlock()
	if cached != nil {
		return ok("ok", true, "categories", cached), nil
	}
	return s.info(func() (*jsjson.Object, error) {
		items, err := s.read(ctx, "/videoCategories?part=snippet&regionCode="+categoryRegion, CostCategoriesList)
		if err != nil {
			return nil, err
		}
		list := []any{}
		for _, c := range items {
			sn := get(c, "snippet")
			if !jsjson.Truthy(c) || !truthy(c, "id") || !jsjson.Truthy(sn) || get(sn, "assignable") == false {
				continue
			}
			list = append(list, ok("id", jsjson.ToString(get(c, "id")), "name", jsjson.ToString(or(sn, "title", get(c, "id")))))
		}
		s.mu.Lock()
		s.categoryList = list
		s.mu.Unlock()
		return ok("ok", true, "categories", list), nil
	})
}
