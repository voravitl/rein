package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

func TestRouteLaunchIdentity(t *testing.T) {
	wt := t.TempDir()
	c := &contract.Contract{Name: "task", Worktree: wt}
	cases := []struct {
		name  string
		argv  []string
		agent string
		valid bool
	}{
		{"codex", []string{"codex", "exec", "-m", "gpt-test", "prompt"}, "codex", true},
		{"kiro", []string{"kiro-cli", "chat", "--model=gpt-test", "prompt"}, "kiro", true},
		{"agy", []string{"agy", "--print", "prompt", "--model", "gpt-test"}, "antigravity", true},
		{"opencode", []string{"opencode2", "run", "-m", "gpt-test", "prompt"}, "opencode2", true},
		{"claude", []string{"claude", "--model", "gpt-test", "-p", "prompt"}, "claude", true},
		{"orca", []string{"orca", "orchestration", "worker-start", "--run", "R", "--worktree", "path:" + wt, "--agent", "codex", "--model", "gpt-test"}, "codex", true},
		{"missing model", []string{"codex", "exec", "prompt"}, "", false},
		{"duplicate model", []string{"codex", "exec", "-m", "gpt-test", "--model", "other"}, "", false},
		{"missing value", []string{"codex", "exec", "--model"}, "", false},
		{"other cwd", []string{"codex", "exec", "-m", "gpt-test", "-C", filepath.Dir(wt)}, "", false},
		{"shell", []string{"sh", "-c", "codex exec -m gpt-test"}, "", false},
		{"other command", []string{"orca", "terminal", "create", "--model", "gpt-test"}, "", false},
		{"missing run", []string{"orca", "orchestration", "worker-start", "--worktree", "path:" + wt, "--agent", "codex", "--model", "gpt-test"}, "", false},
		{"foreign run", []string{"orca", "orchestration", "worker-start", "--run", "other", "--worktree", "path:" + wt, "--agent", "codex", "--model", "gpt-test"}, "", false},
		{"foreign tree", []string{"orca", "orchestration", "worker-start", "--run", "R", "--worktree", "path:" + filepath.Dir(wt), "--agent", "codex", "--model", "gpt-test"}, "", false},
		{"config override", []string{"codex", "exec", "-m", "gpt-test", "-c", "model=other"}, "", false},
		{"profile override", []string{"codex", "exec", "-m", "gpt-test", "--profile", "other"}, "", false},
		{"local provider", []string{"codex", "exec", "-m", "gpt-test", "--oss"}, "", false},
		{"attached config", []string{"codex", "exec", "-m", "gpt-test", "-cmodel_provider=other"}, "", false},
		{"attached profile", []string{"codex", "exec", "-m", "gpt-test", "-pother"}, "", false},
		{"hook override", []string{"codex", "exec", "-m", "gpt-test", "-c", "hooks.PreToolUse=[]"}, "", false},
		{"kiro empty agent", []string{"kiro-cli", "chat", "--model", "gpt-test", "--agent="}, "", false},
		{"attached cwd", []string{"codex", "exec", "-m", "gpt-test", "-C" + filepath.Dir(wt)}, "", false},
		{"attached managed tree", []string{"claude", "--model", "gpt-test", "-wother"}, "", false},
		{"managed tree", []string{"claude", "--model", "gpt-test", "--worktree"}, "", false},
		{"kiro wrong agent", []string{"kiro-cli", "chat", "--model", "gpt-test", "--agent", "other"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, m, err := launchIdentity(tc.argv, c, "R")
			if (err == nil) != tc.valid || (tc.valid && (a != tc.agent || m != "gpt-test")) {
				t.Fatalf("agent=%q model=%q error=%v", a, m, err)
			}
		})
	}
}

