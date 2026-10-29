//go:build !darwin && !linux

package supervisor

import "os"

// DAGTerminalRows is unknown on other platforms; the TUI does not clip.
func DAGTerminalRows(*os.File) int { return 0 }
