package main

import (
	"encoding/json"
	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChargeCannotOverrideLaunchRun(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err := ledger.Append(ledger.Row{Kind: "routing_launch", Attempt: "a", Run: "real", Task: "t", Decision: "d", Worker: &ledger.AgentModel{Agent: "codex", Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--attempt", "a", "--pool", "p", "--unit", "usd", "--amount", "1", "--charge-id", "x", "--run", "wrong"}
	if code := cmdLedgerCharge(args); code == 0 {
		t.Fatal("contradictory run accepted")
	}
	rows, err := ledger.LoadStrict("")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rejected charge changed ledger: %+v %v", rows, err)
	}
	args[len(args)-1] = "real"
	if code := cmdLedgerCharge(args); code != 0 {
		t.Fatalf("matching run refused: %d", code)
	}
}

func TestCalibrationReviewChargeEntersMeasuredCohortAndSettlesOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	p := providers.Provider{Agent: "codex", Model: "review-model", Effort: "high", Launch: "shell",
		Billing: &providers.Billing{Mode: "api", Unit: "usd", Pool: "review-pool"}}
	cfgPath := filepath.Join(root, "config.json")
	b, _ := json.Marshal(providers.Config{Providers: map[string]providers.Provider{"r": p}})
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	amount := 2.0
	h, err := budget.Reserve("", nil, budget.ReserveRequest{Run: "run", Task: "task", Attempt: "cal-attempt",
		Items:       []budget.Item{{Pool: "review-pool", Unit: "usd", Amount: 3, Calls: 2, Kind: "calibration", Provider: "r", Model: p.Model, Effort: p.Effort, Config: "frozen-config"}},
		Calibration: &budget.CalibrationCaps{MaxCalls: 2, PoolCaps: map[string]float64{"review-pool": 3}, MaxCashUSD: 3}})
	if err != nil || h == nil {
		t.Fatalf("reserve calibration: %v %+v", err, h)
	}
	args := []string{"--attempt", h.Attempt, "--component", "review", "--provider", "r", "--model", p.Model, "--pool", "review-pool", "--unit", "usd", "--amount", "2", "--config", cfgPath}
	if code := cmdLedgerCharge(args); code != 0 {
		t.Fatalf("calibration charge refused: %d", code)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	ev := ledger.EvaluateReviewCost(rows, "r", p.Model, p.Effort, "frozen-config", ledger.EvidenceOptions{Now: time.Now(), MaxAge: 24 * time.Hour})
	if !ev.Available || ev.Reviews != 1 || ev.PerReview["review-pool"] != amount {
		t.Fatalf("calibration charge not usable as exact reviewer evidence: %+v", ev)
	}
	holds, err := routing.SettleAttempt(h.Attempt)
	if err != nil || len(holds) != 1 || holds[0].Used["review-pool"] != amount {
		t.Fatalf("settlement duplicated or lost measured charge: %+v %v", holds, err)
	}
	_, err = budget.Reserve("", nil, budget.ReserveRequest{Run: "run", Task: "task", Attempt: "second-calibration",
		Items:       []budget.Item{{Pool: "review-pool", Unit: "usd", Amount: 1, Calls: 1, Kind: "calibration"}},
		Calibration: &budget.CalibrationCaps{MaxCalls: 2, PoolCaps: map[string]float64{"review-pool": 3}, MaxCashUSD: 3}})
	if err == nil {
		t.Fatal("settled reviewer calibration disappeared from call/pool/cash caps")
	}
	for name, caps := range map[string]*budget.CalibrationCaps{
		"pool": {MaxCalls: 10, PoolCaps: map[string]float64{"review-pool": 2}, MaxCashUSD: 10},
		"cash": {MaxCalls: 10, PoolCaps: map[string]float64{"review-pool": 10}, MaxCashUSD: 2},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := budget.Reserve("", nil, budget.ReserveRequest{Run: "run", Task: "task", Attempt: "second-" + name,
				Items: []budget.Item{{Pool: "review-pool", Unit: "usd", Amount: 0.1, Calls: 1, Kind: "calibration"}}, Calibration: caps})
			if err == nil {
				t.Fatalf("settled reviewer calibration disappeared from %s cap", name)
			}
		})
	}
}

func TestCalibrationReviewChargeRequiresMatchingOutstandingHold(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	args := []string{"--attempt", "no-hold", "--component", "review", "--provider", "r", "--model", "m", "--pool", "p", "--unit", "usd", "--amount", "1"}
	if code := cmdLedgerCharge(args); code == 0 {
		t.Fatal("review charge without a calibration hold accepted")
	}
	rows, err := ledger.LoadStrict("")
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused charge changed ledger: %+v %v", rows, err)
	}
}

