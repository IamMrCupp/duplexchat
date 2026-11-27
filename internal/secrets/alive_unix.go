//go:build !windows

package secrets

import "syscall"

// Alive reports whether process pid exists, as process.kill(pid, 0) does:
// a process we may not signal still counts.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
