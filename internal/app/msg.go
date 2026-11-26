package app

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/sources"
)

// Field is one key of a message, in order.
type Field struct {
	Key   string
	Value any
}

func encode(fields []Field) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		buf.Write(k)
		buf.WriteByte(':')
		if nested, ok := f.Value.([]Field); ok {
			buf.Write(encode(nested))
			continue
		}
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(f.Value); err != nil {
			panic(err)
		}
		buf.Truncate(buf.Len() - 1)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// BuildMsg is makeHandlers' onMessage: {type: 'msg', ...m, source, platform,
// channel, ts}. A field the reader set that the app also sets keeps its
// first position and takes the app's value, as an object spread does.
func BuildMsg(m []Field, e sources.Entry, at time.Time) hub.Msg {
	fields := append([]Field{{"type", "msg"}}, m...)
	for _, f := range []Field{{"source", e.ID}, {"platform", e.Platform}, {"channel", e.Channel}, {"ts", at.UnixMilli()}} {
		fields = set(fields, f)
	}
	return toMsg(fields)
}

func set(fields []Field, f Field) []Field {
	for i := range fields {
		if fields[i].Key == f.Key {
			fields[i].Value = f.Value
			return fields
		}
	}
	return append(fields, f)
}

// toMsg encodes fields and pulls out what the feed's history is edited by.
func toMsg(fields []Field) hub.Msg {
	var m hub.Msg
	for _, f := range fields {
		switch f.Key {
		case "id":
			m.ID, _ = f.Value.(string)
		case "source":
			m.Source, _ = f.Value.(string)
		case "author":
			if k, ok := authorKey(f.Value); ok {
				m.AuthorKey = &k
			}
		}
	}
	m.JSON = encode(fields)
	return m
}

// authorKey reads author.key when it's a string.
func authorKey(author any) (string, bool) {
	switch a := author.(type) {
	case Author:
		if a.Key != nil {
			return *a.Key, true
		}
	case *Author:
		if a != nil && a.Key != nil {
			return *a.Key, true
		}
	case []Field:
		for _, f := range a {
			if f.Key == "key" {
				k, ok := f.Value.(string)
				return k, ok
			}
		}
	}
	return "", false
}

// Author is a message author as the pages expect it.
type Author struct {
	Name  string   `json:"name"`
	Color *string  `json:"color"`
	Roles []string `json:"roles"`
	Key   *string  `json:"key"`
}

// Part is a piece of a message: text, or an emote image.
type Part struct {
	T   string `json:"t"`
	V   string `json:"v,omitempty"`
	URL string `json:"url,omitempty"`
	Alt string `json:"alt,omitempty"`
}

func str(s string) *string { return &s }

// Test is the panel's test button: five English messages that each say
// they're a test, so one that lands on stream explains itself (multichat
// server.js testMessages).
func (a *App) Test() {
	ts := a.now().UnixMilli()
	base := []Field{{"type", "msg"}, {"source", "test"}, {"ts", ts}}
	msg := func(rest ...Field) []Field {
		out := append(append([]Field{}, base...), Field{"id", a.nextTestID()})
		return append(out, rest...)
	}
	text := func(v string) []Part { return []Part{{T: "text", V: v}} }
	all := [][]Field{
		msg(Field{"platform", "twitch"}, Field{"channel", "test"}, Field{"kind", "chat"},
			Field{"author", Author{"ExampleViewer", str("#ff69b4"), []string{"sub"}, str("exampleviewer")}},
			Field{"parts", []Part{{T: "text", V: "Test message from Twitch "}, {T: "emote", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/default/dark/2.0", Alt: "Kappa"}}}),
		msg(Field{"platform", "youtube"}, Field{"channel", "test"}, Field{"kind", "chat"},
			Field{"author", Author{"Example Member", nil, []string{"member"}, str("UCtest")}},
			Field{"parts", text("Test message from YouTube 😀")}),
		msg(Field{"platform", "twitch"}, Field{"channel", "test"}, Field{"kind", "chat"},
			Field{"author", Author{"ExampleMod", str("#57aeff"), []string{"mod"}, str("examplemod")}},
			Field{"parts", text("Test message from a moderator")}),
		msg(Field{"platform", "youtube"}, Field{"channel", "test"}, Field{"kind", "super"},
			Field{"author", Author{"Example Supporter", nil, []string{}, str("UCrich")}},
			Field{"amount", "$5.00"}, Field{"colors", []Field{{"header", "#ffb300"}, {"body", "#ffca28"}}},
			Field{"parts", text("Test Super Chat — no money moved")}),
		msg(Field{"platform", "twitch"}, Field{"channel", "test"}, Field{"kind", "system"},
			Field{"author", []Field{{"name", "ExampleSub"}, {"roles", []string{}}, {"key", "examplesub"}}},
			Field{"parts", []Part{}}, Field{"system", "Test: ExampleSub subscribed with Prime. 3 months in a row."}),
	}
	for _, m := range all {
		a.Hub.Publish(toMsg(m))
	}
}

// Encode is a message's JSON with its fields in order.
func Encode(fields []Field) []byte { return encode(fields) }
