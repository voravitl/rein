package main

import (
	"slices"
	"testing"

	"github.com/voravitl/rein/internal/providers"
)

// A print-mode worker has nobody to answer an edit prompt, so the compiled
// argv must carry the worktree-scoped edit mode, and the approved-argv parser
// must accept exactly that argv.
func TestBuildDAGLaunchArgvGrantsWorktreeEdits(t *testing.T) {
	for agent, want := range map[string][]string{
		"claude": {"--permission-mode", "acceptEdits"},
		"codex":  {"--sandbox", "workspace-write"},
		"agy":    {"--mode", "accept-edits"},
	} {
		argv, err := buildDAGLaunchArgv(providers.Provider{Agent: agent, Model: "m"}, "approved task")
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		i := slices.Index(argv, want[0])
		if i < 0 || i+1 >= len(argv) || argv[i+1] != want[1] {
			t.Fatalf("%s argv lacks %v: %q", agent, want, argv)
		}
		slot, err := graphPromptSlot(argv)
		if err != nil || argv[slot] != "approved task" {
			t.Fatalf("%s compiled argv refused by graphPromptSlot: slot=%d err=%v argv=%q", agent, slot, err, argv)
		}
	}
}

// agy 1.4.10 reads the argument right after -p/--print as the prompt, so the
// compiled argv must end with --print followed by the prompt and carry no "--".
func TestBuildDAGLaunchArgvAgyPromptFollowsPrint(t *testing.T) {
	prompt := "-starts with a dash\nTASK: x"
	argv, err := buildDAGLaunchArgv(providers.Provider{Agent: "antigravity", Model: "m", Effort: "low"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	n := len(argv)
	if n < 2 || argv[n-2] != "--print" || argv[n-1] != prompt || slices.Contains(argv, "--") || slices.Contains(argv, "-p") {
		t.Fatalf("agy argv = %q", argv)
	}
	if slot, err := graphPromptSlot(argv); err != nil || slot != n-1 {
		t.Fatalf("graphPromptSlot(%q) = %d, %v", argv, slot, err)
	}
}
