//go:build windows

package e2e

import "os/exec"

// configureProcessGroup is a no-op on Windows; exec.CommandContext cancels the
// direct process there.
func configureProcessGroup(cmd *exec.Cmd) {}
