package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
)

// A Hermes worker launched through Orca runs with the user's own Hermes home, so the task's
// guard plugin is never loaded. That launch must be refused, not allowed to run unguarded.
func TestHermesOrcaLaunchIsRefused(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("REIN_PROFILE", "")
	c := &contract.Contract{Name: "task", Worktree: wt, ReportPath: filepath.Join(root, "report"), Allow: []string{"**"}, Scope: []string{"S1"}}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", wt).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if _, _, err := hooks.Install(c, os.Args[0], []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{"h": {Agent: "hermes", Model: "openrouter:model-id", Launch: "orca", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}}}, WorkerChains: map[string][]string{"backend": {"h"}}}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(root, "providers.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := routing.Prepare(c, "run", "worker:backend", "", cfgPath, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	argv := []string{"orca", "orchestration", "worker-start", "--run", "run", "--agent", "hermes",
		"--model", "openrouter:model-id", "--worktree", "path:" + wt, "--task", "task_opaque"}
	stderr := os.Stderr
	capture, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	os.Stderr = capture
	code := cmdRouteLaunch(append([]string{"--task", "task", "--run", "run", "--"}, argv...))
	os.Stderr = stderr
	b, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatal("Orca Hermes launch accepted: the task's guard plugin would not be loaded")
	}
	if !strings.Contains(string(b), "direct shell route") {
		t.Errorf("Orca Hermes launch refused for the wrong reason: %s", b)
	}
}

// A task home whose dotenv can switch the plugin off must not reach exec.
func TestHermesTaskHomeDotenvIsRefused(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.MkdirAll(filepath.Join(wt, ".hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", ".op.env"} {
		if err := os.WriteFile(filepath.Join(wt, ".hermes", name), []byte("HERMES_SAFE_MODE=1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		err := hooks.CheckHermesTaskHome(wt)
		if err == nil {
			t.Fatalf("%s accepted in the task home", name)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s refusal does not name the file: %v", name, err)
		}
		if err := os.Remove(filepath.Join(wt, ".hermes", name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := hooks.CheckHermesTaskHome(wt); err != nil {
		t.Errorf("clean task home refused: %v", err)
	}
}

// The launch must refuse the task home, not only the helper.
func TestHermesLaunchRefusesTaskHomeDotenv(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("REIN_PROFILE", "")
	c := &contract.Contract{Name: "task", Worktree: wt, ReportPath: filepath.Join(root, "report"), Allow: []string{"**"}, Scope: []string{"S1"}}
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
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$TEST_LAUNCH_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := routing.Prepare(c, "run", "worker:backend", "", cfgPath, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	argv := []string{cli, "--cli", "chat", "--model", "openrouter:model-id", "--query", "prompt", "--oneshot"}
	args := append([]string{"--task", "task", "--run", "run", "--"}, argv...)

	if err := os.MkdirAll(filepath.Join(wt, ".hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".hermes", ".env"), []byte("HERMES_SAFE_MODE=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRouteLaunch(args); code == 0 {
		t.Fatal("launch accepted a task home whose dotenv can disable the guard")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("the provider process started despite the refusal: %v", err)
	}
}

// Installing hooks for another vendor must not hide a worker's own .hermes files from git status.
func TestHermesIsNotADefaultVendor(t *testing.T) {
	for _, v := range hooks.Default {
		if v == "hermes" {
			t.Fatal("hermes must be opt-in: installing it by default hides a worker's .hermes files from git status")
		}
	}
	if !slices.Contains(hooks.All, "hermes") {
		t.Fatal("hermes must remain a known vendor")
	}
}
