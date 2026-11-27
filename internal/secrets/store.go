// Package secrets is where sign-in tokens and client secrets live:
// secrets.json beside config.json, owner-only (0600), read and written
// exactly as multichat does (lib/tokens.js at 29a9d2c). Nothing in here is
// ever logged or sent to a page: callers hand out names and booleans, not
// values. On Windows the mode bits don't apply; the file inherits the
// folder's ACL.
//
// Nothing is cached. Every read goes to the file and every write is a
// read-merge-write of one section, so two processes on the same folder see
// each other's changes instead of overwriting them from a stale copy. That
// matters most for Twitch, whose refresh tokens are single-use.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

const posix = runtime.GOOS != "windows"

// Store is one secrets file. It's safe for concurrent use: each Get, and
// each Set's read-merge-write, happens whole, as on Node's single thread.
type Store struct {
	mu   sync.Mutex
	file string
	log  func(kind string, what any)
	now  func() time.Time
	pid  int

	readError  string // the file is there and can't be read
	brokenNote string // the file was unparseable and has been set aside
}

// Options are the store's seams. log is only ever given fixed text and an
// error code: never file contents, and never a parser message (those quote
// the offending text).
type Options struct {
	Log func(kind string, what any)
	Now func() time.Time
}

// ErrUnreadable is Set's refusal while the file can't be read: it may be
// full of good tokens.
var ErrUnreadable = errors.New("secrets file is unreadable")

// New returns the store for file.
func New(file string, o Options) *Store {
	s := &Store{file: file, log: o.Log, now: o.Now, pid: os.Getpid()}
	if s.log == nil {
		s.log = func(string, any) {}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Problem is what the owner should know about the file, for /setup, or "".
func (s *Store) Problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var parts []string
	for _, p := range []string{s.readError, s.brokenNote} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

func (s *Store) setAside() {
	stamp := s.now().UTC().Format("20060102T150405Z")
	aside := s.file + ".broken-" + stamp
	base := filepath.Base(s.file)
	if err := os.Rename(s.file, aside); err != nil {
		s.brokenNote = fmt.Sprintf("%s couldn't be parsed and couldn't be moved aside (%s).", base, Code(err))
	} else {
		if posix {
			os.Chmod(aside, 0o600)
		}
		s.brokenNote = fmt.Sprintf("%s couldn't be parsed, so it was moved to %s and sign-ins start fresh.", base, filepath.Base(aside))
	}
	s.log("secrets", s.brokenNote)
}

func (s *Store) read() *jsjson.Object {
	raw, err := func() ([]byte, error) {
		st, err := os.Stat(s.file)
		if err != nil {
			return nil, err
		}
		// someone (an editor, a restore from backup) loosened it: tighten it first
		if posix && st.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(s.file, 0o600); err != nil {
				return nil, err
			}
		}
		return os.ReadFile(s.file)
	}()
	if err != nil {
		if Code(err) == "ENOENT" {
			s.readError = ""
			return jsjson.NewObject()
		}
		msg := fmt.Sprintf("%s can't be read (%s). Sending is off until that's fixed.", filepath.Base(s.file), Code(err))
		if msg != s.readError {
			s.log("secrets", msg)
		}
		s.readError = msg
		return jsjson.NewObject()
	}
	s.readError = ""
	parsed, err := jsjson.Parse(raw) // the error is deliberately unused
	o, ok := parsed.(*jsjson.Object)
	if err != nil || !ok {
		s.setAside()
		return jsjson.NewObject()
	}
	return o
}

// Get returns a copy of one section, or nil when it's missing or isn't an
// object. An array reads as an object keyed by index, as { ...array } does.
func (s *Store) Get(section string) *jsjson.Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.read().Get(section)
	switch t := v.(type) {
	case *jsjson.Object:
		return t.Copy()
	case []any:
		o := jsjson.NewObject()
		for i, x := range t {
			o.Set(fmt.Sprint(i), x)
		}
		return o
	}
	return nil
}

// Set replaces one section; nil removes it. Only that section changes. The
// file is written to a temporary file created 0600 and renamed over the real
// one, so the secrets never sit in a wider-mode file and a crash mid-write
// can't leave half a token behind.
func (s *Store) Set(section string, value *jsjson.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.read()
	if s.readError != "" {
		return ErrUnreadable
	}
	if value == nil {
		if !data.Has(section) {
			return nil // nothing to forget: don't create the file
		}
		data.Delete(section)
	} else {
		data.Set(section, value.Copy())
	}
	tmp := filepath.Join(filepath.Dir(s.file), fmt.Sprintf(".%s.%d.tmp", filepath.Base(s.file), s.pid))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(jsjson.Stringify(data, "  "))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if posix {
		if err := os.Chmod(tmp, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, s.file)
}
