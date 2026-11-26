package applog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLine(t *testing.T) {
	at := time.Date(2026, 11, 25, 12, 34, 56, 789_000_000, time.FixedZone("x", -8*3600))
	got := Line(at, "listen", errors.New("address in use"))
	if got != "[2026-11-25T20:34:56.789Z] listen: address in use\n" {
		t.Errorf("got %q", got)
	}
}

func TestWriteAppendsAndEchoes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "error.log")
	var echo bytes.Buffer
	l := &Log{path: p, echo: &echo, now: func() time.Time { return time.Unix(0, 0) }}
	l.Write("http", "one")
	l.Write("panic", errors.New("two"))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "[1970-01-01T00:00:00.000Z] http: one\n[1970-01-01T00:00:00.000Z] panic: two\n"
	if string(b) != want {
		t.Errorf("file = %q", b)
	}
	if strings.Count(echo.String(), "\n") != 2 || !strings.Contains(echo.String(), "panic: two") {
		t.Errorf("echo = %q", echo.String())
	}
}
