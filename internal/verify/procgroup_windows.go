//go:build windows

package verify

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
