//go:build windows

package providers

import "os/exec"

func setGroup(cmd *exec.Cmd) {}

// killGroup kills the child only.
// ponytail: Windows has no process-group kill here, so a descendant the harness itself spawned can outlive discovery; a job
// object would close that. Windows CI is excluded for now.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
