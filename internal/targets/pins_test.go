package targets

import (
	"encoding/json"
	"testing"
)

func pinsOver(list *[]Source) *Pins { return New(func() []Source { return *list }) }

func replyTo(t *testing.T, p *Pins) string {
	t.Helper()
	b, err := json.Marshal(p.ToConfig())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOnlySourceIsPinned(t *testing.T) {
	var list []Source
	p := pinsOver(&list)
	a := Source{ID: "a", URL: "https://twitch.tv/a", Platform: "twitch"}
	list = append(list, a)
	p.Added(a)
	b := Source{ID: "b", URL: "https://twitch.tv/b", Platform: "twitch"}
	list = append(list, b)
	p.Added(b) // a second source doesn't move the pin
	if got := replyTo(t, p); got != `{"twitch":"https://twitch.tv/a"}` {
		t.Errorf("replyTo = %s", got)
	}
}

func TestRemovingThePinLeavesNoTarget(t *testing.T) {
	a := Source{ID: "a", URL: "https://twitch.tv/a", Platform: "twitch"}
	b := Source{ID: "b", URL: "https://twitch.tv/b", Platform: "twitch"}
	list := []Source{a}
	p := pinsOver(&list)
	p.Added(a)
	list = append(list, b)
	p.Added(b)
	list = list[1:]
	p.Removed(a)
	if got := replyTo(t, p); got != `{}` {
		t.Errorf("replyTo = %s, want no target (no hopping to b)", got)
	}
}

func TestRestoreByURL(t *testing.T) {
	list := []Source{
		{ID: "a", URL: "https://twitch.tv/a", Platform: "twitch"},
		{ID: "b", URL: "https://twitch.tv/b", Platform: "twitch"},
		{ID: "y", URL: "https://youtube.com/@y", Platform: "youtube"},
	}
	p := pinsOver(&list)
	p.Restore(map[string]any{"twitch": "https://twitch.tv/b", "youtube": "https://youtube.com/@gone", "other": "x"})
	if got := replyTo(t, p); got != `{"twitch":"https://twitch.tv/b"}` {
		t.Errorf("replyTo = %s", got)
	}
	p.Restore("not an object") // ignored
	p.Added(Source{ID: "z", URL: "x", Platform: "kick"})
	if got := replyTo(t, p); got != `{"twitch":"https://twitch.tv/b"}` {
		t.Errorf("after junk: replyTo = %s", got)
	}
}
