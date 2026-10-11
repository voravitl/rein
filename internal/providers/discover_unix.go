//go:build !windows

package providers

import (
	"os/exec"
	"syscall"
)

// setGroup starts the child as leader of its own process group, so killGroup reaches its descendants too.
func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killGroup SIGKILLs the child's whole process group (pgid == pid because of setGroup). Safe to call repeatedly, and after the
// leader exited, to reap descendants that outlived it.
// ponytail: once the leader is reaped its pid could in theory be recycled into an unrelated group before this runs; the window
// is microseconds, and pids <= 1 are refused outright.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.Process.Pid <= 1 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
