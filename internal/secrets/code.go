package secrets

import (
	"errors"
	"syscall"
)

// Code is the error code Node would report for err ("EACCES", "ENOENT", …),
// for the messages that quote one; "error" when there's none.
func Code(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return "error"
	}
	if c := errnoCode(errno); c != "" {
		return c
	}
	return "error"
}
