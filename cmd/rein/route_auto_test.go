package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

// TestRouteDiscoverProcess is the fake `agy`: --version and `models`, selected by REIN_FAKE_DISCOVER.
func TestRouteDiscoverProcess(t *testing.T) {
	if os.Getenv("REIN_FAKE_DISCOVER") == "" {
		return
	}
	last := os.Args[len(os.Args)-1]
	kiro := os.Getenv("REIN_FAKE_DISCOVER") == "kiro" || strings.Contains(strings.Join(os.Args, " "), "-- kiro")
	switch {
	case last == "--version" && kiro:
		fmt.Println("kiro-cli 2.21.0")
	case last == "--version":
		fmt.Println("1.3.2")
	case last == "json" && kiro:
		fmt.Print(`{"models":[{"model_id":"claude-opus-5-5-high","context_window_tokens":200000,"rate_multiplier":1.0,"rate_unit":"Credit"}]}`)
	case last == "models":
		fmt.Print("gemini-w-high\tGemini W (High)\n")
	}
	os.Exit(0)
}

func capture(t *testing.T, f func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	code := f()
	w.Close()
	os.Stdout = old
	return <-done, code
}

func seedRows(t *testing.T, rows ...ledger.Row) {
	t.Helper()
	f, err := os.OpenFile(ledger.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range rows {
		if r.RecordedAt == "" {
			r.RecordedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		}
		b, _ := json.Marshal(r)
		fmt.Fprintf(f, "%s\n", b)
	}
}

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }
func zero() *int { v := 0; return &v }
func one() *int  { v := 1; return &v }

// e2eEnv is a project with one worker (agy), one reviewer (kiro), real discovery adapters against fake CLIs, evidence for both and a
// task profile bound to the contract.
type e2eEnv struct {
	root, wt, cfgPath, profile, hash string
	pj                               []byte
	c                                *contract.Contract
	w, r                             providers.Provider
}