func TestReviewChargeUsesLaunchCohort(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	launch := ledger.Row{Kind: "routing_launch", Attempt: "a", Run: "real", Task: "t", Decision: "d", Provider: "r", Type: "backend", Role: "review:auto", Effort: "high", Config: "old-launch-config", Worker: &ledger.AgentModel{Agent: "codex", Model: "m"}}
	if err := ledger.Append(launch); err != nil {
		t.Fatal(err)
	}
	args := []string{"--attempt", "a", "--component", "review", "--provider", "r", "--model", "m", "--pool", "p", "--unit", "usd", "--amount", "1", "--charge-id", "x"}
	if code := cmdLedgerCharge(args); code != 0 {
		t.Fatalf("review charge refused: %d", code)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	got := rows[len(rows)-1]
	if got.Effort != launch.Effort || got.Config != launch.Config {
		t.Fatalf("cohort lost: %+v", got)
	}
	args[5] = "different-reviewer"
	if code := cmdLedgerCharge(args); code == 0 {
		t.Fatal("charge for another reviewer attributed to launch")
	}
}

func TestReviewChargeRejectsWorkerOrMalformedLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		worker     *ledger.AgentModel
	}{
		{"worker task", "worker:auto", &ledger.AgentModel{Agent: "codex", Model: "m"}},
		{"missing worker", "review:auto", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
			launch := ledger.Row{Kind: "routing_launch", Attempt: "a", Run: "r", Provider: "reviewer", Type: "review", Role: tc.role, Config: "cfg", Worker: tc.worker}
			if err := ledger.Append(launch); err != nil {
				t.Fatal(err)
			}
			args := []string{"--attempt", "a", "--component", "review", "--provider", "reviewer", "--model", "m", "--pool", "p", "--unit", "usd", "--amount", "1"}
			if code := cmdLedgerCharge(args); code == 0 {
				t.Fatal("non-review or malformed launch accepted")
			}
			rows, err := ledger.LoadStrict("")
			if err != nil || len(rows) != 1 {
				t.Fatalf("rejected charge changed ledger: %+v %v", rows, err)
			}
		})
	}
}

func TestAutomaticReviewRecordLaunchThenCharge(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	d := &routing.Decision{Task: "t", Run: "run", Chain: "review:auto", Provider: "r", Agent: "kiro", Model: "claude-opus-5-5-high", Attempt: "at-r", DecisionID: "d-r", Kind: "backend", Tier: "T3", Suite: "s1", Effort: "high", ConfigFingerprint: "launch-config"}
	if err := routing.RecordLaunch(d); err != nil {
		t.Fatal(err)
	}
	args := []string{"--attempt", d.Attempt, "--component", "review", "--provider", d.Provider, "--model", d.Model, "--pool", "p", "--unit", "usd", "--amount", "1", "--charge-id", "x"}
	if code := cmdLedgerCharge(args); code != 0 {
		t.Fatalf("actual auto reviewer launch cannot record cost: %d", code)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	l, ok := ledger.LaunchRow(rows, d.Attempt)
	if !ok || l.Decision != d.DecisionID || l.Type != d.Kind || l.Tier != d.Tier || l.Suite != d.Suite || l.Worker.Agent != d.Agent || l.Role != d.Chain {
		t.Fatalf("lost receipt identity: %+v", l)
	}
	c := rows[len(rows)-1]
	if c.Effort != d.Effort || c.Config != d.ConfigFingerprint || c.Run != d.Run || c.Task != d.Task || c.Decision != d.DecisionID {
		t.Fatalf("lost reviewer attribution: %+v", c)
	}
}

func TestWorkerPreflightIsNotAnAttributableExecution(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	d := &routing.Decision{Task: "t", Run: "run", Chain: "worker:auto", Provider: "w", Agent: "codex", Model: "gpt-worker", Attempt: "at-w", DecisionID: "d-w", Kind: "backend", Tier: "T1", Suite: "s1", Effort: "high", ConfigFingerprint: "launch-config"}
	if err := routing.RecordLaunch(d); err != nil {
		t.Fatal(err)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ledger.LaunchRow(rows, d.Attempt); ok {
		t.Fatal("worker preflight created an executed attempt")
	}
	if len(rows) != 1 || rows[0].Attempt != "" || rows[0].Worker != nil {
		t.Fatalf("preflight populated execution identity: %+v", rows)
	}
	// The actual worker launcher records the attributable execution separately.
	actual := ledger.Row{Kind: "routing_launch", Task: d.Task, Run: d.Run, Role: d.Chain, Provider: d.Provider, Model: d.Model, Worker: &ledger.AgentModel{Agent: d.Agent, Model: d.Model}, Attempt: d.Attempt, Decision: d.DecisionID, Type: d.Kind, Tier: d.Tier, Suite: d.Suite, Effort: d.Effort, Config: d.ConfigFingerprint}
	if err := ledger.Append(actual); err != nil {
		t.Fatal(err)
	}
	rows, err = ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	executions := 0
	for _, r := range rows {
		if r.Kind == "routing_launch" && r.Attempt == d.Attempt && r.Worker != nil {
			executions++
		}
	}
	if executions != 1 {
		t.Fatalf("worker execution count = %d", executions)
	}
}
