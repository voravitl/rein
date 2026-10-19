//go:build !windows

package e2e

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup places cmd in its own process group and makes context
// cancellation terminate the whole group, so a deadline cannot be defeated by a
// child process that keeps the command's pipes open.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
