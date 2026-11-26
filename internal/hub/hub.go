// Package hub is the server-sent events feed every page listens to: chat
// messages, deletions, source status and viewer counts, plus the last 100
// messages for pages that connect late (multichat server.js at 29a9d2c: the
// SSE hub, pushHistory and makeHandlers' history edits).
//
// One goroutine owns all state. Methods hand it work and wait for it to be
// done, and it never blocks: a client that can't keep up is dropped rather
// than slowing the feed for everyone else.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// HistoryCap is how many messages a newly connected page receives.
const HistoryCap = 100

// Count is one platform's viewer count as the pages expect it.
type Count struct {
	State string `json:"state"`
	Count *int   `json:"count"`
	Note  string `json:"note"`
}

// ViewerCounts is the viewers payload, Twitch first.
type ViewerCounts struct {
	Twitch  Count `json:"twitch"`
	YouTube Count `json:"youtube"`
}

// NoViewers is what pages get while nothing is being counted.
var NoViewers = ViewerCounts{Twitch: Count{State: "none"}, YouTube: Count{State: "none"}}

// Msg is a chat message ready to send: its encoded JSON, plus the fields the
// history is edited by. AuthorKey is nil when the message has no author key.
type Msg struct {
	ID        string
	Source    string
	AuthorKey *string
	JSON      []byte
}

// Options are the hub's settings; zero values mean multichat's.
type Options struct {
	Heartbeat     time.Duration // 25 s
	Buffer        int           // frames queued per client before it's dropped; 256
	OnFirstClient func()        // run (in its own goroutine) when the first page connects
}

type client struct {
	out  chan []byte
	gone chan struct{}
}

// Hub is the feed. Create it with New and start it with Run.
type Hub struct {
	opts    Options
	cmds    chan func()
	history []Msg
	sources any
	viewers any
	clients map[*client]struct{}
	n       atomic.Int32
}

// New returns a hub with no history, no sources and no viewer counts.
func New(opts Options) *Hub {
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 25 * time.Second
	}
	if opts.Buffer <= 0 {
		opts.Buffer = 256
	}
	return &Hub{
		opts:    opts,
		cmds:    make(chan func()),
		sources: []any{},
		viewers: NoViewers,
		clients: map[*client]struct{}{},
	}
}

// Run owns the hub's state until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	tick := time.NewTicker(h.opts.Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				h.drop(c)
			}
			return
		case f := <-h.cmds:
			f()
		case <-tick.C:
			h.send([]byte(":hb\n\n"))
		}
	}
}

// do runs f on the hub's goroutine and waits for it.
func (h *Hub) do(f func()) {
	done := make(chan struct{})
	h.cmds <- func() { f(); close(done) }
	<-done
}

func frame(v any) []byte {
	var buf bytes.Buffer
	buf.WriteString("data: ")
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // only for values that can't be JSON, which is a bug
	}
	buf.Truncate(buf.Len() - 1) // Encode's newline
	buf.WriteString("\n\n")
	return buf.Bytes()
}

// send queues a frame for every client, dropping any whose queue is full.
func (h *Hub) send(f []byte) {
	for c := range h.clients {
		select {
		case c.out <- f:
		default:
			h.drop(c)
		}
	}
}

func (h *Hub) drop(c *client) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	h.n.Add(-1)
	close(c.gone)
}

func (h *Hub) keep(pred func(Msg) bool) {
	kept := h.history[:0]
	for _, m := range h.history {
		if pred(m) {
			kept = append(kept, m)
		}
	}
	for i := len(kept); i < len(h.history); i++ {
		h.history[i] = Msg{}
	}
	h.history = kept
}

// Publish adds a message to the history and sends it.
func (h *Hub) Publish(m Msg) {
	h.do(func() {
		h.history = append(h.history, m)
		if over := len(h.history) - HistoryCap; over > 0 {
			h.history = append([]Msg(nil), h.history[over:]...)
		}
		h.send(append(append([]byte("data: "), m.JSON...), '\n', '\n'))
	})
}

// Remove deletes one message by ID.
func (h *Hub) Remove(id string) {
	h.do(func() {
		h.keep(func(m Msg) bool { return m.ID != id })
		h.send(frame(struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}{"remove", id}))
	})
}

// RemoveAuthor deletes one author's messages from one source.
func (h *Hub) RemoveAuthor(source, key string) {
	h.do(func() {
		h.keep(func(m Msg) bool { return !(m.Source == source && m.AuthorKey != nil && *m.AuthorKey == key) })
		h.send(frame(struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Key    string `json:"key"`
		}{"removeAuthor", source, key}))
	})
}

// ClearSource deletes everything from one source.
func (h *Hub) ClearSource(source string) {
	h.do(func() {
		h.keep(func(m Msg) bool { return m.Source != source })
		h.send(frame(struct {
			Type   string `json:"type"`
			Source string `json:"source"`
		}{"clear", source}))
	})
}

// ClearAll deletes everything (the panel's clear button).
func (h *Hub) ClearAll() {
	h.do(func() {
		h.history = nil
		h.send(frame(struct {
			Type string `json:"type"`
		}{"clear"}))
	})
}

// Status sends the source list and keeps it for pages that connect later.
func (h *Hub) Status(sources any) {
	h.do(func() {
		h.sources = sources
		h.send(frame(struct {
			Type    string `json:"type"`
			Sources any    `json:"sources"`
		}{"status", sources}))
	})
}

// Viewers sends viewer counts and keeps them for pages that connect later.
func (h *Hub) Viewers(v any) {
	h.do(func() {
		h.viewers = v
		h.send(frame(struct {
			Type    string `json:"type"`
			Viewers any    `json:"viewers"`
		}{"viewers", v}))
	})
}

// History returns a copy of the current history (for tests and /api/state).
func (h *Hub) History() []Msg {
	var out []Msg
	h.do(func() { out = append(out, h.history...) })
	return out
}

// Sources returns the last source list sent.
func (h *Hub) Sources() any {
	var out any
	h.do(func() { out = h.sources })
	return out
}

// CurrentViewers returns the last viewer counts sent.
func (h *Hub) CurrentViewers() any {
	var out any
	h.do(func() { out = h.viewers })
	return out
}

// Clients is how many pages are connected.
func (h *Hub) Clients() int { return int(h.n.Load()) }

func (h *Hub) hello() []byte {
	hist := make([]json.RawMessage, len(h.history))
	for i, m := range h.history {
		hist[i] = m.JSON
	}
	return frame(struct {
		Type    string            `json:"type"`
		History []json.RawMessage `json:"history"`
		Sources any               `json:"sources"`
		Viewers any               `json:"viewers"`
	}{"hello", hist, h.sources, h.viewers})
}

// ServeHTTP is GET /events.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	c := &client{out: make(chan []byte, h.opts.Buffer), gone: make(chan struct{})}
	var hello []byte
	h.do(func() {
		hello = h.hello()
		h.clients[c] = struct{}{}
		if h.n.Add(1) == 1 && h.opts.OnFirstClient != nil {
			go h.opts.OnFirstClient()
		}
	})
	defer func() {
		select {
		case <-c.gone:
		default:
			h.do(func() { h.drop(c) })
		}
	}()

	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !write([]byte(":ok\n\n")) || !write(hello) {
		return
	}
	for {
		select {
		case f := <-c.out:
			if !write(f) {
				return
			}
		case <-c.gone:
			return
		case <-r.Context().Done():
			return
		}
	}
}
