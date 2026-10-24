package guard

// Phase C slice C5 (O6 — hook/policy conflict fails closed).
//
// The OMC integration contract says a hook or policy conflict must fail
// closed with a reason, never be "resolved" by disabling a guard
// (docs/OMC_INTEGRATION_CONTRACT.th.md §4: never set OMC_GIT_GUARDRAILS=0;
// agents/orca-swarm.md:42). rein enforces that on its own side: a worker
// cannot edit, move or delete the installed guard's hook files, and the
// coordinator cannot either, because the hook files are outside the
// coordinator_writable allowlist and inside uncontracted worktrees.
//
// What this test pins:
//  1. a worker inside its own worktree cannot touch the hook files
//  2. rm/mv/find -delete over a hook file or its directory is refused
//  3. the coordinator is refused too (not only workers)
//  4. the refusal names the guard, so the reason is actionable
//  5. control: an ordinary file next to the hook is still writable, so the
//     guard is a boundary and not a blanket denial
//
// Test-only: no production path changes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func hookEnv(t *testing.T) (*coordEnv, string) {
	t.Helper()
	e := newCoordEnv(t)
	// A contracted worker worktree with the guard's hook files installed in it.
	wt := filepath.Join(contract.Real(filepath.Dir(e.root)), "wts", "task-h")
	linkWorktree(t, e.root, wt, "task-h")
	writeFile(t, filepath.Join(wt, "src", "w.go"), "x\n")
	writeFile(t, filepath.Join(wt, ".codex", "hooks.json"), `{"hooks":{}}`)
	writeFile(t, filepath.Join(wt, ".claude", "settings.local.json"), `{}`)
	writeFile(t, filepath.Join(wt, ".zcode", "config.json"), `{"hooks":{"enabled":true,"events":{}}}`)
	if _, err := contract.New("task-h", 1, filepath.Join(e.outside, "run"), "src/**", "", "S1", filepath.Dir(wt), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	e.contract = filepath.Join(contract.Real(filepath.Dir(e.root)), "contracts")
	t.Setenv("PIPELINE_CONTRACTS", e.contract)
	return e, wt
}

func TestCoordAndWorkerCannotDisableGuard(t *testing.T) {
	e, wt := hookEnv(t)

	// 1. Worker inside its own worktree: writing or removing a hook file is
	// refused even though the file is untracked and inside the worktree.
	for _, cmd := range []string{
		"echo '{}' > .codex/hooks.json",
		"rm .codex/hooks.json",
		"rm -rf .codex",
		"mv .codex/hooks.json /tmp/stolen.json",
		"echo '{}' > .claude/settings.local.json",
		`echo '{"hooks":{"enabled":false}}' > .zcode/config.json`, // ZCode's off switch
		"rm -rf .zcode",
		"find .codex -delete",
	} {
		e.expect(true, e.bash(cmd, map[string]any{"cwd": wt}), "worker: "+cmd)
	}
	// The deny reason names the guard, so the message is actionable.
	out := e.bash("rm -rf .codex", map[string]any{"cwd": wt})
	if !strings.Contains(out, "rein guard") {
		t.Errorf("deny reason does not name the guard: %q", out)
	}

	// 2. The coordinator is refused as well: the hook files are outside
	// coordinator_writable, so disabling the guard is not a coordinator move.
	e.expect(true, e.bash("echo '{}' > "+filepath.Join(wt, ".codex", "hooks.json")), "coordinator writes a worker hook file")
	e.expect(true, e.tool("Write", map[string]any{"file_path": filepath.Join(wt, ".claude", "settings.local.json")}), "coordinator Write on a worker hook file")
	e.expect(true, e.bash("rm -rf "+filepath.Join(wt, ".claude")), "coordinator removes the hook directory")

	// 3. Control: an ordinary file next to the hook is still writable, so the
	// guard is a boundary rather than a blanket denial.
	writeFile(t, filepath.Join(wt, "src", "w2.go"), "x\n")
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(wt, "src", "w2.go")}, map[string]any{"cwd": wt}), "ordinary worker file still editable")
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.root, "docs", "note.md")}), "coordinator docs still writable")

	// 4. The hook files are still on disk: nothing above was applied.
	for _, f := range []string{".codex/hooks.json", ".claude/settings.local.json", ".zcode/config.json"} {
		if _, err := os.Stat(filepath.Join(wt, filepath.FromSlash(f))); err != nil {
			t.Errorf("guard file %s was removed: %v", f, err)
		}
	}
}
