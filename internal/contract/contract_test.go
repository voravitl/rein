package contract

import (
	"path/filepath"
	"testing"
)

func TestRelativeRunDirIsStoredAbsolute(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "idx"))
	t.Chdir(root)
	c, err := New("t", 0, "runs/r1", "src/**", "", "S1", "wts", "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"RunCopy": c.RunCopy, "ReportPath": c.ReportPath, "Worktree": c.Worktree} {
		if !filepath.IsAbs(p) {
			t.Errorf("%s = %q must be absolute", name, p)
		}
	}
	t.Chdir(t.TempDir()) // saving from another directory still updates the original durable copy
	c.HooksInstalled = []string{"codex"}
	saved, err := c.Save()
	if err != nil || len(saved) != 2 || saved[1] != c.RunCopy {
		t.Errorf("saved %v, %v", saved, err)
	}
}
