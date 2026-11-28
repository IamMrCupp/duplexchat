package youtubesend

// withBroadcasts of test/youtube-send.test.js at 29a9d2c: a fake channel,
// its broadcasts by status, the video behind each one, and a record of
// every write.

import (
	"testing"
)

type world struct {
	active, upcoming []any
	videos           func(id string) any // nil: nothing under that ID
	puts             []call
	refuse           func(c call) reply
	cats             []any // nil: the source's three
}

func bc(id, title string, extra map[string]any) map[string]any {
	sn := map[string]any{"title": title, "description": "Example description for " + id, "scheduledStartTime": "2026-11-23T18:00:00Z", "channelId": channelID}
	for k, v := range extra {
		sn[k] = v
	}
	return map[string]any{"id": id, "snippet": sn}
}

func defaultVideo(id string) any {
	return map[string]any{"id": id, "snippet": map[string]any{"title": "video title", "description": "Example video description", "categoryId": "22",
		"tags": []any{"old", "two words"}, "defaultLanguage": "en", "channelId": channelID, "liveBroadcastContent": "live"}}
}

// serve runs with g.mu held.
func (w *world) serve(c call) reply {
	switch c.key {
	case "GET /youtube/v3/liveBroadcasts":
		var items []any
		switch c.query.Get("broadcastStatus") {
		case "active":
			items = w.active
		case "upcoming":
			items = w.upcoming
		}
		if items == nil {
			items = []any{}
		}
		return reply{200, map[string]any{"items": items}}
	case "GET /youtube/v3/videoCategories":
		if w.cats != nil {
			return reply{200, map[string]any{"items": w.cats}}
		}
		return reply{200, map[string]any{"items": []any{
			map[string]any{"id": "28", "snippet": map[string]any{"title": "Science & Technology", "assignable": true}},
			map[string]any{"id": "20", "snippet": map[string]any{"title": "Gaming", "assignable": true}},
			map[string]any{"id": "30", "snippet": map[string]any{"title": "Movies", "assignable": false}},
		}}}
	}
	w.puts = append(w.puts, c)
	if w.refuse != nil {
		return w.refuse(c)
	}
	return reply{200, c.body}
}

func withBroadcasts(t *testing.T, r *rig) *world {
	t.Helper()
	w := &world{videos: defaultVideo}
	r.g.state(func() {
		r.g.world = w
		r.g.video = func(c call) reply {
			items := []any{}
			if w.videos != nil {
				if v := w.videos(c.query.Get("id")); v != nil {
					items = append(items, v)
				}
			}
			return reply{200, map[string]any{"items": items}}
		}
	})
	return w
}

// edit changes the world while the fake isn't answering.
func (r *rig) edit(fn func()) { r.g.state(fn) }

func (r *rig) puts() []call {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	return append([]call(nil), r.g.world.puts...)
}
