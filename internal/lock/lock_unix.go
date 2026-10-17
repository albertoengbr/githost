//go:build !windows

package lock

import (
	"os"

	"golang.org/x/sys/unix"
)

func tryLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func tryUnlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
