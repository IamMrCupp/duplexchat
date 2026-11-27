package targets

// The rest of lib/targets.js: the dock's view of where a reply goes, and
// an explicit choice. Every target carries a key (source ID plus the channel
// or video it resolves to). The dock sends back the key it was showing, and
// a send whose key no longer matches is refused, so a reply can't land
// somewhere the page didn't show.

// Reader is what a running chat reader knows that the targets need. Zero
// values mean it doesn't know (yet).
type Reader struct {
	Channel        string // Twitch: the login it joined
	VideoID        string // YouTube: the broadcast it's reading
	OwnerChannelID string // YouTube: whose stream that is, when the watch page said
}

// Choice is one source the dock can pick.
type Choice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Target is one platform's reply target as the send API consumes it.
type Target struct {
	Pinned   bool
	SourceID string
	Choices  []Choice
	Channel  *string // Twitch: the reader's channel; YouTube: the source's channel
	VideoID  *string
	OwnerID  *string
	Label    string
	Key      string
}

// Source's Channel and Reader feed the view; pinning ignores them.

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func name(s Source) string {
	if s.Platform == "twitch" {
		c := s.Reader.Channel
		if c == "" {
			c = s.Channel
		}
		if c == "" {
			c = "?"
		}
		return "#" + c
	}
	if s.Channel != "" && s.Channel != "youtube" {
		return s.Channel
	}
	return s.URL
}

func describe(s Source, t *Target) {
	t.Label = name(s)
	if s.Platform == "twitch" {
		t.Channel = strOrNil(s.Reader.Channel)
		t.Key = s.ID + ":" + s.Reader.Channel
		return
	}
	t.VideoID = strOrNil(s.Reader.VideoID)
	t.Channel = strOrNil(s.Channel)
	t.OwnerID = strOrNil(s.Reader.OwnerChannelID)
	if s.Reader.VideoID != "" {
		t.Label += " · video " + s.Reader.VideoID
	} else {
		t.Label += " · not live yet"
	}
	t.Key = s.ID + ":" + s.Reader.VideoID
}

// Set is an explicit choice from the dock: false for an unknown platform or
// a source that isn't one of that platform's.
func (p *Pins) Set(platform, sourceID string) bool {
	if !known(platform) {
		return false
	}
	for _, s := range p.of(platform) {
		if s.ID == sourceID {
			p.pin[platform] = s.ID
			return true
		}
	}
	return false
}

// Snapshot is the pinned source ID per platform.
func (p *Pins) Snapshot() map[string]string {
	out := map[string]string{}
	for k, v := range p.pin {
		out[k] = v
	}
	return out
}

// View is get(): per platform, the choices and, when one is pinned, where a
// reply goes. list is the source list with what each reader knows.
func View(list []Source, pins map[string]string) map[string]Target {
	out := map[string]Target{}
	for _, platform := range Platforms {
		t := Target{Choices: []Choice{}}
		var pinned *Source
		for i, s := range list {
			if s.Platform != platform {
				continue
			}
			t.Choices = append(t.Choices, Choice{s.ID, name(s)})
			if pins[platform] != "" && s.ID == pins[platform] && pinned == nil {
				pinned = &list[i]
			}
		}
		if pinned != nil {
			t.Pinned, t.SourceID = true, pinned.ID
			describe(*pinned, &t)
		}
		out[platform] = t
	}
	return out
}
