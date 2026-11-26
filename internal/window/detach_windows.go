//go:build windows

package window

import "syscall"

// detached starts the browser outside the console (Node's detached: true).
func detached() *syscall.SysProcAttr {
	const detachedProcess, newProcessGroup = 0x00000008, 0x00000200
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup}
}
