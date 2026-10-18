//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureGraphProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
func stopGraphProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return errors.New("provider process was not started")
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func graphGroupGone(pid int) error {
	err := syscall.Kill(-pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return errors.New("local process group is still present or its liveness is unknown")
}
