package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func TestHermesLaunchIdentityAndLiteralPrompt(t *testing.T) {
	c := &contract.Contract{Worktree: t.TempDir()}
	prompt := "งานที่อนุมัติ\n'\" $(touch should-not-exist) `echo bad`"
	argv := []string{"hermes", "--cli", "chat", "--model", "openrouter:model-id", "--reasoning", "high", "--query", prompt, "--oneshot", "--quiet"}
	agent, model, err := launchIdentity(argv, c, "r")
	if err != nil || agent != "hermes" || model != "openrouter:model-id" {
		t.Fatalf("identity=%s/%s err=%v", agent, model, err)
	}
	bare := append([]string(nil), argv...)
	bare[4] = "model-id"
	if _, _, err := launchIdentity(bare, c, "r"); err == nil {
		t.Fatal("unqualified model accepted")
	}
	qualified := append([]string(nil), argv...)
	qualified[4] = "openrouter:anthropic/claude-sonnet-4"
	if _, model, err := launchIdentity(qualified, c, "r"); err != nil || model != qualified[4] {
		t.Fatalf("provider-qualified identity changed: %q %v", model, err)
	}
	if effort, err := launchEffort(argv); err != nil || effort != "high" {
		t.Fatalf("effort=%q err=%v", effort, err)
	}
	if slot, err := graphPromptSlot(argv); err != nil || argv[slot] != prompt {
		t.Fatalf("slot=%d err=%v", slot, err)
	}
	for _, flags := range [][]string{
		{"--profile", "other"}, {"--profile=other"}, {"-p", "other"}, {"-pother"},
		{"--in", c.Worktree}, {"--worktree"}, {"-w"}, {"--provider", "other"},
		{"--resume", "latest"}, {"-r", "latest"}, {"--continue"}, {"-c"},
		{"--safe-mode"}, {"--ignore-user-config"}, {"--ignore-rules"}, {"--yolo"}, {"--accept-hooks"},
		{"--query-file", "file"}, {"--query", "other"}, {"--reasoning", "low"}, {"--effort", "high"},
		{"--model", "other"}, {"-mother"}, {"--tui"}, {"--cli"}, {"--", "other"},
	} {
		bad := append(append([]string(nil), argv...), flags...)
		if _, _, err := launchIdentity(bad, c, "r"); err == nil {
			t.Errorf("identity accepted %v", flags)
		}
		if _, err := graphPromptSlot(bad); err == nil {
			t.Errorf("graph accepted %v", flags)
		}
	}
	for _, bad := range [][]string{
		{"hermes", "chat", "--model", "openrouter:model-id", "--query", prompt, "--oneshot"},
		{"hermes", "--cli", "chat", "--model", "openrouter:model-id", "--query", prompt},
		{"hermes", "--cli", "chat", "--model", "openrouter:model-id", "--oneshot", prompt},
		{"hermes", "--cli", "chat", "--model", "openrouter:model-id", "--query", "--profile=other", "--oneshot"},
	} {
		if _, _, err := launchIdentity(bad, c, "r"); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestHermesPreparedLaunchPinsEnvironment(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("REIN_PROFILE", "")
	t.Setenv("HERMES_HOME", "/foreign/hermes")
	t.Setenv("HERMES_ENABLE_PROJECT_PLUGINS", "0")
	t.Setenv("HERMES_INFERENCE_PROVIDER", "anthropic")
	c := &contract.Contract{Name: "task", Worktree: wt, ReportPath: filepath.Join(root, "report"), Allow: []string{"**"}, Scope: []string{"S1"}, HooksInstalled: []string{"hermes"}, HooksGeneration: "g"}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", wt).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if _, _, err := hooks.Install(c, os.Args[0], []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{"h": {Agent: "hermes", Model: "openrouter:model-id", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}}}, WorkerChains: map[string][]string{"backend": {"h"}}}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(root, "providers.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cli, log := filepath.Join(root, "hermes"), filepath.Join(root, "actual")
	t.Setenv("TEST_LAUNCH_LOG", log)
	t.Setenv("TEST_REPORT_PATH", c.ReportPath)
	t.Setenv("TEST_WRITE_REPORT", "1")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\000' \"$PWD\" \"$HERMES_HOME\" \"$HERMES_ENABLE_PROJECT_PLUGINS\" \"$ADVISE_TASK\" \"$ADVISE_RUN\" \"$@\" > \"$TEST_LAUNCH_LOG\"\n[ \"$TEST_WRITE_REPORT\" = 1 ] && printf '# S1 PASS\\n' > \"$TEST_REPORT_PATH\"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := routing.Prepare(c, "run", "worker:backend", "", cfgPath, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	argv := []string{cli, "--cli", "chat", "--model", "openrouter:model-id", "--query", "literal '$()' prompt", "--oneshot"}
	args := append([]string{"--task", "task", "--run", "run", "--"}, argv...)
	for _, flags := range [][]string{{"--profile", "other"}, {"--provider", "other"}, {"--resume", "latest"}, {"--safe-mode"}} {
		bad := append(append([]string(nil), args...), flags...)
		if code := cmdRouteLaunch(bad); code == 0 {
			t.Fatalf("override launched: %v", flags)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("override started process: %v", err)
		}
	}
	for _, key := range []string{"HERMES_SAFE_MODE", "HERMES_IGNORE_USER_CONFIG"} {
		t.Setenv(key, "1")
		if code := cmdRouteLaunch(args); code == 0 {
			t.Fatalf("accepted incompatible %s", key)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("%s started process", key)
		}
		t.Setenv(key, "")
	}
	// A zero-exit provider without the required report cannot complete the task.
	t.Setenv("TEST_WRITE_REPORT", "")
	if code := cmdRouteLaunch(args); code == 0 {
		t.Fatal("missing report accepted after provider exited zero")
	}
	// A report left by an earlier attempt is not evidence of this one.
	if err := os.WriteFile(c.ReportPath, []byte("# S1 PASS\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRouteLaunch(args); code == 0 {
		t.Fatal("stale report accepted as this attempt's report")
	}
	t.Setenv("TEST_WRITE_REPORT", "1")
	if code := cmdRouteLaunch(args); code != 0 {
		t.Fatalf("launch=%d", code)
	}
	bound, err := os.ReadFile(filepath.Join(wt, ".hermes/config.yaml"))
	if err != nil || !strings.Contains(string(bound), `home: "/foreign/hermes"`) {
		t.Fatalf("missing credential reference: %s %v", bound, err)
	}
	raw, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	actual := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	want := append([]string{contract.Real(wt), filepath.Join(wt, ".hermes"), "1", "task", "run"}, argv[1:]...)
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("actual=%q want=%q", actual, want)
	}
}

func TestHermesGraphInputPreservesApprovedContext(t *testing.T) {
	e, path, p := localGraphFixture(t, "exit 0", "true")
	_, hash, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	d := &routing.Decision{Run: p.Run, Task: "task", Attempt: "current", Worktree: e.wt, ContractHash: e.hash}
	original := "approved literal\n'\" $(touch should-not-exist)"
	binary := filepath.Join(e.root, "hermes")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	argv := []string{binary, "--cli", "chat", "--model", "openrouter:model-id", "--reasoning", "high", "--query", original, "--oneshot"}
	out, digest, err := graphLaunchInput(control, p, p.Tasks[0], d, argv, graphHandoffRef{})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := graphPromptSlot(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out[slot], original) || !strings.Contains(out[slot], `"attempt":"current"`) || digest != graphHash([]byte(out[slot])) {
		t.Fatalf("bad context %q", out[slot])
	}
	raw, err := exec.Command(out[0], out[1:]...).Output()
	if err != nil {
		t.Fatal(err)
	}
	actual := strings.Split(string(raw), "\x00")
	if actual[slot-1] != out[slot] {
		t.Fatalf("literal graph input changed: %q", actual)
	}
	if _, err := os.Stat(filepath.Join(e.wt, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("query executed as shell text")
	}
	for i := range argv {
		if i != slot && argv[i] != out[i] {
			t.Fatalf("changed argv %d", i)
		}
	}
}

func TestHermesGraphContinuationRevalidatesReceipt(t *testing.T) {
	e, path, p := quotaGraphFixture(t)
	_, hash, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	ops := graphOperations(p)
	prepared, err := ops.Prepare(context.Background(), control, p.Tasks[0])
	if err != nil {
		t.Fatal(err)
	}
	first := append([]string(nil), p.Tasks[0].Launches[prepared.Receipt.Provider]...)
	for i, arg := range first {
		first[i] = strings.ReplaceAll(strings.ReplaceAll(arg, "{model}", prepared.Receipt.Model), "{effort}", prepared.Receipt.Effort)
	}
	previous, err := ops.Launch(context.Background(), control, p.Tasks[0], prepared.Receipt, first)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ops.Observe(context.Background(), control, p.Tasks[0], previous)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := ops.Finish(context.Background(), control, p.Tasks[0], previous, observed)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := prepareGraphHandoff(control, p, p.Tasks[0], previous, outcome)
	if err != nil {
		t.Fatal(err)
	}
	d := &routing.Decision{Run: p.Run, Task: "task", Attempt: "next", Worktree: e.wt, ContractHash: e.hash}
	argv := []string{"hermes", "--cli", "chat", "--model", "openrouter:model-id", "--query", "approved task", "--oneshot"}
	out, _, err := graphLaunchInput(control, p, p.Tasks[0], d, argv, ref)
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := graphPromptSlot(out)
	if !strings.Contains(out[slot], "Verified continuation receipt") || !strings.Contains(out[slot], "src/a.go: unstaged") || !strings.HasSuffix(out[slot], "approved task") {
		t.Fatalf("missing continuation %q", out[slot])
	}
	if err := os.WriteFile(ref.Path, []byte(`{"progress":"tampered"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := graphLaunchInput(control, p, p.Tasks[0], d, argv, ref); err == nil {
		t.Fatal("changed continuation receipt accepted")
	}
	for _, flags := range [][]string{{"--resume", "latest"}, {"--continue"}, {"--query-file", "-"}} {
		bad := append(append([]string(nil), argv...), flags...)
		if _, _, err := graphLaunchInput(control, p, p.Tasks[0], d, bad, graphHandoffRef{}); err == nil {
			t.Fatalf("native continuation accepted %v", flags)
		}
	}
}
