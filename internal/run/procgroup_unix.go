//go:build !windows

package run

import (
	"os/exec"
	"syscall"
)

// setProcessGroup isolates the child so a timeout or a lost lease kills the
// whole subtree, not just the top process.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * 1e9 // give the group 10s to exit before SIGKILL
}
