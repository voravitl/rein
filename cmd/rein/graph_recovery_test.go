package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/ledger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func quotaGraphFixture(t *testing.T, override ...string) (*e2eEnv, string, supervisor.Plan) {
	t.Helper()
	worker := `if [ -z "$REIN_HANDOFF_PATH" ]; then
 printf 'partial\n' > src/a.go
 printf 'approved quota marker\n'
 while :; do sleep 1; done
fi
test -n "$REIN_HANDOFF_SHA256" || exit 2
grep '"previous_attempt"' "$REIN_HANDOFF_PATH" >/dev/null || exit 3
test "$(cat src/a.go)" = partial || exit 4
printf 'fixed\n' > src/a.go`
	if len(override) > 0 {
		worker = override[0]
	}
	return localGraphFixture(t, worker, `test "$(cat src/a.go)" = fixed`, func(e *e2eEnv, p *supervisor.Plan) {
		cfg, err := providers.Load(e.cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		backup := cfg.Providers["w"]
		billing := *backup.Billing
		billing.Pool = "backup/credits"
		backup.Billing = &billing
		backup.Pool = "backup-quota"
		backup.Capabilities = append(append([]string(nil), backup.Capabilities...), "continuation")
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("backup-seed-%d", i)
			launch := ledger.Row{Kind: "routing_launch", Attempt: id, Run: "old", Task: "old", Worker: &ledger.AgentModel{Agent: backup.Agent, Model: backup.Model}, Type: "backend", Effort: backup.Effort, Config: routing.ProviderFingerprint(backup), Suite: "s1", Tier: "T1"}
			outcome := ledger.Row{Kind: "task", Attempt: id, Run: "old", Task: "old", Source: "measured", GatesPassed: yes(), Reviewer: &ledger.AgentModel{Agent: "claude", Model: "claude-opus-5-5-high"}, ReviewSHA: "sha", Approved: true, Blockers: zero(), Highs: zero(), FalseClaims: zero(), Drift: zero(), ReviewRounds: one()}
			charge, _ := ledger.NewChargeRow(id, "backup", "worker", billing.Pool, billing.Unit, backup.Agent, backup.Model, 2)
			seedRows(t, launch, outcome, charge)
		}
		e.c.Profile.Selection.Objective = "quality_first"
		if _, err := e.c.Save(); err != nil {
			t.Fatal(err)
		}
		e.hash = routing.ContractHash(e.c)
		var profile routing.TaskProfile
		json.Unmarshal(e.pj, &profile)
		profile.ContractHash = e.hash
		e.pj, _ = json.Marshal(profile)
		p.Tasks[0].ContractHash = e.hash
		p.Tasks[0].Profile = e.pj
		cfg.Providers["zbackup"] = backup
		cfg.WorkerChains["backend"] = []string{"w", "zbackup"}
		cfg.QuotaSignals = map[string][]string{"antigravity": {"approved quota marker"}}
		raw, _ := json.Marshal(cfg)
		if err := os.WriteFile(e.cfgPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		p.Pools = append(p.Pools, "backup-quota")
		p.Tasks[0].Launches["zbackup"] = p.Tasks[0].Launches["w"]
	})
}

func TestGraphCLIAliveQuotaStopsThenFallbackUsesPartialWorkAndHandoff(t *testing.T) {
	e, path, _ := quotaGraphFixture(t)
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 || !strings.Contains(out, `"task"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	raw, _ := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	var d routing.Decision
	json.Unmarshal(raw, &d)
	if d.Provider != "zbackup" {
		t.Fatalf("fallback receipt: %+v", d)
	}
	execution, err := os.ReadFile(graphEvidencePath(d.Attempt, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	var x graphExecution
	json.Unmarshal(execution, &x)
	if x.HandoffPath == "" || x.HandoffDigest == "" {
		t.Fatal("continuation receipt not passed")
	}
	if out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code != 0 {
		t.Fatalf("restart %d %s", code, out)
	}
	after, _ := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	if string(raw) != string(after) {
		t.Fatal("restart repeated attempt")
	}
}

func TestGraphResumeRejectsChangedCheckpointExecutionAndHandoff(t *testing.T) {
	for _, kind := range []string{"worktree", "execution", "handoff"} {
		t.Run(kind, func(t *testing.T) {
			e, path, p := quotaGraphFixture(t)
			_, hash, err := supervisor.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := run.AcquireController(p.Repo, hash)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ops := graphOperations(p)
			prepared, err := ops.Prepare(context.Background(), c, p.Tasks[0])
			if err != nil {
				t.Fatal(err)
			}
			argv := append([]string(nil), p.Tasks[0].Launches[prepared.Receipt.Provider]...)
			for i, v := range argv {
				v = strings.ReplaceAll(v, "{model}", prepared.Receipt.Model)
				argv[i] = strings.ReplaceAll(v, "{effort}", prepared.Receipt.Effort)
			}
			d, err := ops.Launch(context.Background(), c, p.Tasks[0], prepared.Receipt, argv)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := ops.Observe(context.Background(), c, p.Tasks[0], d)
			if err != nil || observed.State != supervisor.Quota {
				t.Fatalf("observe %+v %v", observed, err)
			}
			finished, err := ops.Finish(context.Background(), c, p.Tasks[0], d, observed)
			if err != nil {
				t.Fatal(err)
			}
			if err := ops.Resume(context.Background(), c, p.Tasks[0], d, finished); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "worktree":
				os.WriteFile(filepath.Join(e.wt, "src/a.go"), []byte("changed"), 0600)
			case "execution":
				os.WriteFile(graphEvidencePath(d.Attempt, "execution"), []byte("{}"), 0600)
			case "handoff":
				os.WriteFile(graphEvidencePath(d.Attempt, "handoff"), []byte("{}"), 0600)
			}
			// A fresh adapter simulates restart; the old immutable artifact cannot be regenerated.
			if err := graphOperations(p).Resume(context.Background(), c, p.Tasks[0], d, finished); err == nil {
				t.Fatal("changed retry evidence accepted")
			}
		})
	}
}
func TestGraphQuotaWriterOnlyConfiguredLiteralAndNoOverflow(t *testing.T) {
	for _, tc := range []struct {
		chunks []string
		match  bool
	}{{[]string{"HTTP 429 quota exhausted"}, false}, {[]string{"APPROVED quo", "ta marker"}, true}, {[]string{strings.Repeat("x", 1<<20), "approved quota marker"}, false}} {
		var b dispatchReceiptBuffer
		w := &graphQuotaWriter{capture: &b, signals: []string{"approved quota marker"}, matched: make(chan string, 1)}
		for _, chunk := range tc.chunks {
			w.Write([]byte(chunk))
		}
		select {
		case <-w.matched:
			if !tc.match {
				t.Fatal("unexpected quota match")
			}
		default:
			if tc.match {
				t.Fatal("missing split quota match")
			}
		}
	}
	env, err := graphHandoffEnv([]string{"REIN_HANDOFF_PATH=stale", "REIN_HANDOFF_SHA256=stale", "OK=1"}, nil)
	if err != nil || len(env) != 1 || env[0] != "OK=1" {
		t.Fatalf("stale handoff environment survived: %v %v", env, err)
	}
}

func TestGraphQuotaImmediateExitStillRecovers(t *testing.T) {
	_, path, _ := quotaGraphFixture(t, `if [ -z "$REIN_HANDOFF_PATH" ]; then printf 'partial\n' > src/a.go; printf 'approved quota marker\n'; exit 1; fi
 test "$(cat src/a.go)" = partial || exit 4
 printf 'fixed\n' > src/a.go`)
	if out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code != 0 {
		t.Fatalf("quota exit failed: %d %s", code, out)
	}
}

func TestGraphQuotaFailedStopKeepsLeaseAndReservations(t *testing.T) {
	_, path, p := quotaGraphFixture(t)
	_, hash, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ops := graphOperations(p)
	prepared, err := ops.Prepare(context.Background(), c, p.Tasks[0])
	if err != nil {
		t.Fatal(err)
	}
	old := stopGraphQuotaProcess
	stopGraphQuotaProcess = func(*exec.Cmd) error { return errors.New("injected stop failure") }
	defer func() { stopGraphQuotaProcess = old }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	argv := append([]string(nil), p.Tasks[0].Launches[prepared.Receipt.Provider]...)
	for i, v := range argv {
		v = strings.ReplaceAll(v, "{model}", prepared.Receipt.Model)
		argv[i] = strings.ReplaceAll(v, "{effort}", prepared.Receipt.Effort)
	}
	if _, err := ops.Launch(ctx, c, p.Tasks[0], prepared.Receipt, argv); err == nil {
		t.Fatal("failed stop advanced")
	}
	if routing.TaskBusy("task") == "" {
		t.Fatal("failed stop released lease")
	}
	holds, err := budget.HoldsFor(prepared.Attempt)
	if err != nil || len(holds) == 0 {
		t.Fatalf("missing holds %v", err)
	}
	for _, h := range holds {
		if h.State != "reserved" {
			t.Fatalf("failed stop settled hold %+v", h)
		}
	}
	cancel()
}

func TestGraphQuotaPoolsUseQuotaScopeAndSharedCooldownBlocksFallback(t *testing.T) {
	e, _, p := quotaGraphFixture(t)
	if err := validateLocalGraphPlan(p); err != nil {
		t.Fatal(err)
	}
	p.Pools = []string{"agy/credits", "backup/credits"}
	if err := validateLocalGraphPlan(p); err == nil {
		t.Fatal("billing pools accepted as quota scopes")
	}
	cfg, err := providers.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	backup := cfg.Providers["zbackup"]
	backup.Pool = ""
	cfg.Providers["zbackup"] = backup
	raw, _ := json.Marshal(cfg)
	os.WriteFile(e.cfgPath, raw, 0600)
	if err := routing.SetCooldown(e.cfgPath, "w", "observed configured quota signal", nil, false); err != nil {
		t.Fatal(err)
	}
	p.Pools = []string{"antigravity"}
	p.Tasks[0].ConfigDigest = graphHash(raw)
	if err := validateLocalGraphPlan(p); err != nil {
		t.Fatal(err)
	}
	pool, err := routing.QuotaPool(backup)
	if err != nil || pool != "antigravity" {
		t.Fatalf("shared quota pool %s %v", pool, err)
	}
	status, err := routing.Status()
	if err != nil || status[pool].Reason == "" {
		t.Fatal("shared pool bypassed cooldown")
	}
}

func TestGraphRetryChecksCheckpointAfterPrepareAndOnRestart(t *testing.T) {
	e, path, _ := quotaGraphFixture(t)
	p, hash, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ops := graphOperations(p)
	prepare := ops.Prepare
	count := 0
	ops.Prepare = func(ctx context.Context, c *run.Controller, task supervisor.Task) (*routing.AutoResult, error) {
		prepared, err := prepare(ctx, c, task)
		count++
		if err == nil && count == 2 {
			if err := os.WriteFile(filepath.Join(e.wt, "src/a.go"), []byte("tampered after prepare"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return prepared, err
	}
	if _, err := supervisor.RunGraph(context.Background(), p, hash, ops); err == nil || !strings.Contains(err.Error(), "changed since checkpoint") {
		t.Fatalf("mutation after retry prepare error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected one fresh fallback preparation, got %d", count)
	}
	var d routing.Decision
	raw, _ := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	json.Unmarshal(raw, &d)
	if routing.ReceiptUsed(d.Attempt) {
		t.Fatal("rejected fallback receipt consumed")
	}
	if _, err := supervisor.RunGraph(context.Background(), p, hash, graphOperations(p)); err == nil || !strings.Contains(err.Error(), "changed since checkpoint") {
		t.Fatalf("restart prior checkpoint error: %v", err)
	}
	if routing.ReceiptUsed(d.Attempt) {
		t.Fatal("restart consumed rejected fallback receipt")
	}
}
