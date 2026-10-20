package guard

import (
	"testing"
)

func TestZZPanicSubshell(t *testing.T) {
	e, _ := routedEnv(t)
	// and-list with RHS a subshell (not CallExpr) -> type assertion panic?
	cmd := "rein spec check /tmp/x side && (orca orchestration worker-start --worktree path:" + e.side + ")"
	out := e.bash(cmd)
	t.Logf("out: %q", out)
}
