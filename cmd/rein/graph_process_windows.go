//go:build windows

package main

import (
	"errors"
	"os/exec"
)

func configureGraphProcess(*exec.Cmd) error {
	return errors.New("graph local process-group proof is unavailable on Windows")
}
func stopGraphProcess(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return errors.New("provider process was not started")
}
func graphGroupGone(int) error {
	return errors.New("graph local process-group proof is unavailable on Windows")
}
