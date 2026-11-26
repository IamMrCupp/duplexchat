// Package applog is error.log: one line per failure, appended, and the same
// line on stderr. The app keeps running after anything it logs (multichat
// server.js at 29a9d2c: logErr).
package applog

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Log appends to a file and echoes to a writer.
type Log struct {
	mu   sync.Mutex
	path string
	echo io.Writer
	now  func() time.Time
}

// New logs to path and echoes to stderr.
func New(path string) *Log { return NewTo(path, os.Stderr) }

// NewTo logs to path and echoes to echo (nil for no echo).
func NewTo(path string, echo io.Writer) *Log {
	return &Log{path: path, echo: echo, now: time.Now}
}

// Line formats an entry: [2026-11-25T12:34:56.789Z] kind: what
func Line(at time.Time, kind string, what any) string {
	return fmt.Sprintf("[%s] %s: %v\n", at.UTC().Format("2006-01-02T15:04:05.000Z"), kind, what)
}

// Write records what under kind. Errors writing the log itself are ignored.
func (l *Log) Write(kind string, what any) {
	line := Line(l.now(), kind, what)
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.WriteString(line)
		f.Close()
	}
	if l.echo != nil {
		fmt.Fprintln(l.echo, strings.TrimSpace(line))
	}
}
