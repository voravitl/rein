package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/supervisor"
)

// freshDAGWorkerFixture is the DAG worker fixture with every ledger row the
// shared e2e fixture seeds removed: a brand-new install with no evidence.
func freshDAGWorkerFixture(t *testing.T, calibration *supervisor.TaskCalibration, ownerCaps bool) (*e2eEnv, string) {
	t.Helper()
	e, path, p, _ := localDAGWorkerFixture(t)
	if err := os.Remove(ledger.Path()); err != nil {
		t.Fatal(err)
	}
	if calibration != nil {
		zero := 0.0
		if ownerCaps {
			e.c.Profile.Selection.Calibration = &contract.Calibration{Authorization: "owner-approved DAG pilot", MaxCalls: 2, PoolCaps: map[string]float64{"agy/credits": 10}, MaxCashUSD: &zero}
		}
		if _, err := e.c.Save(); err != nil {
			t.Fatal(err)
		}
		hash := routing.ContractHash(e.c)
		var tp routing.TaskProfile
		if err := json.Unmarshal(p.Tasks[0].Profile, &tp); err != nil {
			t.Fatal(err)
		}
		tp.ContractHash = hash
		p.Tasks[0].Profile, _ = json.Marshal(tp)
		p.Tasks[0].ContractHash = hash
		p.Tasks[0].Calibration = calibration
		bindDAGPlan(t, e, path, p)
	}
	return e, path
}

func TestDAGFreshInstallScoredWorkerParksWithoutEvidence(t *testing.T) {
	_, path := freshDAGWorkerFixture(t, nil, false)
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	var result supervisor.GraphResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if code == 0 && result.Status == "verified" {
		t.Fatalf("scored worker launched with no ledger evidence: %+v", result)
	}
	if !strings.Contains(out, "no_eligible_candidate") && !strings.Contains(out, "no candidate") {
		t.Fatalf("want a no-candidate refusal, got %d %s", code, out)
	}
}

func TestDAGCalibrationWorkerRunsOnFreshLedger(t *testing.T) {
	e, path := freshDAGWorkerFixture(t, &supervisor.TaskCalibration{Amount: 3}, true)
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	var result supervisor.GraphResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if code != 0 || result.Status != "verified" || len(result.Completed) != 1 || result.Completed[0] != "task" {
		t.Fatalf("calibration DAG %d %+v", code, result)
	}
	if got := dagGitOut(t, e.wt, "show", "HEAD:src/a.go"); got != "fixed" {
		t.Fatalf("committed src/a.go = %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d routing.Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.SelectionMode != "calibration_unscored" || d.Provider != "w" || d.Hold == "" || !routing.ReceiptUsed(d.Attempt) {
		t.Fatalf("worker did not run under a used calibration receipt: %+v", d)
	}
	holds, err := budget.HoldsFor(d.Attempt)
	if err != nil || len(holds) != 1 || holds[0].State == "reserved" || holds[0].Items[0].Kind != "calibration" {
		t.Fatalf("calibration hold not settled: %+v %v", holds, err)
	}
	if out, err := budget.Outstanding(); err != nil || len(out) != 0 {
		t.Fatalf("unsettled holds %+v %v", out, err)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	launched, charged := false, false
	for _, r := range rows {
		if r.Attempt != d.Attempt {
			continue
		}
		launched = launched || (r.Kind == "routing_launch" && r.Worker != nil && r.Worker.Model == d.Model)
		charged = charged || (r.Kind == "cost" && r.Component == "calibration" && r.Amount != nil && *r.Amount == 3)
		if r.Kind == "routing_probe" {
			t.Fatal("calibration worker was probed")
		}
	}
	if !launched || !charged {
		t.Fatalf("ledger missing launch or bounded charge: launched=%v charged=%v", launched, charged)
	}
	// A completed calibration worker replays without a second launch.
	again, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 {
		t.Fatalf("replay %d %s", code, again)
	}
	if after, _ := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json")); string(after) != string(raw) {
		t.Fatal("replay prepared another worker")
	}
}

func TestDAGCalibrationWorkerRefusedWithoutOwnerCaps(t *testing.T) {
	e, path := freshDAGWorkerFixture(t, &supervisor.TaskCalibration{Amount: 3}, false)
	out, code := captureBoth(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code == 0 && strings.Contains(out, `"verified"`) {
		t.Fatalf("calibration worker ran without owner caps: %s", out)
	}
	if !strings.Contains(out, "calibration authorization and caps required") {
		t.Fatalf("want the missing-authorization refusal, got %s", out)
	}
	if _, err := os.Stat(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json")); err == nil {
		t.Fatal("receipt written without owner calibration authorization")
	}
}

func TestDAGCalibrationWorkerRefusedOverCap(t *testing.T) {
	_, path := freshDAGWorkerFixture(t, &supervisor.TaskCalibration{Amount: 11}, true)
	out, code := captureBoth(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code == 0 && strings.Contains(out, `"verified"`) {
		t.Fatalf("calibration worker over the pool cap ran: %s", out)
	}
	if !strings.Contains(out, "calibration_cap_exhausted") {
		t.Fatalf("want a calibration cap refusal, got %s", out)
	}
	if holds, err := budget.Outstanding(); err != nil || len(holds) != 0 {
		t.Fatalf("refused calibration left holds %+v %v", holds, err)
	}
}

// The real supervisor journal (finalize results) feeds rein dag integrate.
func TestDagIntegrateAfterRealSupervisorRun(t *testing.T) {
	e, path, _, _ := localDAGWorkerFixture(t)
	// A fresh machine: no git identity anywhere.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if out, code := captureBoth(t, func() int { return cmdDag([]string{"integrate", "--plan", path, "--branch", "too-early"}) }); code != 1 || !strings.Contains(out, "journal") {
		t.Fatalf("integrate before any run: %d %s", code, out)
	}
	if out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code != 0 || !strings.Contains(out, `"verified"`) {
		t.Fatalf("supervisor run %d %s", code, out)
	}
	target := filepath.Join(t.TempDir(), "integrated")
	out, code := captureBoth(t, func() int {
		return cmdDag([]string{"integrate", "--plan", path, "--branch", "dag/integrated", "--worktree", target})
	})
	if code != 0 || !strings.Contains(out, "goal verification passed") {
		t.Fatalf("integrate %d %s", code, out)
	}
	if got := dagGitOut(t, target, "show", "HEAD:src/a.go"); got != "fixed" {
		t.Fatalf("integrated src/a.go = %q", got)
	}
	if got := dagGitOut(t, e.wt, "log", "-1", "--format=%an|%cn", "dag/integrated"); got != "rein-supervisor|rein-supervisor" {
		t.Fatalf("integrated branch head author|committer = %q", got)
	}
}
