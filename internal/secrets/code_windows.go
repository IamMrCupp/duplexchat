package secrets

import "syscall"

// Windows error numbers mapped as libuv maps them (uv_translate_sys_error).
var codes = map[syscall.Errno]string{
	2:    "ENOENT", // ERROR_FILE_NOT_FOUND
	3:    "ENOENT", // ERROR_PATH_NOT_FOUND
	4:    "EMFILE", // ERROR_TOO_MANY_OPEN_FILES
	5:    "EPERM",  // ERROR_ACCESS_DENIED
	32:   "EBUSY",  // ERROR_SHARING_VIOLATION
	33:   "EBUSY",  // ERROR_LOCK_VIOLATION
	80:   "EEXIST", // ERROR_FILE_EXISTS
	112:  "ENOSPC", // ERROR_DISK_FULL
	123:  "ENOENT", // ERROR_INVALID_NAME
	183:  "EEXIST", // ERROR_ALREADY_EXISTS
	206:  "ENAMETOOLONG",
	267:  "ENOTDIR", // ERROR_DIRECTORY
	1920: "EACCES",  // ERROR_CANT_ACCESS_FILE
}

func errnoCode(e syscall.Errno) string {
	if c, ok := codes[e]; ok {
		return c
	}
	// Go's own POSIX-style errnos (from the os package's emulation)
	switch e {
	case syscall.EACCES:
		return "EACCES"
	case syscall.EEXIST:
		return "EEXIST"
	case syscall.ENOENT:
		return "ENOENT"
	}
	return ""
}
