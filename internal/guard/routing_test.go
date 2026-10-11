package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

func routedEnv(t *testing.T) (*coordEnv, *contract.Contract) {
	t.Helper()
	e := newCoordEnv(t)
	c := &contract.Contract{Name: "side", Worktree: e.side, Allow: []string{"src/**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(e.outside, "reports", "side.md")}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	e.m.Allowed = []run.Allowance{{Kind: "task", Ref: "side", Reason: "test"}}
	e.save()
	return e, c
}

func TestRoutingLaunchFailsClosed(t *testing.T) {
	e, _ := routedEnv(t)
	for _, flags := range []string{"", " --agent codex --model gpt-test", " --run other --agent codex --model gpt-test", " --run sprint --agent codex", " --run sprint --agent codex --model gpt-test"} {
		out := e.bash("orca orchestration worker-start --worktree path:" + e.side + flags)
		if !isDeny(out) || !strings.Contains(out, "ROUTE") {
			t.Errorf("missing route must deny %q: %s", flags, out)
		}
	}
	// A contracted hook may not bypass routing by spawning from the worker checkout.
	out := e.bash("orca orchestration worker-start --worktree path:"+e.side+" --run sprint --agent codex --model gpt-test", map[string]any{"cwd": e.side})
	if !isDeny(out) || !strings.Contains(out, "ROUTE") {
		t.Errorf("contracted checkout bypass: %s", out)
	}
}

func TestRoutingDirectHarnessLaunch(t *testing.T) {
	e := newCoordEnv(t)
	for _, cmd := range []string{"claude -p fix", "codex exec fix", "agy fix", "kiro-cli chat --tui --trust-all-tools orca", "opencode run fix", "opencode2 run fix", "env codex exec fix", "bash -c 'claude -p fix'", "orca terminal create --command 'codex exec fix'", "orca terminal create --command='kiro-cli chat'"} {
		out := e.bash(cmd)
		if !isDeny(out) || !strings.Contains(out, "rein route launch") {
			t.Errorf("untracked harness %q: %s", cmd, out)
		}
	}
	for _, cmd := range []string{"claude --version", "codex --help", "kiro-cli --version", "opencode models", "claude auth status"} {
		e.expect(false, e.bash(cmd), "read-only probe "+cmd)
	}
}

func TestRoutingProbe(t *testing.T) {
	if os.Getenv("REIN_GUARD_PROBE") == "1" {
		fmt.Println("OK")
		os.Exit(0)
	}
}
func prepareGuardRoute(t *testing.T, c *contract.Contract, runID string) {
	prepareGuardRouteFor(t, c, runID, "codex", "gpt-6.1-sol", "shell", "worker:backend")
}
func prepareGuardRouteFor(t *testing.T, c *contract.Contract, runID, agent, model, launch, chain string) {
	t.Helper()
	c.HooksGeneration = "route-fixture"
	c.HooksInstalled = []string{agent}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{"test": {Agent: agent, Model: model, Launch: launch, Probe: []string{os.Args[0], "-test.run=^TestRoutingProbe$"}}}, WorkerChains: map[string][]string{"backend": {"test"}}, ReviewChains: map[string][]string{"backend": {"test"}}}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "fallback.json")
	writeFile(t, p, string(b))
	t.Setenv("REIN_GUARD_PROBE", "1")
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.csv"))
	if _, err := routing.Prepare(c, runID, chain, "gpt-6.1-sol", p, false, time.Second*5); err != nil {
		t.Fatal(err)
	}
}
func TestRoutingBoundSelection(t *testing.T) {
	e, c := routedEnv(t)
	prepareGuardRouteFor(t, c, e.m.Run, "claude", "claude-sonnet", "orca", "worker:backend")
	start := "rein route launch --task side --run sprint -- orca orchestration worker-start --worktree path:" + e.side + " --run sprint --agent claude --model "
	e.expect(false, e.bash(start+"claude-sonnet"), "prepared matching route")
	e.expect(true, e.bash(start+"gpt-other"), "wrong model")
	e.expect(true, e.bash(start+"claude-sonnet --worktree path:"+e.outside), "duplicate worktree")
	e.expect(true, e.bash(start+"claude-sonnet --model gpt-other"), "duplicate model")
	e.expect(true, e.bash(start+"claude-sonnet --run other"), "duplicate run")
	e.expect(true, e.bash(start+"claude-sonnet --agent codex"), "duplicate agent")
	e.expect(false, e.bash(start+"claude-sonnet", map[string]any{"cwd": e.side}), "matching route in worker checkout")
}

