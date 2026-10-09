package main

import (
	"encoding/json"
	"fmt"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRouteRejectsIncompleteInvocation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"check", "--task", "missing"}, {"cooldown", "--config", "missing", "--provider", "x"}, {"clear", "--config", "missing"}, {"prepare", "--extra"}} {
		if got := cmdRoute(args); got == 0 {
			t.Fatal("accepted", args)
		}
	}
}
func TestRouteStatusFailsClosed(t *testing.T) {
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", d)
	if got := cmdRoute([]string{"status"}); got != 0 {
		t.Fatal(got)
	}
	if err := os.Mkdir(d+"/routes", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d+"/routes/cooldowns.json", []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := cmdRoute([]string{"status"}); got == 0 {
		t.Fatal("accepted malformed state")
	}
}

func TestRouteProbeProcess(t *testing.T) {
	if len(os.Args) > 2 && os.Args[len(os.Args)-1] == "route-probe" {
		fmt.Println("OK")
		os.Exit(0)
	}
}
func TestRouteCheckPhaseAndWorktree(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "contracts")
	wt := filepath.Join(root, "worktree")
	os.MkdirAll(index, 0700)
	os.MkdirAll(wt, 0700)
	t.Setenv("PIPELINE_CONTRACTS", index)
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	c := &contract.Contract{Name: "task", Worktree: wt, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(root, "report"), HooksInstalled: []string{"codex"}, HooksGeneration: "g"}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(contract.PathOf(c.Name), b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{"codex": {Agent: "codex", Model: "gpt-test", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}}}, ReviewChains: map[string][]string{"backend": {"codex"}}}
	path := filepath.Join(root, "config.json")
	b, _ = json.Marshal(cfg)
	os.WriteFile(path, b, 0600)
	if _, err := routing.Prepare(c, "run", "review:backend", "claude-sonnet", path, false, time.Second); err != nil {
		t.Fatal(err)
	}
	base := []string{"check", "--task", "task", "--run", "run", "--agent", "codex", "--model", "gpt-test"}
	if got := cmdRoute(base); got == 0 {
		t.Fatal("review receipt authorized worker")
	}
	if got := cmdRoute(append(append([]string{}, base...), "--phase", "review", "--worktree", root)); got == 0 {
		t.Fatal("wrong directory accepted")
	}
	if got := cmdRoute(append(append([]string{}, base...), "--phase", "unknown")); got == 0 {
		t.Fatal("unknown phase accepted")
	}
	if got := cmdRoute(append(append([]string{}, base...), "--phase", "review", "--worktree", wt)); got != 0 {
		t.Fatal("valid review rejected", got)
	}
}
