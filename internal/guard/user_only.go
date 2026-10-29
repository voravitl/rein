package guard

import (
	"os"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"mvdan.cc/sh/v3/syntax"
)

// userOnlyShell reuses shell expansion and wrapper traversal without enabling
// Claude's coordinator lifecycle, write, routing or tool policy on other vendors.
func userOnlyShell(command, cwd string) string {
	x := &ctx{c: &contract.Contract{}, top: cwd, cwd: cwd, vars: map[string]string{},
		pipeRHS: map[*syntax.Stmt]bool{}, userOnly: true}
	if home, err := os.UserHomeDir(); err == nil {
		x.vars["HOME"] = home
	}
	if reason := x.script(command); strings.Contains(reason, "is for the user") {
		return reason
	}
	return ""
}
