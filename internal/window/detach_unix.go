//go:build !windows

package window

import "syscall"

// detached puts the browser in its own process group, so Ctrl-C on the
// server doesn't close the window (Node's detached: true).
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
