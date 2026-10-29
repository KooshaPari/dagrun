//go:build !windows

package house

import "syscall"

// statfs fills a statfs_t for the filesystem holding path.
func statfs(path string, st *syscallStatfs) error {
	return syscall.Statfs(path, st)
}

// syscallStatfs mirrors syscall.Statfs_t so the windows build can stub it.
type syscallStatfs = syscall.Statfs_t
