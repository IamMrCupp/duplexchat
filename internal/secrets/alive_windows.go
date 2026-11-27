package secrets

import "syscall"

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	errorAccessDenied              = syscall.Errno(5)
)

// Alive reports whether process pid is running, as libuv's uv_kill(pid, 0)
// does on Windows: it exists and hasn't exited, or it exists and we may not
// look at it. Go's os.FindProcess plus Signal(0) can't answer this here.
func Alive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return err == errorAccessDenied
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
