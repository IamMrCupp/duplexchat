package secrets

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Lock is the outcome of AcquireSendLock. One process per folder may send:
// the server falls back to the next free port when its own is taken, so a
// second copy started by accident would otherwise share secrets.json with
// the first and race it for single-use refresh tokens. The second copy still
// reads chat; it just won't send.
type Lock struct {
	Held   bool
	File   string
	Owner  int    // the other copy's PID when it holds the lock, else 0
	Reason string // why sending is off, when not held

	release func()
}

// Release gives the lock up. Releasing a lock that isn't held does nothing.
func (l Lock) Release() {
	if l.release != nil {
		l.release()
	}
}

// AcquireSendLock takes dir/sending.lock for pid. isAlive is nil for the
// real check.
func AcquireSendLock(dir string, pid int, isAlive func(int) bool) Lock {
	if isAlive == nil {
		isAlive = Alive
	}
	file := filepath.Join(dir, "sending.lock")
	mine := strconv.Itoa(pid)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.WriteString(mine)
			f.Close()
			return Lock{Held: true, File: file, release: func() {
				if b, err := os.ReadFile(file); err == nil && string(b) == mine {
					os.Remove(file)
				}
			}}
		}
		if Code(err) != "EEXIST" {
			return Lock{File: file, Reason: fmt.Sprintf("Sending is off: couldn't create %s (%s).", filepath.Base(file), Code(err))}
		}
		owner := math.NaN()
		if b, err := os.ReadFile(file); err == nil {
			owner = jsNumber(string(b))
		}
		if owner == float64(pid) {
			return Lock{Held: true, File: file, release: func() { os.Remove(file) }}
		}
		if owner == math.Trunc(owner) && owner > 0 && owner <= math.MaxInt32 && isAlive(int(owner)) {
			return Lock{File: file, Owner: int(owner), Reason: fmt.Sprintf(
				"Sending is off in this copy: another copy of the server (process %d) is already running from this folder and owns sending. Use that one's dock, or stop it.", int(owner))}
		}
		os.Remove(file) // left behind by a copy that's gone
	}
	return Lock{File: file, Reason: "Sending is off: the sending lock is contested."}
}

// jsNumber is Number(s.trim()) for what a lock file can hold: decimal
// numbers, with "" as 0 and anything else NaN.
func jsNumber(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.ContainsAny(s, "_xXpP") || strings.EqualFold(strings.TrimLeft(s, "+-"), "inf") {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}
