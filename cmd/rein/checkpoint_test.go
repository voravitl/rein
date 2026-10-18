package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdCheckpointLifecycle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	wt := filepath.Join(root, "source")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("config", "core.hooksPath", "/dev/null")
	if err := os.WriteFile(filepath.Join(wt, "file"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	git("commit", "-qm", "base")
	base := strings.TrimSpace(git("rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(wt, "file"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	if err := os.WriteFile(filepath.Join(wt, "extra"), []byte{0, 255, 42}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := capture(t, func() int {
		return cmdCheckpoint([]string{"finalize", "--task", "task", "--attempt", "a1", "--run", "r", "--worktree", wt, "--base", base, "--contract-hash", "hash", "--untracked", "extra"})
	}); code != 0 {
		t.Fatalf("finalize: %d", code)
	}
	if code := cmdCheckpoint([]string{"verify", "--task", "task", "--attempt", "a1"}); code != 0 {
		t.Fatalf("verify: %d", code)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "restored")
	if code := cmdCheckpoint([]string{"restore", "--task", "task", "--attempt", "a1", "--dest", dest}); code != 0 {
		t.Fatalf("restore without source: %d", code)
	}
	b, err := os.ReadFile(filepath.Join(dest, "file"))
	if err != nil || string(b) != "staged\n" {
		t.Fatalf("restored file %q: %v", b, err)
	}
	cmd := exec.Command("git", "-C", dest, "diff", "--cached", "--name-only")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "file" {
		t.Fatalf("staged state %s: %v", out, err)
	}
	if code := cmdCheckpoint([]string{"restore", "--task", "task", "--attempt", "a1", "--dest", dest}); code != 1 {
		t.Fatalf("existing dest exit: %d", code)
	}
}

func TestCmdCheckpointInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"finalize"}, {"verify"}, {"restore", "--task", "t", "--attempt", "a"}, {"verify", "--task", "t", "--attempt", "a", "extra"}, {"verify", "--task", "t", "--attempt", "a", "--unknown"}} {
		if code := cmdCheckpoint(args); code != 2 {
			t.Errorf("args %v: code %d", args, code)
		}
	}
}