func TestRouteLaunchExecutesOnlyPreparedWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake POSIX CLI execution; identity validation is platform independent")
	}
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("REIN_PROFILE", "")
	c := &contract.Contract{Name: "task", Worktree: wt, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(root, "report"), HooksInstalled: []string{"codex", "kiro"}, HooksGeneration: "g"}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	config := providers.Config{Providers: map[string]providers.Provider{
		"codex": {Agent: "codex", Model: "gpt-test", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}},
		"kiro":  {Agent: "kiro", Model: "claude-sonnet", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}},
	}, WorkerChains: map[string][]string{"backend": {"codex"}, "ordinary": {"kiro"}}, ReviewChains: map[string][]string{"backend": {"codex"}}}
	path := filepath.Join(root, "config.json")
	b, _ := json.Marshal(config)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "launch.log")
	t.Setenv("TEST_LAUNCH_LOG", log)
	for _, name := range []string{"codex", "kiro-cli"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$TEST_LAUNCH_LOG\"\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--task", c.Name, "--run", "run", "--", filepath.Join(root, "codex"), "exec", "--model", "gpt-test", "prompt"}
	if cmdRouteLaunch(args) == 0 {
		t.Fatal("launch without route accepted")
	}
	if _, err := routing.Prepare(c, "run", "review:backend", "claude-sonnet", path, false, time.Second); err != nil {
		t.Fatal(err)
	}
	if cmdRouteLaunch(args) == 0 {
		t.Fatal("review route authorized implementation")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("rejected launch executed provider")
	}
	if _, err := routing.Prepare(c, "run", "worker:backend", "", path, false, time.Second); err != nil {
		t.Fatal(err)
	}
	if cmdRouteLaunch(args) != 0 {
		t.Fatal("prepared worker launch failed")
	}
	b, _ = os.ReadFile(log)
	for _, required := range []string{wt, "--dangerously-bypass-hook-trust", "hooks.PreToolUse=", "hooks.Stop=", "--task task", "gpt-test"} {
		if !strings.Contains(string(b), required) {
			t.Fatalf("launched argv missing %q: %s", required, b)
		}
	}
	if _, err := routing.Prepare(c, "run", "worker:ordinary", "", path, false, time.Second); err != nil {
		t.Fatal(err)
	}
	args = []string{"--task", c.Name, "--run", "run", "--", filepath.Join(root, "kiro-cli"), "chat", "--model", "claude-sonnet", "prompt"}
	if cmdRouteLaunch(args) != 0 {
		t.Fatal("prepared Kiro launch failed")
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "--agent\nrein\n") {
		t.Fatalf("Kiro guard agent missing: %s", b)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	launches, exits := 0, 0
	for _, row := range rows {
		if row.Run != "run" || row.Task != "task" {
			t.Fatalf("missing attribution: %+v", row)
		}
		if row.Kind == "routing_launch" {
			launches++
			var note map[string]any
			if err := json.Unmarshal([]byte(row.Notes), &note); err != nil || note["launch"] != "shell" {
				t.Fatalf("incorrect actual launch: %s", row.Notes)
			}
		}
		if row.Kind == "routing_exit" {
			exits++
		}
	}
	if launches != 2 || exits != 2 {
		t.Fatalf("launches=%d exits=%d", launches, exits)
	}
}

func TestRouteBudgetRequiresAllowedTask(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REIN_RUN_DIR", "")
	t.Setenv("REIN_PROFILE", "")
	m := run.Marker{Schema: run.Schema, Run: "R", Root: root, SessionID: "test", PID: os.Getpid()}
	path := filepath.Join(gitDir, run.MarkerFile)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	c := &contract.Contract{Name: "task", Worktree: root}
	if err := routeBudget(c, "R"); err == nil {
		t.Fatal("unapproved task accepted")
	}
	m.Allowed = []run.Allowance{{Kind: "task", Ref: "task", Reason: "test"}}
	b, _ = json.Marshal(m)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := routeBudget(c, "R"); err != nil {
		t.Fatal(err)
	}
}
func TestRouteLaunchRejectsDuplicateScope(t *testing.T) {
	for _, args := range [][]string{
		{"--task", "first", "--task", "second", "--run", "R", "--", "codex", "exec"},
		{"--task", "task", "--run", "first", "--run=second", "--", "codex", "exec"},
	} {
		if code := cmdRouteLaunch(args); code != 2 {
			t.Fatalf("duplicate scope returned %d", code)
		}
	}
}
