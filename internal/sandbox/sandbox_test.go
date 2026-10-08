package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func TestApplyMergesAndExcludes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	m := filepath.Join(root, "m")
	for _, a := range [][]string{{"init", "-q", m}, {"-C", m, "commit", "-q", "--allow-empty", "-m", "base"}, {"-C", m, "worktree", "add", "-q", "-b", "w", filepath.Join(root, "wts", "w")}} {
		cmd := exec.Command("git", a...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", a, out)
		}
	}
	c, err := contract.New("w", 0, filepath.Join(root, "run"), "src/**", "", "S1", filepath.Join(root, "wts"), "", "", 0,
		&contract.Profile{SandboxExcludedCommands: []string{"orca *"}, SandboxAllowedDomains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wts", "w")
	_ = os.MkdirAll(filepath.Join(wt, ".claude"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".claude", "settings.local.json"), []byte(`{"permissions":{"allow":["Bash(ls)"]}}`), 0o644)
	for i := 0; i < 2; i++ { // idempotent
		if _, _, err := Apply(c); err != nil {
			t.Fatal(err)
		}
	}
	var doc map[string]any
	b, _ := os.ReadFile(filepath.Join(wt, ".claude", "settings.local.json"))
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["permissions"] == nil {
		t.Error("existing keys must be kept")
	}
	sb := doc["sandbox"].(map[string]any)
	if sb["enabled"] != true || sb["allowUnsandboxedCommands"] != false || sb["excludedCommands"] == nil || sb["network"] == nil {
		t.Errorf("sandbox block: %v", sb)
	}
	out, _ := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if strings.Contains(string(out), "settings.local.json") {
		t.Errorf("settings file must be excluded from git status: %q", out)
	}
	ex, _ := os.ReadFile(filepath.Join(m, ".git", "info", "exclude"))
	if strings.Count(string(ex), ".claude/settings.local.json") != 1 {
		t.Errorf("exclude line must appear once:\n%s", ex)
	}
}

func TestApplyNeedsWorktree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	c, _ := contract.New("missing", 0, filepath.Join(root, "run"), "src/**", "", "S1", filepath.Join(root, "wts"), "", "", 0, nil)
	if _, _, err := Apply(c); err == nil || !strings.Contains(err.Error(), "does not exist yet") {
		t.Errorf("want a create-it-first error, got %v", err)
	}
}