func newE2E(t *testing.T, workerLaunch string) *e2eEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake POSIX CLI")
	}
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("PIPELINE_PRICES", filepath.Join(root, "prices.json"))
	t.Setenv("REIN_PROFILE", "")
	t.Setenv("REIN_RUN_DIR", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("REIN_FAKE_DISCOVER", "1")

	sel := &contract.Selection{Objective: "balanced", AcceptFreshness: []string{"client_catalog"}, InventoryMaxAgeMinutes: 60, Suite: "s1",
		Worker:   contract.WorkerPolicy{QualityFloor: contract.QualityFloor{MinApprovalRate: 0.5, MaxFalseClaims: zero()}, MinCompleteAttempts: 5, MaxEvidenceAgeDays: 30},
		Reviewer: contract.ReviewerPolicy{MinDefectFixtures: 4, MinCleanFixtures: 4, MinRecall: 0.4, MinSpecificity: 0.4, MaxFalsePositiveRate: func() *float64 { v := 0.5; return &v }(), MaxEvidenceAgeDays: 30}}
	c := &contract.Contract{Name: "task", Worktree: wt, Allow: []string{"src/**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(root, "report"), HooksInstalled: []string{"agy"},
		HooksGeneration: "g", MaxChangedLines: 50, Profile: contract.Profile{Selection: sel, SensitivePaths: []string{"infra/**"}}}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	prov := func(model string, pool string) providers.Provider {
		return providers.Provider{Agent: "antigravity", Model: model, Launch: workerLaunch, Effort: "high", Capabilities: []string{"repository-edit", "tests"}, ContextTokens: 200000,
			Billing: &providers.Billing{Mode: "credits", Unit: "agy_credits", Pool: pool}, ProbeBound: &providers.Bound{Calls: 1, Amount: 0.5},
			Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}}
	}
	reviewer := prov("claude-opus-5-5-high", "kiro/credits") // reviews run on codex, kiro or claude only
	reviewer.Agent, reviewer.Effort, reviewer.Launch = "kiro", "", "shell"
	reviewer.Billing = &providers.Billing{Mode: "credits", Unit: "kiro_credits", Pool: "kiro/credits"}
	cfg := providers.Config{Providers: map[string]providers.Provider{"w": prov("gemini-w-high", "agy/credits"), "r": reviewer},
		WorkerChains: map[string][]string{"backend": {"w"}}, ReviewChains: map[string][]string{"all": {"r"}},
		Discovery: map[string]providers.DiscoverySpec{
			"antigravity": {Command: []string{os.Args[0], "-test.run=^TestRouteDiscoverProcess$", "--"}},
			"kiro":        {Command: []string{os.Args[0], "-test.run=^TestRouteDiscoverProcess$", "--", "kiro"}}}}
	cfgPath := filepath.Join(root, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}

	// evidence: five verified attempts of the worker with attributed cost, and the reviewer's calibration on frozen fixtures
	w, r := cfg.Providers["w"], cfg.Providers["r"]
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("seed-%d", i)
		launch := ledger.Row{Kind: "routing_launch", Attempt: id, Run: "old", Task: "old", Worker: &ledger.AgentModel{Agent: w.Agent, Model: w.Model}, Type: "backend", Effort: "high", Config: routing.ProviderFingerprint(w), Suite: "s1", Tier: "T1"}
		out := ledger.Row{Kind: "task", Attempt: id, Run: "old", Task: "old", Source: "measured", GatesPassed: yes(), Reviewer: &ledger.AgentModel{Agent: "claude", Model: "claude-opus-5-5-high"}, ReviewSHA: "sha", Approved: true,
			Blockers: zero(), Highs: zero(), FalseClaims: zero(), Drift: zero(), ReviewRounds: one()}
		ch, _ := ledger.NewChargeRow(id, "w", "worker", "agy/credits", "agy_credits", "antigravity", w.Model, 1.5)
		seedRows(t, launch, out, ch)
	}
	for i := 0; i < 4; i++ {
		for _, class := range []string{"defect", "clean"} {
			row := ledger.Row{Kind: "fixture", Fixture: fmt.Sprintf("%s%d", class, i), FixtureClass: class, Reviewer: &ledger.AgentModel{Agent: r.Agent, Model: r.Model}, Type: "backend", Effort: r.Effort,
				Config: routing.ProviderFingerprint(r), Suite: "s1", Tier: "T1", Adjudicated: yes()}
			if class == "defect" {
				row.Detected = yes()
			} else {
				row.FalsePositive = no()
			}
			seedRows(t, row)
		}
		rc, _ := ledger.NewChargeRow(fmt.Sprintf("rv%d", i), "rv", "review", "kiro/credits", "kiro_credits", "r", r.Model, 1)
		rc.Effort, rc.Config = r.Effort, routing.ProviderFingerprint(r)
		seedRows(t, rc)
	}

	// contract hash, then a task profile bound to it
	out, code := capture(t, func() int { return cmdContract([]string{"hash", "task"}) })
	hash := strings.TrimSpace(out)
	if code != 0 || hash != routing.ContractHash(c) {
		t.Fatalf("contract hash: %q %d", out, code)
	}
	profile := filepath.Join(root, "profile.json")
	pj, _ := json.Marshal(routing.TaskProfile{ContractHash: hash, Phase: "worker", Kind: "backend", RequiredCapabilities: []string{"tests"}, ExpectedContext: 20000,
		PlannedFiles: []string{"src/a.go"}, Rationale: []routing.Rationale{{Path: "src/a.go", Reason: "adds a"}}})
	if err := os.WriteFile(profile, pj, 0o600); err != nil {
		t.Fatal(err)
	}
	return &e2eEnv{root: root, wt: wt, cfgPath: cfgPath, profile: profile, hash: hash, pj: pj, c: c, w: w, r: r}
}

