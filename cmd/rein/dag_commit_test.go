package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func dagCommitRepo(t *testing.T) (string, string) {
	t.Helper()
	wt := t.TempDir()
	gitIn(t, wt, "init", "-q")
	gitIn(t, wt, "config", "user.email", "t@t")
	gitIn(t, wt, "config", "user.name", "t")
	for path, body := range map[string]string{"src/a.go": "a\n", "docs/x.md": "x\n"} {
		if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "commit", "-q", "-m", "base")
	return wt, dagGitOut(t, wt, "rev-parse", "HEAD")
}

func dagGitOut(t *testing.T, wt string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func dagCommitContract(wt string) *contract.Contract {
	return &contract.Contract{Name: "run-code", Worktree: wt, Allow: []string{"src/**"}, Deny: append([]string{"src/secret/**"}, contract.AlwaysDeny...)}
}

func TestCommitDAGWorkerResultCommitsInScopeEdits(t *testing.T) {
	wt, base := dagCommitRepo(t)
	if err := os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "src", "new file.go"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	committed, err := commitDAGWorkerResult(dagCommitContract(wt), wt, "run-code", "attempt-1")
	if err != nil || !committed {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
	if got := dagGitOut(t, wt, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("tree not clean after commit:\n%s", got)
	}
	if got := dagGitOut(t, wt, "rev-parse", "HEAD~1"); got != base {
		t.Fatalf("commit is not one commit on top of base: HEAD~1=%s base=%s", got, base)
	}
	if got := dagGitOut(t, wt, "log", "-1", "--format=%an <%ae>|%cn"); got != "rein-supervisor <rein-supervisor@localhost>|rein-supervisor" {
		t.Fatalf("author/committer = %q", got)
	}
	files := dagGitOut(t, wt, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if files != "src/a.go\nsrc/new file.go" {
		t.Fatalf("committed files = %q", files)
	}
}

func TestCommitDAGWorkerResultSkipsCleanTree(t *testing.T) {
	wt, base := dagCommitRepo(t)
	committed, err := commitDAGWorkerResult(dagCommitContract(wt), wt, "run-code", "attempt-1")
	if err != nil || committed {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
	if got := dagGitOut(t, wt, "rev-parse", "HEAD"); got != base {
		t.Fatalf("HEAD moved on a clean tree")
	}
	// A worker that committed its own in-scope edit is not committed again.
	if err := os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("own\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "commit", "-q", "-am", "worker commit")
	own := dagGitOut(t, wt, "rev-parse", "HEAD")
	committed, err = commitDAGWorkerResult(dagCommitContract(wt), wt, "run-code", "attempt-1")
	if err != nil || committed || dagGitOut(t, wt, "rev-parse", "HEAD") != own {
		t.Fatalf("re-committed a clean tree: committed=%v err=%v", committed, err)
	}
}

func TestCommitDAGWorkerResultRefusesOutOfContract(t *testing.T) {
	for name, path := range map[string]string{
		"outside-allow":     "docs/x.md",
		"deny-glob":         "src/secret/k.go",
		"untracked-outside": "notes.txt",
	} {
		t.Run(name, func(t *testing.T) {
			wt, base := dagCommitRepo(t)
			if err := os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(path)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, path), []byte("bad\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before := dagGitOut(t, wt, "status", "--porcelain", "--untracked-files=all")
			committed, err := commitDAGWorkerResult(dagCommitContract(wt), wt, "run-code", "attempt-1")
			if err == nil || committed || !strings.Contains(err.Error(), "outside the task contract") {
				t.Fatalf("committed=%v err=%v", committed, err)
			}
			if dagGitOut(t, wt, "rev-parse", "HEAD") != base {
				t.Fatal("refused commit moved HEAD")
			}
			if after := dagGitOut(t, wt, "status", "--porcelain", "--untracked-files=all"); after != before {
				t.Fatalf("refused commit changed the index or tree:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// A hostile repository hook must not run when the supervisor commits.
func TestCommitDAGWorkerResultIgnoresRepositoryHooks(t *testing.T) {
	wt, _ := dagCommitRepo(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	// A repository-level hooksPath overrides any global one, so the hook is live
	// on every host (a developer's global core.hooksPath would otherwise mask it).
	hooksDir := filepath.Join(wt, ".git", "repo-hooks")
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "config", "core.hooksPath", hooksDir)
	hook := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Prove the hook is live before testing that the supervisor bypasses it.
	if err := os.WriteFile(filepath.Join(wt, "src", "probe.go"), []byte("p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "src/probe.go")
	if exec.Command("git", "-C", wt, "commit", "-qm", "probe").Run() == nil {
		t.Fatal("fixture hook did not block a normal commit")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("fixture hook did not run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if committed, err := commitDAGWorkerResult(dagCommitContract(wt), wt, "run-code", "attempt-1"); err != nil || !committed {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository pre-commit hook ran during the supervisor commit")
	}
}
