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
	a := Source{"a", "https://twitch.tv/a", "twitch"}
	list = append(list, a)
	p.Added(a)
	b := Source{"b", "https://twitch.tv/b", "twitch"}
	list = append(list, b)
	p.Added(b) // a second source doesn't move the pin
	if got := replyTo(t, p); got != `{"twitch":"https://twitch.tv/a"}` {
		t.Errorf("replyTo = %s", got)
	}
}

func TestRemovingThePinLeavesNoTarget(t *testing.T) {
	a := Source{"a", "https://twitch.tv/a", "twitch"}
	b := Source{"b", "https://twitch.tv/b", "twitch"}
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
		{"a", "https://twitch.tv/a", "twitch"},
		{"b", "https://twitch.tv/b", "twitch"},
		{"y", "https://youtube.com/@y", "youtube"},
	}
	p := pinsOver(&list)
	p.Restore(map[string]any{"twitch": "https://twitch.tv/b", "youtube": "https://youtube.com/@gone", "other": "x"})
	if got := replyTo(t, p); got != `{"twitch":"https://twitch.tv/b"}` {
		t.Errorf("replyTo = %s", got)
	}
	p.Restore("not an object") // ignored
	p.Added(Source{"z", "x", "kick"})
	if got := replyTo(t, p); got != `{"twitch":"https://twitch.tv/b"}` {
		t.Errorf("after junk: replyTo = %s", got)
	}
}