func TestRoutingWrapperPreservesGates(t *testing.T) {
	e, c := routedEnv(t)
	wrapper := "rein route launch --task side --run sprint -- "
	e.expect(true, e.bash(wrapper+"codex --model gpt-6.1-sol exec fix"), "wrapper must have receipt")
	prepareGuardRoute(t, c, e.m.Run)
	e.expect(false, e.bash(wrapper+"codex --model gpt-6.1-sol exec fix"), "valid direct wrapper")
	e.expect(false, e.bash(wrapper+"codex -m gpt-6.1-sol exec fix"), "short model flag")
	e.expect(true, e.bash(wrapper+"codex --model gpt-other exec fix"), "wrapper model mismatch")
	prepareGuardRouteFor(t, c, e.m.Run, "claude", "claude-sonnet", "orca", "worker:backend")
	launch := wrapper + "orca orchestration worker-start --worktree path:" + e.side + " --run sprint --agent claude --model claude-sonnet --task task_opaque-id"
	e.expect(true, e.bash(launch), "wrapper cannot bypass source preflight")
	valid := "# Task\n\n## Scope\n- **S1** Fix launch\n  - red check: wrong model denied\n\n" + c.Snippet() + "\n## Not in scope\n- Other changes\n\nGates: go test ./...\nTimebox: 30m\n"
	source := filepath.Join(e.outside, "wrapped-source.md")
	writeFile(t, source, valid)
	e.expect(false, e.bash("rein spec check "+source+" side && "+launch), "wrapped adjacent source preflight")
	e.m.Allowed = nil
	e.save()
	e.expect(true, e.bash(wrapper+"codex --model gpt-6.1-sol exec fix"), "wrapper cannot bypass scope ruling")
}

func TestRoutingConfiguredBudgetFailsClosed(t *testing.T) {
	e, c := routedEnv(t)
	prepareGuardRoute(t, c, e.m.Run)
	launch := "rein route launch --task side --run sprint -- codex --model gpt-6.1-sol exec fix"
	p := filepath.Join(e.outside, "profile.json")
	t.Setenv("REIN_PROFILE", p)
	e.expect(true, e.bash(launch), "requested unreadable profile")
	writeFile(t, p, `{"budget":{"max_review_rounds":2}}`)
	t.Setenv("PIPELINE_LEDGER", t.TempDir())
	e.expect(true, e.bash(launch), "unreadable budget ledger")
	t.Setenv("PIPELINE_LEDGER", filepath.Join(e.outside, "budget-ledger.jsonl"))
	writeFile(t, os.Getenv("PIPELINE_LEDGER"), `{"kind":"task","run":"sprint","task":"side","review_rounds":2,"recorded_at":"2099-01-01T00:00:00Z"}`+"\n")
	e.expect(true, e.bash(launch), "task review round limit")
}

func TestRoutingContractBudgetWithoutEnvironment(t *testing.T) {
	e, c := routedEnv(t)
	t.Setenv("REIN_PROFILE", "")
	c.Profile.Budget = &contract.Budget{MaxReviewRounds: 2}
	prepareGuardRoute(t, c, e.m.Run)
	writeFile(t, os.Getenv("PIPELINE_LEDGER"), `{"kind":"task","run":"sprint","task":"side","review_rounds":2,"recorded_at":"2099-01-01T00:00:00Z"}`+"\n")
	start := "rein route launch --task side --run sprint -- codex --model gpt-6.1-sol exec fix"
	e.expect(true, e.bash(start), "contract budget remains enforced without profileenv")
	e.expect(true, e.bash("rein route launch --task side --run sprint -- codex --model gpt-6.1-sol exec fix"), "directwrapper contract budget")
}

func TestRoutingRawAndWrongLaunchRejected(t *testing.T) {
	e, c := routedEnv(t)
	prepareGuardRoute(t, c, e.m.Run)
	raw := "orca orchestration worker-start --worktree path:" + e.side + " --run sprint --agent codex --model gpt-6.1-sol"
	e.expect(true, e.bash(raw), "raw Orca bypasses route launch recording")
	e.expect(true, e.bash("codex exec fix", map[string]any{"cwd": e.side}), "contracted direct childspawn bypass")
	e.expect(true, e.bash("rein route launch --task side --run sprint -- "+raw), "shell receipt cannot launch Orca")
	prepareGuardRouteFor(t, c, e.m.Run, "claude", "claude-sonnet", "orca", "review:backend")
	e.expect(true, e.bash("rein route launch --task side --run sprint -- claude --model claude-sonnet -p fix"), "review route cannot launch worker")
}

func TestRoutingNativeOrcaRejectedAndRunCap(t *testing.T) {
	e, c := routedEnv(t)
	prepareGuardRouteFor(t, c, e.m.Run, "codex", "gpt-6.1-sol", "orca", "worker:backend")
	command := "rein route launch --task side --run sprint -- orca orchestration worker-start --worktree path:" + e.side + " --run sprint --agent codex --model gpt-6.1-sol"
	out := e.bash(command)
	if !isDeny(out) || !strings.Contains(out, "not forwarded") {
		t.Errorf("native Orca launch silently ignores flags: %s", out)
	}
	c.Profile.Budget = &contract.Budget{Pools: map[string]contract.PoolCaps{"codex_tokens": {RunCap: 100, TaskCap: 500}}}
	prepareGuardRoute(t, c, e.m.Run)
	writeFile(t, os.Getenv("PIPELINE_LEDGER"), `{"kind":"task","run":"sprint","task":"other","worker":{"agent":"codex","model":"gpt-6.1-sol"},"worker_tokens":200,"recorded_at":"2099-01-01T00:00:00Z"}`+"\n")
	out = e.bash("rein route launch --task side --run sprint -- codex --model gpt-6.1-sol exec fix")
	if !isDeny(out) || !strings.Contains(out, "budget cap") {
		t.Errorf("direct wrapper must enforce run cap across tasks: %s", out)
	}
}
