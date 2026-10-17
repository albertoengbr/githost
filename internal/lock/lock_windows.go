//go:build windows

package lock

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock locks the first byte of the lock file exclusively and
// non-blocking via LockFileEx — the standard Windows advisory lock.
func tryLock(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1, 0, // lock one byte at offset 0
		&overlapped,
	)
	if err != nil {
		return err
	}
	return nil
}

func tryUnlock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		1, 0,
		&overlapped,
	)
}
