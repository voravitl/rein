package main

import (
	"fmt"
	"os"

	"github.com/voravitl/rein/internal/run"
)

// Fixtures may substitute only the terminal probe, never the agent-session check.
var ownerStdinTerminal = run.InteractiveStdin

func requireOwner(command string) bool {
	if kind, _ := run.AgentSession(); kind != "" {
		fmt.Fprintf(os.Stderr, "[rein] %s is for the user: refused inside %s; use your own interactive terminal\n", command, kind)
		return false
	}
	if !ownerStdinTerminal() {
		fmt.Fprintf(os.Stderr, "[rein] %s is for the user: standard input is not an interactive terminal\n", command)
		return false
	}
	return true
}
