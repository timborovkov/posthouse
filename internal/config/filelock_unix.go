//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockConfigFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockConfigFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
