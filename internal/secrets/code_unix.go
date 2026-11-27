//go:build !windows

package secrets

import "syscall"

var codes = map[syscall.Errno]string{
	syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENOENT: "ENOENT",
	syscall.EEXIST: "EEXIST", syscall.EISDIR: "EISDIR", syscall.ENOTDIR: "ENOTDIR",
	syscall.EROFS: "EROFS", syscall.ENOSPC: "ENOSPC", syscall.EBUSY: "EBUSY",
	syscall.EMFILE: "EMFILE", syscall.ENFILE: "ENFILE", syscall.ELOOP: "ELOOP",
	syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.EIO: "EIO", syscall.EXDEV: "EXDEV",
	syscall.EINVAL: "EINVAL", syscall.EAGAIN: "EAGAIN", syscall.EDQUOT: "EDQUOT",
}

func errnoCode(e syscall.Errno) string { return codes[e] }
