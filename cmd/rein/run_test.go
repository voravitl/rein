package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRunUserOnlyCommandsRefuseUnderClaude(t *testing.T) {
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	pid := strconv.Itoa(os.Getpid())

	if code := cmdRun([]string{"start", root, "--run", "r1", "--session", "s1", "--pid", pid}); code != 0 {
		t.Fatalf("start = %d", code)
	}
	if code := cmdRun([]string{"start", root, "--run", "r2", "--session", "s1", "--pid", pid}); code != 2 {
		t.Fatalf("a second start must fail, got %d", code)
	}
	loc, _ := run.Locate(root)
	for _, env := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_PID"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")
			if code := cmdRun([]string{"allow", root, "--task", "t1", "--reason", "x"}); code != 2 {
				t.Errorf("allow under Claude = %d, want 2", code)
			}
			if code := cmdRun([]string{"end", root, "--abandon", "--reason", "x"}); code != 2 {
				t.Errorf("end --abandon under Claude = %d, want 2", code)
			}
			if m, err := run.Load(loc.Marker); err != nil || len(m.Allowed) != 0 {
				t.Errorf("the refused commands changed the run: %+v %v", m, err)
			}
			// a clean end and an audit stay coordinator commands
			if code := cmdRun([]string{"audit", root}); code != 0 {
				t.Errorf("audit under Claude = %d", code)
			}
		})
	}
	if code := cmdRun([]string{"allow", root, "--task", "t1"}); code != 2 { // no reason
		t.Errorf("allow without a reason = %d", code)
	}
	if code := cmdRun([]string{"allow", root, "--task", "t1", "--reason", "user ruled"}); code != 0 {
		t.Errorf("allow in the user's terminal = %d", code)
	}
	if m, _ := run.Load(loc.Marker); len(m.Allowed) != 1 {
		t.Errorf("allowance not recorded: %+v", m)
	}
	if code := cmdRun([]string{"end", root, "--abandon"}); code != 2 { // no reason
		t.Errorf("abandon without a reason = %d", code)
	}
	if code := cmdRun([]string{"end", root}); code != 0 {
		t.Fatalf("clean end = %d", code)
	}
	if _, err := run.Load(loc.Marker); err == nil {
		t.Fatal("run still active after a clean end")
	}
}

func TestRunStartNeedsAnOwner(t *testing.T) {
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	if code := cmdRun([]string{"start", root, "--run", "r"}); code != 2 {
		t.Fatalf("start with no owner = %d, want 2", code)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess")
	t.Setenv("CLAUDE_PID", "not-a-number")
	if code := cmdRun([]string{"start", root, "--run", "r"}); code != 2 {
		t.Fatalf("start with a bad CLAUDE_PID = %d, want 2", code)
	}
}
