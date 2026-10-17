// Package lock provides the exclusive instance lock that coordinates
// `serve` with `backup` and `restore`.
//
// The lock uses OS primitives (flock on POSIX, LockFileEx on Windows):
// the kernel releases it automatically when the process dies, so a
// crashed instance never leaves a stale lock behind, and a running server
// makes backup/restore fail fast with a clear message instead of
// snapshotting a mutating instance.
package lock

import (
	"fmt"
	"os"
)

// Lock is a held instance lock.
type Lock struct {
	f *os.File
}

// Acquire takes the exclusive, non-blocking lock at path, creating the
// lock file when it does not exist. It returns an error immediately when
// another process holds the lock.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("another GitHost process holds %s (stop it and retry)", path)
	}
	return &Lock{f: f}, nil
}

// Release frees the lock. The lock file itself is kept: with OS locks the
// file is just a rendezvous point and removing it races waiters.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := tryUnlock(l.f)
	closeErr := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	return closeErr
}
