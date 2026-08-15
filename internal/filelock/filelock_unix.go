//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package filelock

import (
	"os"

	"golang.org/x/sys/unix"
)

func lock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
