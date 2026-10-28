//go:build windows

package store

import (
	"errors"
	"os"
)

// Windows has no flock(2). Rather than silently degrade into an unsafe
// one-writer-per-run assumption, dagrun refuses to run there. macOS and Linux
// are the supported targets; a LockFileEx implementation is a small follow-up.
func lockExclusive(f *os.File) error {
	return errors.New("dagrun does not support Windows yet: no safe cross-process lock")
}

func unlock(f *os.File) error { return nil }