func TestRouteAutoLaunchAndSettleEndToEnd(t *testing.T) {
	e := newE2E(t, "shell")
	root, wt, cfgPath, profile, hash, pj, c, w, r := e.root, e.wt, e.cfgPath, e.profile, e.hash, e.pj, e.c, e.w, e.r
	_, _, _ = hash, c, r
	var out string
	var code int
	base := []string{"auto", "--task", "task", "--run", "run", "--profile-file", profile, "--config", cfgPath, "--timeout", "30s"}

	// with a policy configured, a hand-picked chain cannot bypass the scored ordering
	if code := cmdRoute([]string{"prepare", "--task", "task", "--run", "run", "--chain", "worker:backend", "--config", cfgPath, "--timeout", "5s"}); code != 1 {
		t.Fatalf("route prepare beside a selection policy: %d", code)
	}

	// a profile that names a model is refused before anything runs
	bad := filepath.Join(root, "bad.json")
	os.WriteFile(bad, []byte(strings.Replace(string(pj), "{", `{"model":"gemini-w-high",`, 1)), 0o600)
	if _, code = capture(t, func() int {
		return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", bad, "--config", cfgPath})
	}); code != 1 {
		t.Fatalf("coordinator-chosen model accepted: %d", code)
	}

	out, code = capture(t, func() int { return cmdRoute(base) })
	if code != 0 {
		t.Fatalf("route auto failed (%d):\n%s", code, out)
	}
	var res struct {
		Attempt string `json:"attempt"`
		Launch  struct {
			Provider, Agent, Model, Effort string
			Route                          string `json:"launch"`
		} `json:"launch"`
		Receipt struct {
			Attempt string `json:"attempt"`
			Hold    string `json:"hold"`
		} `json:"receipt"`
		SelectionMode string `json:"selection_mode"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Launch.Agent != "antigravity" || res.Launch.Model != "gemini-w-high" || res.Launch.Effort != "high" || res.Launch.Route != "shell" || res.SelectionMode != "scored" || res.Receipt.Attempt != res.Attempt {
		t.Fatalf("%+v", res)
	}

	// the launch wrapper runs exactly the prepared model AND effort, under a task lease, and records attributable rows
	agy := filepath.Join(root, "agy")
	logf := filepath.Join(root, "launch.log")
	t.Setenv("TEST_LAUNCH_LOG", logf)
	if err := os.WriteFile(agy, []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$TEST_LAUNCH_LOG\"\nsleep \"${TEST_LAUNCH_SLEEP:-0}\"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	launch := func(effort ...string) []string {
		a := []string{"--task", "task", "--run", "run", "--", agy, "--model", "gemini-w-high"}
		return append(append(a, effort...), "--print", "prompt")
	}
	for name, args := range map[string][]string{"no effort": launch(), "wrong effort": launch("--effort", "low"), "conflicting": launch("--effort", "high", "--variant", "low")} {
		if code := cmdRouteLaunch(args); code == 0 {
			t.Errorf("%s: launch accepted", name)
		}
	}
	if _, err := os.Stat(logf); !os.IsNotExist(err) {
		t.Fatal("a rejected launch executed the provider")
	}
	if routing.ReceiptUsed(res.Attempt) {
		t.Fatal("a rejected launch consumed the receipt")
	}
	if code := cmdRouteLaunch(launch("--effort", "HIGH")); code != 0 {
		t.Fatalf("launch with the prepared effort failed: %d", code)
	}
	if b, _ := os.ReadFile(logf); !strings.Contains(string(b), "--effort\nHIGH") || !strings.Contains(string(b), wt) {
		t.Fatalf("launched argv: %s", b)
	}
	// a receipt buys ONE execution: the same attempt cannot be launched again, whatever happened to the first run
	os.Remove(logf)
	if code := cmdRouteLaunch(launch("--effort", "high")); code == 0 {
		t.Error("the same receipt launched twice")
	}
	if _, err := os.Stat(logf); !os.IsNotExist(err) {
		t.Fatal("a second launch of a used receipt executed the provider")
	}

	// outcome and charges: cohort fields come from the launch record, never from the caller
	if _, code = capture(t, func() int {
		return cmdLedger([]string{"add", "--attempt", res.Attempt, "--rounds", "1", "--approved", "--gates-passed", "--review-sha", "abc123", "--reviewer", "kiro:claude-opus-5-5-high",
			"--blockers", "0", "--highs", "0", "--false-claims", "0", "--drift", "0", "--minutes", "9"})
	}); code != 0 {
		t.Fatalf("ledger add --attempt: %d", code)
	}
	if code := cmdLedger([]string{"add", "--attempt", res.Attempt, "--rounds", "1", "--worker", "codex:gpt-other"}); code == 0 {
		t.Error("an outcome disagreeing with its launch was accepted")
	}
	if code := cmdLedger([]string{"add", "--attempt", "no-such-attempt", "--rounds", "1"}); code == 0 {
		t.Error("an outcome for an unknown attempt was accepted")
	}
	if _, code = capture(t, func() int {
		return cmdLedger([]string{"charge", "--attempt", res.Attempt, "--component", "worker", "--pool", "agy/credits", "--unit", "agy_credits", "--amount", "2.5", "--charge-id", "c1", "--model", "gemini-w-high", "--config", cfgPath})
	}); code != 0 {
		t.Fatalf("ledger charge: %d", code)
	}
	for _, bad := range [][]string{
		{"charge", "--attempt", res.Attempt, "--pool", "agy/credits", "--unit", "dollars", "--amount", "1"},
		{"charge", "--attempt", res.Attempt, "--pool", "agy/credits", "--unit", "agy_credits", "--amount", "-1"},
		{"charge", "--attempt", res.Attempt, "--pool", "agy/credits", "--price-key", "missing", "--in", "10", "--out", "10"},
		{"charge", "--attempt", "unknown", "--pool", "p", "--unit", "usd", "--amount", "1"},
		// a pool keeps the unit its provider bills in; a review charge must say which reviewer it prices
		{"charge", "--attempt", res.Attempt, "--pool", "agy/credits", "--unit", "usd", "--amount", "1", "--config", cfgPath},
		{"charge", "--attempt", res.Attempt, "--component", "review", "--pool", "kiro/credits", "--unit", "kiro_credits", "--amount", "1", "--config", cfgPath},
	} {
		if _, code := capture(t, func() int { return cmdLedger(bad) }); code == 0 {
			t.Errorf("accepted %v", bad)
		}
	}
	rows, _ := ledger.LoadStrict("")
	var outcome *ledger.Row
	launches, exits := 0, 0
	for i := range rows {
		row := rows[i]
		if row.Attempt != res.Attempt {
			continue
		}
		switch row.Kind {
		case "task":
			outcome = &rows[i]
		case "routing_launch":
			launches++
			if row.Worker == nil || row.Worker.Model != "gemini-w-high" || row.Type != "backend" || row.Effort != "high" || row.Config != routing.ProviderFingerprint(w) || row.Suite != "s1" || row.Tier != "T1" || row.Pid == nil || row.PidStart == nil {
				t.Errorf("launch row lacks cohort fields: %+v", row)
			}
		case "routing_exit":
			exits++
		}
	}
	if launches != 1 || exits != 1 {
		t.Errorf("one receipt, one execution: launches=%d exits=%d", launches, exits)
	}
	if outcome == nil || outcome.Worker.Model != "gemini-w-high" || outcome.Effort != "high" || outcome.Suite != "s1" || outcome.Tier != "T1" || outcome.Type != "backend" || outcome.GatesPassed == nil || !*outcome.GatesPassed || outcome.Run != "run" {
		t.Fatalf("outcome row: %+v", outcome)
	}

	// the hold stays reserved until the attempt is settled; settling charges the winning probe beside the recorded worker usage
	holds, _ := budget.Outstanding()
	if len(holds) != 1 || holds[0].ID != res.Receipt.Hold {
		t.Fatalf("holds %+v", holds)
	}
	out, code = capture(t, func() int { return cmdRoute([]string{"settle", "--attempt", res.Attempt}) })
	if code != 0 || !strings.Contains(out, `"settled"`) || !strings.Contains(out, res.Receipt.Hold) {
		t.Fatalf("settle: %d %s", code, out)
	}
	if holds, _ = budget.Outstanding(); len(holds) != 0 {
		t.Errorf("settled holds: %+v", holds)
	}
	out, _ = capture(t, func() int { return cmdRoute([]string{"holds"}) })
	if strings.TrimSpace(out) != "null" && strings.TrimSpace(out) != "[]" {
		t.Errorf("holds: %s", out)
	}

	// two writers on one checkout: with a run in flight, a freshly prepared receipt is refused (and stays unused)
	t.Setenv("TEST_LAUNCH_SLEEP", "2")
	prepare := func() string {
		out, code := capture(t, func() int { return cmdRoute(base) })
		var r struct {
			Attempt string `json:"attempt"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &r) != nil || r.Attempt == "" {
			t.Fatalf("route auto (%d):\n%s", code, out)
		}
		return r.Attempt
	}
	second := prepare()
	var wg sync.WaitGroup
	var firstCode int
	wg.Add(1)
	go func() { defer wg.Done(); firstCode = cmdRouteLaunch(launch("--effort", "high")) }()
	time.Sleep(700 * time.Millisecond)
	third := prepare() // the first run is in flight: it is not yet evidence, so the model still qualifies
	if code := cmdRouteLaunch(launch("--effort", "high")); code == 0 {
		t.Error("a second writer was admitted beside a running one")
	}
	if routing.ReceiptUsed(third) {
		t.Error("a launch refused for the running writer consumed its receipt")
	}
	wg.Wait()
	if firstCode != 0 || !routing.ReceiptUsed(second) {
		t.Errorf("the first writer: code %d, receipt used %v", firstCode, routing.ReceiptUsed(second))
	}
	t.Setenv("TEST_LAUNCH_SLEEP", "0")

	// a review: its tier follows the real git diff, `route check` binds the effort and spends the receipt once
	if _, err := exec.LookPath("git"); err == nil {
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", wt, "-c", "core.hooksPath=/dev/null", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q", "-b", "main")
		git("commit", "-q", "--allow-empty", "-m", "base")
		git("update-ref", "refs/remotes/origin/main", "HEAD")
		if err := os.MkdirAll(filepath.Join(wt, "src"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("package a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rp := filepath.Join(root, "review.json")
		rj, _ := json.Marshal(routing.TaskProfile{ContractHash: hash, Phase: "review", Kind: "backend", RequiredCapabilities: []string{"tests"}, ExpectedContext: 20000,
			PlannedFiles: []string{"src/a.go"}, ChangedFiles: []string{"src/a.go"}, Rationale: []routing.Rationale{{Path: "src/a.go", Reason: "adds a"}}})
		if err := os.WriteFile(rp, rj, 0o600); err != nil {
			t.Fatal(err)
		}
		out, code := capture(t, func() int {
			return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", rp, "--config", cfgPath, "--base", "origin/main", "--timeout", "30s"})
		})
		var rev struct {
			Launch struct{ Agent, Model, Effort string } `json:"launch"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &rev) != nil || rev.Launch.Agent != "kiro" || rev.Launch.Model != "claude-opus-5-5-high" {
			t.Fatalf("review route auto (%d):\n%s", code, out)
		}
		check := func(effort string) int {
			args := []string{"check", "--task", "task", "--run", "run", "--agent", "kiro", "--model", "claude-opus-5-5-high", "--phase", "review", "--worktree", wt}
			if effort != "" {
				args = append(args, "--effort", effort)
			}
			_, code := capture(t, func() int { return cmdRoute(args) })
			return code
		}
		if code := check("high"); code == 0 { // the review was chosen on evidence for no effort at all
			t.Error("a review at an effort the receipt was not chosen on was admitted")
		}
		if code := check(""); code != 0 {
			t.Fatalf("route check with the prepared effort: %d", code)
		}
		if code := check(""); code == 0 {
			t.Error("one review receipt admitted a second review")
		}
		// a base that does not exist: the tier cannot follow an unknown diff
		if _, code := capture(t, func() int {
			return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", rp, "--config", cfgPath, "--base", "origin/nope", "--timeout", "30s"})
		}); code == 0 {
			t.Error("a review with an unknowable diff was prepared")
		}
	}

	// reconcile is for the user's terminal only
	t.Setenv("CLAUDE_CODE_SESSION_ID", "x")
	if code := cmdRoute([]string{"reconcile", "--hold", "h-1", "--reason", "r"}); code != 2 {
		t.Errorf("reconcile under Claude Code: %d", code)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if code := cmdRoute([]string{"reconcile", "--hold", "h-unknown", "--reason", "r"}); code != 1 {
		t.Errorf("unknown hold: %d", code)
	}
	if code := cmdRoute([]string{"reconcile", "--hold", "h-1"}); code != 2 {
		t.Errorf("reconcile without a reason: %d", code)
	}

	// discovery on its own
	out, code = capture(t, func() int { return cmdRoute([]string{"discover", "--config", cfgPath, "--timeout", "20s"}) })
	if code != 0 || !strings.Contains(out, "antigravity") || !strings.Contains(out, "complete") {
		t.Errorf("discover: %d %s", code, out)
	}
}

func TestLedgerFixtureAndPricedCharge(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("PIPELINE_PRICES", filepath.Join(root, "prices.json"))
	t.Setenv("REIN_PROFILE", "")
	cfg := providers.Config{Providers: map[string]providers.Provider{"r": {Agent: "claude", Model: "claude-opus-x", Launch: "shell", Effort: "high", Probe: []string{"x"}}}}
	cfgPath := filepath.Join(root, "config.json")
	b, _ := json.Marshal(cfg)
	os.WriteFile(cfgPath, b, 0o600)
	fx := func(extra ...string) []string {
		return append([]string{"fixture", "--provider", "r", "--config", cfgPath, "--fixture", "f1", "--type", "backend", "--suite", "s1"}, extra...)
	}
	for _, bad := range [][]string{
		fx("--class", "defect"), fx("--class", "defect", "--detected", "--missed"), fx("--class", "clean", "--detected"), fx("--class", "weird", "--detected"),
		{"fixture", "--provider", "nope", "--config", cfgPath, "--fixture", "f", "--type", "backend", "--suite", "s", "--class", "clean", "--clean-ok"},
		{"fixture", "--provider", "r", "--config", cfgPath, "--fixture", "f", "--type", "backend", "--class", "clean", "--clean-ok"}, // no suite and no policy to take it from
		fx("--class", "clean", "--clean-ok", "--tier", "T9"),
	} {
		if _, code := capture(t, func() int { return cmdLedger(bad) }); code == 0 {
			t.Errorf("accepted %v", bad)
		}
	}
	if _, code := capture(t, func() int { return cmdLedger(fx("--class", "defect", "--detected", "--adjudicated")) }); code != 0 {
		t.Fatal(code)
	}
	if _, code := capture(t, func() int { return cmdLedger(fx("--class", "clean", "--clean-ok")) }); code != 0 { // not adjudicated: recorded, but counts for nothing
		t.Fatal(code)
	}
	rows, _ := ledger.LoadStrict("")
	if len(rows) != 2 || rows[0].Adjudicated == nil || !*rows[0].Adjudicated || rows[1].Adjudicated != nil || rows[0].Config != routing.ProviderFingerprint(cfg.Providers["r"]) || rows[0].Reviewer.Model != "claude-opus-x" {
		t.Fatalf("%+v", rows)
	}

	// API usage is priced from provenance-bearing rates, never from a blended legacy price
	rates := `{"m":{"input_per_mtok":2,"output_per_mtok":10,"source":"https://example.com/p","as_of":"2026-01-01T00:00:00Z","valid_until":"2099-01-01T00:00:00Z"},"legacy":{"usd_per_mtok":5}}`
	os.WriteFile(os.Getenv("PIPELINE_PRICES"), []byte(rates), 0o600)
	seedRows(t, ledger.Row{Kind: "routing_launch", Attempt: "a1", Run: "r1", Task: "t1", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-x"}, Type: "backend"})
	charge := func(extra ...string) []string {
		return append([]string{"charge", "--attempt", "a1", "--pool", "openai/api", "--component", "worker"}, extra...)
	}
	if _, code := capture(t, func() int {
		return cmdLedger(charge("--price-key", "m", "--in", "1000000", "--out", "500000", "--charge-id", "p1"))
	}); code != 0 {
		t.Fatal(code)
	}
	for _, bad := range [][]string{charge("--price-key", "legacy", "--in", "10", "--out", "10"), charge("--price-key", "m", "--in", "10", "--out", "10", "--amount", "1"), charge("--price-key", "m", "--unit", "kiro_credits", "--in", "1", "--out", "1")} {
		if _, code := capture(t, func() int { return cmdLedger(bad) }); code == 0 {
			t.Errorf("accepted %v", bad)
		}
	}
	rows, _ = ledger.LoadStrict("")
	last := rows[len(rows)-1]
	if last.Kind != "cost" || last.Unit != "usd" || last.Amount == nil || *last.Amount != 7 || last.Run != "r1" || last.Task != "t1" || !strings.Contains(last.Notes, "example.com") {
		t.Errorf("priced charge: %+v", last)
	}
	// an expired rate card is refreshed from its source, not extended
	os.WriteFile(os.Getenv("PIPELINE_PRICES"), []byte(`{"m":{"input_per_mtok":2,"output_per_mtok":10,"source":"s","as_of":"2026-01-01T00:00:00Z","valid_until":"2026-02-01T00:00:00Z"}}`), 0o600)
	if _, code := capture(t, func() int { return cmdLedger(charge("--price-key", "m", "--in", "10", "--out", "10")) }); code == 0 {
		t.Error("expired rates priced a charge")
	}
	// the same charge recorded twice stays in the append-only ledger twice; evidence and spend count it once (see ledger and budget tests)
	for i := 0; i < 2; i++ {
		capture(t, func() int { return cmdLedger(charge("--unit", "usd", "--amount", "1", "--charge-id", "dup")) })
	}
	n := 0
	for _, r := range mustLoad(t) {
		if r.ChargeID == "dup" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("expected both records of the retried charge, got %d", n)
	}
}

func mustLoad(t *testing.T) []ledger.Row {
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Reconcile releases reserved funds. The hold's owner is the already-exited `route auto`, so its exit proves nothing about the
// worker: a task whose checkout still has a writer keeps its holds.
func TestReconcileRefusesWhileTheTaskHasAWriter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	h, err := budget.Reserve("", nil, budget.ReserveRequest{Run: "run", Task: "busy", Attempt: "at-1", Items: []budget.Item{{Pool: "p", Unit: "tokens", Amount: 1, Kind: "funding"}}})
	if err != nil {
		t.Fatal(err)
	}
	stderr := func(f func() int) (string, int) {
		old := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		code := f()
		w.Close()
		os.Stderr = old
		return <-done, code
	}
	// without a lease the refusal is the owner's (this process is alive)
	msg, code := stderr(func() int { return cmdRoute([]string{"reconcile", "--hold", h.ID, "--reason", "test"}) })
	if code != 1 || !strings.Contains(msg, "still running") {
		t.Fatalf("no writer, live owner: %d %q", code, msg)
	}
	// with a live writer on the task the refusal is the writer's, whoever owned the hold
	lease := filepath.Join(root, "contracts", "routes", "leases")
	if err := os.MkdirAll(lease, 0o700); err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	start, _ := run.ProcStart(me)
	rec, _ := json.Marshal(routing.Lease{Task: "busy", Attempt: "at-1", Pid: me, PidStart: start})
	if err := os.WriteFile(filepath.Join(lease, "busy.json"), rec, 0o600); err != nil {
		t.Fatal(err)
	}
	msg, code = stderr(func() int { return cmdRoute([]string{"reconcile", "--hold", h.ID, "--reason", "test"}) })
	if code != 1 || !strings.Contains(msg, "still has a writer") {
		t.Fatalf("a live writer must keep the hold: %d %q", code, msg)
	}
	if holds, _ := budget.Outstanding(); len(holds) != 1 {
		t.Errorf("the hold must stay reserved: %+v", holds)
	}
}

// worker-start returns once an Orca worker STARTED: the launcher cannot vouch for the worker afterwards, so the task stays held
// until the attempt is settled, and the exit row claims no duration.
func TestOrcaDispatchedWorkerHoldsTheTaskUntilSettle(t *testing.T) {
	e := newE2E(t, "orca")
	root, wt := e.root, e.wt
	out, code := capture(t, func() int {
		return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", e.profile, "--config", e.cfgPath, "--timeout", "30s"})
	})
	var res struct {
		Attempt string `json:"attempt"`
		Launch  struct {
			Route string `json:"launch"`
		} `json:"launch"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || res.Launch.Route != "orca" {
		t.Fatalf("route auto (%d):\n%s", code, out)
	}
	orca := filepath.Join(root, "orca")
	if err := os.WriteFile(orca, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--task", "task", "--run", "run", "--", orca, "orchestration", "worker-start", "--agent", "antigravity", "--run", "run",
		"--worktree", "path:" + wt, "--model", "gemini-w-high", "--effort", "high"}
	if code := cmdRouteLaunch(args); code != 0 {
		t.Fatalf("orca dispatch: %d", code)
	}
	// the launcher is gone but the worker is not: the task is still held, and nothing can start a second writer
	if why := routing.TaskBusy("task"); !strings.Contains(why, "no recorded end") {
		t.Fatalf("the dispatched worker must keep the task: %q", why)
	}
	rows, _ := ledger.LoadStrict("")
	for _, row := range rows {
		if row.Kind == "routing_exit" && row.Attempt == res.Attempt && row.Minutes != nil {
			t.Errorf("the exit row of a dispatch claims the worker ran %.2f minutes", *row.Minutes)
		}
	}
	// reconcile cannot release funds under a running worker either
	holds, _ := budget.Outstanding()
	if len(holds) != 1 {
		t.Fatalf("holds %+v", holds)
	}
	if code := cmdRoute([]string{"reconcile", "--hold", holds[0].ID, "--reason", "test"}); code != 1 || routing.TaskBusy("task") == "" {
		t.Errorf("reconcile under a dispatched worker: %d", code)
	}
	if _, code := capture(t, func() int { return cmdRoute([]string{"settle", "--attempt", res.Attempt}) }); code != 0 {
		t.Fatalf("settle: %d", code)
	}
	if why := routing.TaskBusy("task"); why != "" {
		t.Errorf("settling ends the attempt and frees the task: %q", why)
	}
}
