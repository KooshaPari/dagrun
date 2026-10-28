//go:build windows

package run

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
