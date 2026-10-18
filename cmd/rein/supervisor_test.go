package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func TestSupervisorCLIProductionRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake Orca executable")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0700)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", b, err)
		}
	}
	t.Setenv("REIN_RUN_DIR", filepath.Join(dir, "run"))
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(dir, "contracts"))
	c := contract.Contract{Name: "task", Worktree: repo, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(dir, "report")}
	os.MkdirAll(contract.IndexDir(), 0700)
	b, _ := json.Marshal(c)
	os.WriteFile(contract.PathOf(c.Name), b, 0600)
	p := supervisor.Plan{Schema: 1, Run: "cli", Repo: repo, Terminal: "coord", Source: "orca-fixture", Version: "fixture", MaxChecks: 1, TimeoutMS: 10000, PollIntervalMS: 1, Tasks: []supervisor.Task{{Task: "task", Attempt: "a", CurrentAttempt: "a", OrcaTask: "ot", Dispatch: "d"}}}
	b, _ = json.Marshal(p)
	_, hash, err := supervisor.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "plan.json")
	os.WriteFile(planPath, b, 0600)
	if got := cmdSupervisor([]string{"validate", "--plan", planPath}); got != 0 {
		t.Fatal(got)
	}
	if _, err := run.StartSupervised(repo, "cli", nil, run.Owner{SessionID: "owner", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"decisions", "leases", "used"} {
		os.MkdirAll(filepath.Join(contract.IndexDir(), "routes", sub), 0700)
	}
	d := routing.Decision{Run: "cli", Task: "task", Attempt: "a", DecisionID: "decision", Worktree: repo, ContractHash: routing.ContractHash(&c)}
	b, _ = json.Marshal(d)
	os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "decisions", "task.json"), b, 0600)
	os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "used", "a"), nil, 0600)
	os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "leases", "task.json"), []byte(`{"task":"task","attempt":"a","decision":"decision","dispatched":true,"orca_task":"ot","dispatch":"d"}`), 0600)
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0700)
	trace := filepath.Join(dir, "trace")
	t.Setenv("SUPERVISOR_TEST_TRACE", trace)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SUPERVISOR_TEST_TRACE"
case " $* " in
  *" --ack "*) printf '%s\n' '{"result":{}}' ;;
  *) printf '%s\n' '{"result":{"deliveryId":"batch","messages":[{"id":"message","type":"worker_done","payload":{"taskId":"ot","dispatchId":"d","outcome":"worker_done"}}]}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "orca"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if got := cmdSupervisor([]string{"run", "--plan", planPath}); got != 0 {
		t.Fatalf("run exit %d", got)
	}
	events, err := routing.ReadEvents()
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if _, err := os.Stat(trace); err != nil {
		t.Fatal("fake Orca was not executed")
	}
}

func TestSupervisorCLIUsageAndInvalidPlan(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"validate"}, {"run", "--unknown"}, {"validate", "--plan", "/does/not/exist"}} {
		if got := cmdSupervisor(args); got != 2 {
			t.Fatalf("%v exit=%d", args, got)
		}
	}
}
