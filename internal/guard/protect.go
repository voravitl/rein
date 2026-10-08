package guard

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/glob"
)

// hookFiles are the files `rein hooks install` writes into a worktree. They are the guard itself, so a worker
// must not edit, move or delete them (the contract's deny list cannot name them: they differ per vendor).
var hookFiles = []string{".codex/hooks.json", ".agents/hooks.json", ".kiro/agents/rein.json",
	".opencode/plugins/rein/server.js", ".claude/settings.local.json"}

// protectedHook reports whether rel (slash form, relative to the worktree) is a hook file, anything under the
// opencode plugin dir, or a directory that currently holds a hook file (so rm -r / mv of the directory is caught).
func protectedHook(top, rel string) bool {
	if glob.Match(rel, append([]string{".opencode/plugins/rein/**"}, hookFiles...)) {
		return true
	}
	for _, f := range hookFiles {
		if strings.HasPrefix(f, rel+"/") {
			if _, err := os.Stat(filepath.Join(top, filepath.FromSlash(f))); err == nil {
				return true
			}
		}
	}
	return false
}
