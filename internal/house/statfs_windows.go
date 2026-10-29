//go:build windows

package house

import "errors"

// statfs is not available on Windows; disk-free reporting is simply omitted.
func statfs(path string, st *syscallStatfs) error {
	return errors.New("statfs is not supported on Windows")
}

type syscallStatfs struct {
	Bsize  uint64
	Bavail uint64
}
