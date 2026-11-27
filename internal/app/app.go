// Package app ties the source list, the feed and the chat readers together:
// what the panel's buttons do, and what a reader's events turn into
// (multichat server.js at 29a9d2c: addSource, removeSource, reconnectSource,
// startAdapter, makeHandlers, testMessages and /api/clear).
package app

import (
	"strconv"
	"sync"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/sources"
	"github.com/IamMrCupp/duplexchat/internal/targets"
)

// Adapter is a running chat reader.
type Adapter interface {
	Start()
	Stop()
}

// Targeted is a reader that knows where its chat is: the Twitch login it
// joined, the YouTube broadcast and its owner. Reply targets read it.
type Targeted interface {
	Target() targets.Reader
}

// Handlers is what a reader reports through.
type Handlers struct {
	Message      func(fields []Field) // the message's own fields; the app adds the rest
	Remove       func(id string)
	RemoveAuthor func(key string)
	Clear        func()
	Status       func(state, detail string)
	Meta         func(channel string)
}

// Factory starts nothing; it builds a reader for a source. It returns the
// channel it resolved ("" to keep the platform name), or an error the
// source shows as its status.
type Factory func(e sources.Entry, h Handlers) (a Adapter, channel string, err error)

// App is the running state behind the panel.
type App struct {
	Store   *sources.Store
	Hub     *hub.Hub
	Readers map[string]Factory // by platform; a platform without one stays "connecting"
	Now     func() time.Time
	Random  func() string // the random tail of a test message's ID

	mu       sync.Mutex
	adapters map[string]Adapter
	testN    int
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) status() { a.Hub.Status(a.Store.Status()) }

// start builds and starts the reader for e, if its platform has one.
func (a *App) start(e sources.Entry) {
	f := a.Readers[e.Platform]
	if f == nil {
		return
	}
	ad, channel, err := f(e, a.handlers(e.ID, e.Platform))
	if err != nil {
		a.Store.SetStatus(e.ID, "error", err.Error())
		return
	}
	if channel != "" {
		a.Store.SetChannel(e.ID, channel)
	}
	a.mu.Lock()
	if a.adapters == nil {
		a.adapters = map[string]Adapter{}
	}
	a.adapters[e.ID] = ad
	a.mu.Unlock()
	ad.Start()
}

func (a *App) stop(id string) {
	a.mu.Lock()
	ad := a.adapters[id]
	delete(a.adapters, id)
	a.mu.Unlock()
	if ad != nil {
		ad.Stop()
	}
}

// readerInfo is what source id's reader knows, if it's running and says.
func (a *App) readerInfo(id string) targets.Reader {
	a.mu.Lock()
	ad := a.adapters[id]
	a.mu.Unlock()
	if t, ok := ad.(Targeted); ok {
		return t.Target()
	}
	return targets.Reader{}
}

// Targets is where replies go, per platform (lib/targets.js get()).
func (a *App) Targets() map[string]targets.Target { return a.Store.Targets(a.readerInfo) }

// SetTarget is an explicit pick from the dock. On a change the choice is
// saved and the pages are told, as multichat's onChange does.
func (a *App) SetTarget(platform, sourceID string) bool {
	if !a.Store.SetTarget(platform, sourceID) {
		return false
	}
	a.Store.Save()
	a.status()
	return true
}

// Start runs every saved source (startup).
func (a *App) Start() {
	a.Store.StartSources()
	for _, e := range a.Store.List() {
		a.start(e)
	}
	a.status()
}

// AddSource is the panel's add: saved, started, announced.
func (a *App) AddSource(link string) (sources.Entry, error) {
	e, err := a.Store.Add(link)
	if err != nil {
		return e, err
	}
	a.start(e)
	a.status()
	return e, nil
}

// RemoveSource stops a source and drops it and its messages. False when the
// ID is unknown.
func (a *App) RemoveSource(id string) bool {
	a.stop(id)
	if !a.Store.Remove(id) {
		return false
	}
	a.Hub.ClearSource(id)
	a.status()
	return true
}

// ReconnectSource restarts a source's reader. False when the ID is unknown.
func (a *App) ReconnectSource(id string) bool {
	e, ok := a.Store.Get(id)
	if !ok {
		return false
	}
	a.stop(id)
	a.Store.SetStatus(id, "connecting", "")
	e.State, e.Detail = "connecting", ""
	a.start(e)
	a.status()
	return true
}

// ClearAll is the panel's clear button.
func (a *App) ClearAll() { a.Hub.ClearAll() }

// handlers turn a reader's events into feed frames and status.
func (a *App) handlers(id, platform string) Handlers {
	return Handlers{
		Message: func(fields []Field) {
			e, ok := a.Store.Get(id)
			if !ok {
				return
			}
			a.Hub.Publish(BuildMsg(fields, e, a.now()))
		},
		Remove:       a.Hub.Remove,
		RemoveAuthor: func(key string) { a.Hub.RemoveAuthor(id, key) },
		Clear:        func() { a.Hub.ClearSource(id) },
		Status: func(state, detail string) {
			if a.Store.SetStatus(id, state, detail) {
				a.status()
			}
		},
		Meta: func(channel string) {
			if channel != "" && a.Store.SetChannel(id, channel) {
				a.status()
			}
		},
	}
}

func (a *App) nextTestID() string {
	a.mu.Lock()
	a.testN++
	n := a.testN
	a.mu.Unlock()
	tail := strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	if a.Random != nil {
		tail = a.Random()
	}
	return "test:" + strconv.Itoa(n) + ":" + tail
}
