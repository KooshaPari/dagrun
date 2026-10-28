//go:build !windows

package verify

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the check command in its own process group so that a
// timeout kills descendants too, not just the shell.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// negative pid signals the whole group
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
