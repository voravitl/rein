package guard

import (
	"regexp"
	"strings"
)

// patchMarker opens every apply_patch body, whether it arrives as a tool call or inside a shell command.
const patchMarker = "*** Begin Patch"

// patchPaths lists every file an apply_patch body touches: the *** Add/Update/Delete File headers and the
// *** Move to destination. Paths are returned as written (relative to the tool's cwd). The result is nil when
// the text names no file, which decide treats as a write it cannot read, never as allowed.
func patchPaths(patch string) []string {
	var out []string
	for _, l := range strings.Split(patch, "\n") {
		l = strings.TrimSpace(l)
		for _, h := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: "} {
			if p, ok := strings.CutPrefix(l, h); ok {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// invokesApplyPatch tells a shell command that RUNS apply_patch from one that merely mentions the marker
// (grep -F '*** Begin Patch' file.go). The command word may carry a path or quotes (/tmp/tools/apply_patch,
// ./applypatch, "apply_patch") and may be glued to its heredoc (apply_patch<<EOF): match on the basename.
var invokesApplyPatch = regexp.MustCompile(`(^|[\s;&|(` + "`" + `'"])([^\s;&|()'"<>` + "`" + `]*/)?(apply_patch|applypatch)['"]?(\s|$|<)`)
