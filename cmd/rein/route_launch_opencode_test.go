package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
)

func TestOpenCodeV2ModelVariantIdentity(t *testing.T) {
	c := &contract.Contract{Worktree: t.TempDir()}
	for _, executable := range []string{"opencode", "opencode2"} {
		for _, selector := range []string{"provider/model#high", "provider/family/model#high"} {
			argv := []string{filepath.Join(t.TempDir(), executable), "run", "--standalone", "-m", selector, "prompt"}
			agent, model, err := launchIdentity(argv, c, "r")
			if err != nil || agent != executable || model != selector[:len(selector)-len("#high")] {
				t.Errorf("%v: identity=%q,%q error=%v", argv, agent, model, err)
			}
			if effort, err := launchEffort(argv); err != nil || effort != "high" {
				t.Errorf("%v: effort=%q error=%v", argv, effort, err)
			}
			if _, err := graphPromptSlot(argv); err != nil {
				t.Errorf("%v: literal input: %v", argv, err)
			}
		}
		for _, selector := range []string{"provider/model#", "provider/model##high", "provider/model#high#low", "#high", "/model#high", "provider/#high", "model#high", "provider/model# high", "provider/model#high\n"} {
			argv := []string{executable, "run", "-m", selector, "prompt"}
			if _, _, err := launchIdentity(argv, c, "r"); err == nil {
				t.Errorf("accepted malformed selector %q", selector)
			}
		}
		for _, flags := range [][]string{{"--variant", "high"}, {"--effort=high"}, {"--config=other"}} {
			argv := append([]string{executable, "run", "-m", "provider/model#high"}, flags...)
			if _, _, err := launchIdentity(argv, c, "r"); err == nil {
				t.Errorf("accepted unsupported flags %v", flags)
			}
			if _, err := launchEffort(argv); err == nil {
				t.Errorf("accepted unsupported effort flags %v", flags)
			}
		}
		for _, flag := range []string{"-c", "--continue"} {
			argv := []string{executable, "run", flag, "-m", "provider/model#high", "prompt"}
			if _, model, err := launchIdentity(argv, c, "r"); err != nil || model != "provider/model" {
				t.Errorf("legacy continue flag changed: model=%q error=%v", model, err)
			}
			if _, err := graphPromptSlot(argv); err == nil {
				t.Errorf("graph accepted native continuation %q", flag)
			}
		}
	}
	// Other harnesses retain their literal model identity, including Orca worker-start.
	for _, argv := range [][]string{{"claude", "-p", "prompt", "-m", "provider/model#high"}, {"orca", "orchestration", "worker-start", "--run", "r", "--worktree", "path:" + c.Worktree, "--agent", "opencode2", "--model", "provider/model#high", "--effort", "high"}} {
		if _, model, err := launchIdentity(argv, c, "r"); err != nil || model != "provider/model#high" {
			t.Fatalf("other harness changed: model=%q err=%v", model, err)
		}
	}
}

func TestOpenCodeV2PreparedVariantLaunch(t *testing.T) {
	for _, executable := range []string{"opencode", "opencode2"} {
		t.Run(executable, func(t *testing.T) {
			e := newE2E(t, "shell")
			e.c.HooksInstalled = []string{"opencode"}
			if _, err := e.c.Save(); err != nil {
				t.Fatal(err)
			}
			var profile routing.TaskProfile
			if err := json.Unmarshal(e.pj, &profile); err != nil {
				t.Fatal(err)
			}
			profile.ContractHash = routing.ContractHash(e.c)
			b, _ := json.Marshal(profile)
			if err := os.WriteFile(e.profile, b, 0600); err != nil {
				t.Fatal(err)
			}
			cli, log := filepath.Join(e.root, executable), filepath.Join(e.root, "launch.log")
			t.Setenv("TEST_LAUNCH_LOG", log)
			body := "#!/bin/sh\ncase \"$1\" in\n--version) echo 2.0.32;;\nmodels) echo openai/gpt-test;;\nrun) printf '%s\\n' \"$PWD\" \"$@\" > \"$TEST_LAUNCH_LOG\";;\n*) exit 2;;\nesac\n"
			if err := os.WriteFile(cli, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			cfg, err := providers.Load(e.cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			w := cfg.Providers["w"]
			w.Agent, w.Model = executable, "openai/gpt-test"
			w.Billing = &providers.Billing{Mode: "subscription", Unit: "subscription_percent", Pool: "opencode/subscription"}
			cfg.Providers["w"] = w
			cfg.Discovery[executable] = providers.DiscoverySpec{Command: []string{cli}}
			b, _ = json.Marshal(cfg)
			if err := os.WriteFile(e.cfgPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				id := fmt.Sprintf("opencode-seed-%d", i)
				launch := ledger.Row{Kind: "routing_launch", Attempt: id, Run: "old", Task: "old", Worker: &ledger.AgentModel{Agent: w.Agent, Model: w.Model}, Type: "backend", Effort: "high", Config: routing.ProviderFingerprint(w), Suite: "s1", Tier: "T1"}
				outcome := ledger.Row{Kind: "task", Attempt: id, Run: "old", Task: "old", Source: "measured", GatesPassed: yes(), Reviewer: &ledger.AgentModel{Agent: "claude", Model: "claude-opus-5-5-high"}, ReviewSHA: "sha", Approved: true, Blockers: zero(), Highs: zero(), FalseClaims: zero(), Drift: zero(), ReviewRounds: one()}
				charge, err := ledger.NewChargeRow(id, "w", "worker", w.Billing.Pool, w.Billing.Unit, w.Agent, w.Model, 1.5)
				if err != nil {
					t.Fatal(err)
				}
				seedRows(t, launch, outcome, charge)
			}
			output, code := capture(t, func() int {
				return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", e.profile, "--config", e.cfgPath, "--timeout", "30s"})
			})
			var result struct{ Attempt string }
			if code != 0 || json.Unmarshal([]byte(output), &result) != nil || result.Attempt == "" {
				t.Fatalf("prepare code=%d output=%s", code, output)
			}
			launch := func(selector string, extra ...string) []string {
				argv := []string{"--task", "task", "--run", "run", "--", cli, "run", "--standalone", "-m", selector}
				return append(append(argv, extra...), "prompt")
			}
			for _, args := range [][]string{launch(w.Model), launch(w.Model + "#low"), launch(w.Model + "#HIGH"), launch(w.Model + "#high#low"), launch(w.Model+"#high", "--variant", "high")} {
				if cmdRouteLaunch(args) == 0 {
					t.Fatalf("rejected launch succeeded: %v", args)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) || routing.ReceiptUsed(result.Attempt) {
					t.Fatalf("rejected launch ran or consumed receipt: stat=%v", err)
				}
			}
			if code := cmdRouteLaunch(launch(w.Model + "#high")); code != 0 {
				t.Fatalf("valid model variant launch failed: %d", code)
			}
			b, err = os.ReadFile(log)
			if err != nil || !strings.Contains(string(b), "-m\n"+w.Model+"#high\nprompt\n") || !routing.ReceiptUsed(result.Attempt) {
				t.Fatalf("actual argv=%s error=%v", b, err)
			}
			rows, err := ledger.LoadStrict("")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.Kind == "routing_launch" && row.Attempt == result.Attempt {
					found = true
					if row.Model != w.Model || row.Worker == nil || row.Worker.Model != w.Model || row.Effort != "high" {
						t.Fatalf("accounting identity changed: %+v", row)
					}
				}
			}
			if !found {
				t.Fatal("missing attributable launch row")
			}
		})
	}
}
