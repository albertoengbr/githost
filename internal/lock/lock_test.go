package lock

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "githost.lock")

	l1, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// A second acquisition of the same lock must fail while held.
	if l2, err := Acquire(path); err == nil {
		l2.Release()
		t.Fatal("second Acquire succeeded while the lock was held")
	} else if !strings.Contains(err.Error(), "another GitHost process") {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := l1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// After release the lock can be taken again.
	l3, err := Acquire(path)
	if err != nil {
		t.Fatalf("re-Acquire after Release: %v", err)
	}
	l3.Release()

	// Double release is a no-op.
	if err := l3.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestLockDirectoryCreated(t *testing.T) {
	// The lock file is created inside the data directory, which the
	// caller (app) creates first; here the directory exists.
	path := filepath.Join(t.TempDir(), "sub", "githost.lock")
	if _, err := Acquire(path); err == nil {
		t.Fatal("Acquire must fail when the directory is missing")
	}
}
