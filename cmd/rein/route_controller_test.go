package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

func routeSupervisedFixture(t *testing.T) (string, *contract.Contract) {
	t.Helper()
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "a"), []byte("a"), 0600)
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "initial")
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	os.MkdirAll(contract.IndexDir(), 0700)
	c := &contract.Contract{Name: "guarded", Worktree: root, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(root, "report")}
	data, _ := json.Marshal(c)
	if err := os.WriteFile(contract.PathOf(c.Name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartSupervised(root, "guarded-run", nil, run.Owner{SessionID: "s", PID: os.Getpid()}, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	return root, c
}

func TestRouteMutationUsesActualTaskScope(t *testing.T) {
	_, c := routeSupervisedFixture(t)
	if unlock, err := acquireRouteMutation(c, "guarded-run"); err == nil {
		unlock()
		t.Fatal("legacy task mutation accepted supervised repository")
	}
	if unlock, err := acquireRouteHoldMutation([]budget.Hold{{Run: "guarded-run", Task: c.Name, Attempt: "a"}}); err == nil {
		unlock()
		t.Fatal("legacy attempt settlement accepted supervised task")
	}
}

func TestRouteMutationChecksInvocationScopesAndReleasesLocks(t *testing.T) {
	root, _ := routeSupervisedFixture(t)
	t.Setenv("ORCA_REPO_DIR", root)
	if unlock, err := acquireRouteMutation(nil, ""); err == nil {
		unlock()
		t.Fatal("environment scope bypassed ownership")
	}
	t.Setenv("ORCA_REPO_DIR", "")
	t.Chdir(root)
	if unlock, err := acquireRouteMutation(nil, ""); err == nil {
		unlock()
		t.Fatal("cwd scope bypassed ownership")
	}
	loc, _ := run.Locate(root)
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireRouteMutation(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := run.StartSupervised(root, "next", nil, run.Owner{SessionID: "s", PID: os.Getpid()}, strings.Repeat("b", 64)); err != nil {
		t.Fatal("leaked legacy lock", err)
	}
}

func TestRouteCooldownAndClearRefuseBeforeStateWrite(t *testing.T) {
	root, _ := routeSupervisedFixture(t)
	t.Setenv("ORCA_REPO_DIR", root)
	for _, action := range []string{"cooldown", "clear"} {
		if code := cmdRoute([]string{action, "--provider", "p", "--reason", "evidence"}); code != 1 {
			t.Fatalf("%s code %d", action, code)
		}
	}
	if _, err := os.Stat(filepath.Join(contract.IndexDir(), "routes", "cooldowns.json")); !os.IsNotExist(err) {
		t.Fatal("ownership refusal wrote cooldown state", err)
	}
}

func TestRouteHoldMutationRefusesUnknownOrContradictoryScope(t *testing.T) {
	for _, holds := range [][]budget.Hold{nil, {{Run: "r"}}, {{Run: "r", Task: "t"}, {Run: "other", Task: "t"}}} {
		if unlock, err := acquireRouteHoldMutation(holds); err == nil {
			unlock()
			t.Fatal("unresolved reservation scope accepted")
		}
	}
}

func TestRouteMutationRejectsMissingActualRepository(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "missing"), root} {
		if unlock, err := acquireRouteMutation(&contract.Contract{Name: "lost", Worktree: path}, "r"); err == nil {
			unlock()
			t.Fatal("unresolved actual contract scope accepted", path)
		}
	}
}
