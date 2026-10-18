package main

import (
	"os"
	"testing"
)

func TestCmdOrca_Validation(t *testing.T) {
	if code := cmdOrca([]string{}); code != 2 {
		t.Errorf("expected exit 2 on empty args, got %d", code)
	}

	if code := cmdOrca([]string{"unknown"}); code != 2 {
		t.Errorf("expected exit 2 on unknown subcommand, got %d", code)
	}

	if code := cmdOrca([]string{"launch"}); code != 2 {
		t.Errorf("expected exit 2 on missing launch args, got %d", code)
	}

	// Unset ORCA_COORD to test wait/drain flag validation
	origCoord := os.Getenv("ORCA_COORD")
	os.Unsetenv("ORCA_COORD")
	defer func() {
		if origCoord != "" {
			os.Setenv("ORCA_COORD", origCoord)
		}
	}()

	if code := cmdOrca([]string{"wait"}); code != 2 {
		t.Errorf("expected exit 2 on wait without coord, got %d", code)
	}

	if code := cmdOrca([]string{"drain"}); code != 2 {
		t.Errorf("expected exit 2 on drain without coord, got %d", code)
	}

	if code := cmdOrca([]string{"close"}); code != 2 {
		t.Errorf("expected exit 2 on close without worktree, got %d", code)
	}
}
