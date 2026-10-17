package orca

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

func controllerRepo(t *testing.T, supervised bool) string {
	t.Helper()
	root := contract.Real(t.TempDir())
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	owner := run.Owner{SessionID: "guard-test", PID: os.Getpid()}
	var err error
	if supervised {
		_, err = run.StartSupervised(root, "r", nil, owner, strings.Repeat("a", 64))
	} else {
		_, err = run.Start(root, "r", nil, owner)
	}
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSupervisedRunRefusesLegacyOrcaBeforeRunner(t *testing.T) {
	root := controllerRepo(t, true)
	t.Setenv("ORCA_REPO_DIR", root)
	t.Setenv("ORCA_RUN", "r")
	t.Setenv("ORCA_COORD", "coord")
	calls := 0
	SetRunner(&mockRunner{handlers: map[string]func([]string) ([]byte, error){
		"orca": func([]string) ([]byte, error) { calls++; return []byte(`{"result":{}}`), nil },
		"git":  func([]string) ([]byte, error) { calls++; return nil, nil },
	}})
	defer SetRunner(nil)
	ctx := context.Background()
	actions := map[string]func() error{
		"launch": func() error {
			_, err := Launch(ctx, LaunchOptions{Name: "task", BaseBranch: "main", SpecContent: "work", Agent: "claude", RepoDir: root, RepoID: "repo", RunID: "r", CoordHandle: "coord"})
			return err
		},
		"wait":  func() error { _, err := Wait(ctx, WaitOptions{CoordHandle: "coord", MaxRounds: 1}); return err },
		"drain": func() error { _, err := Drain(ctx, "coord", true); return err },
		"close": func() error {
			_, err := Close(ctx, CloseOptions{WorktreePath: root, TerminalHandle: "worker"})
			return err
		},
		"send": func() error {
			_, err := Call(ctx, CallRequest{Action: "send", Args: map[string]interface{}{"terminal": "worker", "text": "go"}})
			return err
		},
	}
	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			if err := action(); err == nil || !strings.Contains(err.Error(), "legacy controller") {
				t.Fatalf("expected supervised refusal, got %v", err)
			}
			if calls != 0 {
				t.Fatalf("runner invoked before refusal: %d", calls)
			}
		})
	}
}

func TestLegacyControllerCompatibilityAndContention(t *testing.T) {
	root := controllerRepo(t, false)
	t.Setenv("ORCA_REPO_DIR", root)
	t.Setenv("ORCA_RUN", "")
	unlock, err := legacyController("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	second, err := legacyController(root, "r")
	if err != nil {
		t.Fatalf("parallel legacy operation blocked: %v", err)
	}
	second()
	if _, err := legacyController(root, "foreign"); err == nil {
		t.Fatal("foreign run accepted")
	}
	loc, _ := run.Locate(root)
	if release, err := run.LockFile(loc.Marker+".controller.lock", 0); err == nil {
		release()
		t.Fatal("exclusive controller lock overlapped legacy action")
	}
	unlock()
	release, err := run.LockFile(loc.Marker+".controller.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := legacyController(root, "r"); err == nil {
		t.Fatal("legacy operation overlapped exclusive controller")
	}
	if _, err := os.Stat(filepath.Join(root, ".orca-task.md")); !os.IsNotExist(err) {
		t.Fatalf("guard created task file: %v", err)
	}
}

func TestInvalidRepositoryHintCannotHideSupervisedCwd(t *testing.T) {
	root := controllerRepo(t, true)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	t.Setenv("ORCA_REPO_DIR", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("ORCA_RUN", "r")
	calls := 0
	SetRunner(&mockRunner{handlers: map[string]func([]string) ([]byte, error){"orca": func([]string) ([]byte, error) { calls++; return nil, nil }}})
	defer SetRunner(nil)
	if _, err := Drain(context.Background(), "coord", true); err == nil {
		t.Fatal("invalid hint hid supervised cwd")
	}
	if calls != 0 {
		t.Fatalf("runner called: %d", calls)
	}
	if _, err := Close(context.Background(), CloseOptions{WorktreePath: filepath.Join(t.TempDir(), "removed"), TerminalHandle: "worker"}); err == nil {
		t.Fatal("removed checkout hid supervised cwd")
	}
	if calls != 0 {
		t.Fatalf("runner called: %d", calls)
	}
	other := controllerRepo(t, false)
	t.Setenv("ORCA_REPO_DIR", other)
	if _, err := Drain(context.Background(), "coord", true); err == nil {
		t.Fatal("valid foreign repository hid supervised cwd")
	}
	if calls != 0 {
		t.Fatalf("runner called: %d", calls)
	}
}
