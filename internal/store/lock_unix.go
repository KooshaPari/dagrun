//go:build !windows

package store

import (
	"os"
	"syscall"
)

// lockExclusive blocks until it holds an exclusive advisory lock on f.
// The kernel releases it if the process dies, so there is no stale-lock
// cleanup path anywhere in this codebase.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
