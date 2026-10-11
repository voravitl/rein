package guard

import (
	"github.com/voravitl/rein/internal/contract"
	"path/filepath"
	"testing"
)

func TestEnvCBypass(t *testing.T) {
	e := newCoordEnv(t)
	if _, err := contract.New("side", 1, filepath.Join(e.outside, "run"), "src/**", "", "S1", filepath.Dir(e.side), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	e.expect(false, e.bash("touch README.md"), "control: coordinator may write README.md")
	for _, command := range []string{
		"env -C ../side bash -c 'touch README.md'",
		"env --chdir ../side bash -c 'touch README.md'",
		"env --chdir=../side bash -c 'touch README.md'",
		"env -C../side bash -c 'touch README.md'",
		"env -C ../side -S 'touch README.md'",
		"env -C docs -S '-C .. touch README.md'",
		"env -C docs -S '' -C .. touch README.md",
		`cd "$UNKNOWN" && env -C sub bash -c 'touch README.md'`,
	} {
		// This path is writable from the coordinator checkout, but not from the contracted side worktree.
		if res := e.bash(command); res == "" {
			t.Fatalf("env cwd was not used to check the write: %s", command)
		}
	}
}
