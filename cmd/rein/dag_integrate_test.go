package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

type integrateFixture struct {
	Repo      string
	Base      string
	Marker    string
	Run       string
	PlanPath  string
	PlanHash  string
	Worktree1 string
	Worktree2 string
}

func setupIntegrateFixture(t *testing.T) integrateFixture {
	t.Helper()
	tmp := t.TempDir()

	contractsDir := filepath.Join(tmp, "contracts")
	t.Setenv("PIPELINE_CONTRACTS", contractsDir)

	repo, base := setupTestGitRepo(t)
	cfgPath := writeTestProvidersConfig(t, tmp)
	profPath := writeTestProfile(t, tmp, []string{".env"})

	// Spec with 2 workers and 1 action verification node
	specJSON := fmt.Sprintf(`{
  "schema": "rein_dag_spec/v1",
  "run": "run-integrate",
  "repo": %q,
  "base": %q,
  "goal": "integrate test goal",
  "max_parallel": 2,
  "nodes": [
    {
      "id": "worker1",
      "kind": "worker",
      "title": "worker 1",
      "write": ["pkg/**"],
      "depends_on": [],
      "kind_profile": "backend",
      "required_capabilities": ["repository-edit"],
      "expected_context_tokens": 4000,
      "rationale": [{"path": "pkg/foo.go", "reason": "implement pkg"}],
      "prompt": {
        "task": "task 1",
        "deliverable": "pkg/foo.go",
        "scope": "pkg",
        "verify": "true",
        "stop_when": "done"
      },
      "verify": [["/bin/true"]],
      "providers": ["claude"],
      "inputs": [],
      "outputs": []
    },
    {
      "id": "worker2",
      "kind": "worker",
      "title": "worker 2",
      "write": ["docs/**"],
      "depends_on": ["worker1"],
      "kind_profile": "docs",
      "required_capabilities": ["repository-edit"],
      "expected_context_tokens": 2000,
      "rationale": [{"path": "docs/bar.md", "reason": "document"}],
      "prompt": {
        "task": "task 2",
        "deliverable": "docs/bar.md",
        "scope": "docs",
        "verify": "true",
        "stop_when": "done"
      },
      "verify": [["/bin/sh", "-c", "test -f pkg/foo.go && test -f docs/bar.md"]],
      "providers": ["claude"],
      "inputs": [],
      "outputs": []
    },
    {
      "id": "action1",
      "kind": "action",
      "title": "verify action",
      "write": [],
      "depends_on": ["worker2"],
      "prompt": {
        "task": "verify",
        "deliverable": "report",
        "scope": "test",
        "verify": "true",
        "stop_when": "done"
      },
      "verify": [["/bin/true"]],
      "inputs": [],
      "outputs": [{"name": "report", "path": "report.json", "max_bytes": 1024}],
      "action": {
        "argv": ["/bin/true"],
        "image": "sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af",
        "timeout_ms": 1000
      }
    }
  ]
}`, repo, base)

	specPath := writeDagSpec(t, specJSON)
	outDir := filepath.Join(tmp, "out-compiled")

	// Compile and apply
	args := []string{"compile", "--spec", specPath, "--config", cfgPath, "--profile", profPath, "--out", outDir, "--apply"}
	out, code := captureBoth(t, func() int { return cmdDag(args) })
	if code != 0 {
		t.Fatalf("compile --apply exit %d: %s", code, out)
	}

	planPath := filepath.Join(outDir, "plan.json")
	plan, planHash, err := supervisor.Load(planPath)
	if err != nil {
		t.Fatalf("supervisor.Load: %v", err)
	}

	// Setup run marker in repo
	marker := run.MarkerPath(filepath.Join(repo, ".git"))
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		t.Fatal(err)
	}
	m := run.Marker{
		Run:  plan.Run,
		Root: repo,
	}
	mb, _ := json.Marshal(m)
	if err := os.WriteFile(marker, mb, 0644); err != nil {
		t.Fatal(err)
	}

	c1, err := contract.Load("run-integrate-worker1")
	if err != nil {
		t.Fatalf("contract.Load(run-integrate-worker1): %v", err)
	}
	c2, err := contract.Load("run-integrate-worker2")
	if err != nil {
		t.Fatalf("contract.Load(run-integrate-worker2): %v", err)
	}

	return integrateFixture{
		Repo:      repo,
		Base:      base,
		Marker:    marker,
		Run:       plan.Run,
		PlanPath:  planPath,
		PlanHash:  planHash,
		Worktree1: c1.Worktree,
		Worktree2: c2.Worktree,
	}
}

// writeJournal stubs the supervisor journal reader with recorded terminal
// states, so these tests cover the integration steps (checkpoints, cherry-pick,
// conflicts, refusals) alone. The real journal path is covered by
// TestDagIntegrateAfterRealSupervisorRun. Each entry is a compact
// {"task","attempt","data":{"outcome":{...}}} record.
func writeJournal(t *testing.T, marker, planHash string, entries []string) {
	t.Helper()
	_, _ = marker, planHash
	got := map[string]supervisor.DAGTerminal{}
	for _, raw := range entries {
		var e struct {
			Task    string `json:"task"`
			Attempt string `json:"attempt"`
			Data    struct {
				Outcome struct {
					State            string `json:"state"`
					Reason           string `json:"reason"`
					CheckpointDigest string `json:"checkpoint_digest"`
				} `json:"outcome"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		got[e.Task] = supervisor.DAGTerminal{Attempt: e.Attempt, State: e.Data.Outcome.State,
			Outcome: supervisor.Outcome{Reason: e.Data.Outcome.Reason, CheckpointDigest: e.Data.Outcome.CheckpointDigest}}
	}
	previous := readDAGTerminals
	readDAGTerminals = func(context.Context, supervisor.Plan, string) (map[string]supervisor.DAGTerminal, error) {
		return got, nil
	}
	t.Cleanup(func() { readDAGTerminals = previous })
}

func TestDagCmdIntegrateClean(t *testing.T) {
	fix := setupIntegrateFixture(t)

	// Commit on wt1
	os.MkdirAll(filepath.Join(fix.Worktree1, "pkg"), 0755)
	os.WriteFile(filepath.Join(fix.Worktree1, "pkg", "foo.go"), []byte("package pkg\n"), 0644)
	exec.Command("git", "-C", fix.Worktree1, "add", ".").Run()
	exec.Command("git", "-C", fix.Worktree1, "commit", "-q", "-m", "add foo.go").Run()

	c1, _ := contract.Load("run-integrate-worker1")
	cp1, err := routing.FinalizeCheckpoint("run-integrate-worker1", "attempt-1", fix.Run, fix.Worktree1, fix.Base, nil, routing.ContractHash(c1))
	if err != nil {
		t.Fatalf("FinalizeCheckpoint run-integrate-worker1: %v", err)
	}

	// Commit on wt2
	os.MkdirAll(filepath.Join(fix.Worktree2, "docs"), 0755)
	os.WriteFile(filepath.Join(fix.Worktree2, "docs", "bar.md"), []byte("# Bar Doc\n"), 0644)
	exec.Command("git", "-C", fix.Worktree2, "add", ".").Run()
	exec.Command("git", "-C", fix.Worktree2, "commit", "-q", "-m", "add bar.md").Run()

	c2, _ := contract.Load("run-integrate-worker2")
	cp2, err := routing.FinalizeCheckpoint("run-integrate-worker2", "attempt-1", fix.Run, fix.Worktree2, fix.Base, nil, routing.ContractHash(c2))
	if err != nil {
		t.Fatalf("FinalizeCheckpoint run-integrate-worker2: %v", err)
	}

	// Write Journal with done status for workers and action1
	jEntries := []string{
		fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp1.Digest),
		fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker2","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp2.Digest),
		`{"phase":"result","operation":"terminal","task":"run-integrate-action1","attempt":"attempt-1","data":{"outcome":{"state":"done"}}}`,
	}
	writeJournal(t, fix.Marker, fix.PlanHash, jEntries)

	targetWT := filepath.Join(t.TempDir(), "target-clean")
	out, code := captureBoth(t, func() int {
		return cmdDag([]string{"integrate", "--plan", fix.PlanPath, "--branch", "feature-integrated", "--worktree", targetWT})
	})
	if code != 0 {
		t.Fatalf("exit %d, out: %s", code, out)
	}
	if !strings.Contains(out, "integrated 3 task(s)") || !strings.Contains(out, "goal verification passed") {
		t.Fatalf("unexpected output: %s", out)
	}

	// Verify target branch exists in repo and has both files
	if _, err := os.Stat(filepath.Join(targetWT, "pkg", "foo.go")); err != nil {
		t.Errorf("pkg/foo.go missing in integrated branch")
	}
	if _, err := os.Stat(filepath.Join(targetWT, "docs", "bar.md")); err != nil {
		t.Errorf("docs/bar.md missing in integrated branch")
	}
}

func TestDagCmdIntegrateConflict(t *testing.T) {
	fix := setupIntegrateFixture(t)

	// Both wt1 and wt2 edit README.md with conflicting lines
	os.WriteFile(filepath.Join(fix.Worktree1, "README.md"), []byte("# Version AAA\n"), 0644)
	exec.Command("git", "-C", fix.Worktree1, "add", ".").Run()
	exec.Command("git", "-C", fix.Worktree1, "commit", "-q", "-m", "conflict AAA").Run()
	c1, _ := contract.Load("run-integrate-worker1")
	cp1, err := routing.FinalizeCheckpoint("run-integrate-worker1", "attempt-1", fix.Run, fix.Worktree1, fix.Base, nil, routing.ContractHash(c1))
	if err != nil {
		t.Fatalf("FinalizeCheckpoint wt1: %v", err)
	}

	os.WriteFile(filepath.Join(fix.Worktree2, "README.md"), []byte("# Version BBB\n"), 0644)
	exec.Command("git", "-C", fix.Worktree2, "add", ".").Run()
	exec.Command("git", "-C", fix.Worktree2, "commit", "-q", "-m", "conflict BBB").Run()
	c2, _ := contract.Load("run-integrate-worker2")
	cp2, err := routing.FinalizeCheckpoint("run-integrate-worker2", "attempt-1", fix.Run, fix.Worktree2, fix.Base, nil, routing.ContractHash(c2))
	if err != nil {
		t.Fatalf("FinalizeCheckpoint wt2: %v", err)
	}

	jEntries := []string{
		fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp1.Digest),
		fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker2","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp2.Digest),
		`{"phase":"result","operation":"terminal","task":"run-integrate-action1","attempt":"attempt-1","data":{"outcome":{"state":"done"}}}`,
	}
	writeJournal(t, fix.Marker, fix.PlanHash, jEntries)

	targetWT := filepath.Join(t.TempDir(), "target-conflict")
	out, code := captureBoth(t, func() int {
		return cmdDag([]string{"integrate", "--plan", fix.PlanPath, "--branch", "feature-conflict", "--worktree", targetWT})
	})
	if code != 1 {
		t.Fatalf("expected exit 1 on conflict, got %d, out: %s", code, out)
	}
	if !strings.Contains(out, "cherry-pick conflict") {
		t.Fatalf("expected conflict error, got: %s", out)
	}
}

func TestDagCmdIntegrateRefusals(t *testing.T) {
	fix := setupIntegrateFixture(t)

	// Commit on wt1
	os.MkdirAll(filepath.Join(fix.Worktree1, "pkg"), 0755)
	os.WriteFile(filepath.Join(fix.Worktree1, "pkg", "foo.go"), []byte("package pkg\n"), 0644)
	exec.Command("git", "-C", fix.Worktree1, "add", ".").Run()
	exec.Command("git", "-C", fix.Worktree1, "commit", "-q", "-m", "add foo.go").Run()
	c1, _ := contract.Load("run-integrate-worker1")
	cp1, err := routing.FinalizeCheckpoint("run-integrate-worker1", "attempt-1", fix.Run, fix.Worktree1, fix.Base, nil, routing.ContractHash(c1))
	if err != nil {
		t.Fatalf("FinalizeCheckpoint wt1: %v", err)
	}

	// Commit on wt2
	os.MkdirAll(filepath.Join(fix.Worktree2, "docs"), 0755)
	os.WriteFile(filepath.Join(fix.Worktree2, "docs", "bar.md"), []byte("# Bar Doc\n"), 0644)
	exec.Command("git", "-C", fix.Worktree2, "add", ".").Run()
	exec.Command("git", "-C", fix.Worktree2, "commit", "-q", "-m", "add bar.md").Run()
	c2, _ := contract.Load("run-integrate-worker2")
	cp2, err := routing.FinalizeCheckpoint("run-integrate-worker2", "attempt-1", fix.Run, fix.Worktree2, fix.Base, nil, routing.ContractHash(c2))
	if err != nil {
		t.Fatalf("FinalizeCheckpoint wt2: %v", err)
	}

	for _, c := range []struct {
		name     string
		mutate   func()
		restore  func()
		wantCode int
		wantMsg  string
	}{
		{
			name: "node-not-done",
			mutate: func() {
				writeJournal(t, fix.Marker, fix.PlanHash, []string{
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"failed","reason":"test failed"}}}`),
				})
			},
			restore:  func() {},
			wantCode: 1,
			wantMsg:  "not done",
		},
		{
			name: "checkpoint-digest-mismatch",
			mutate: func() {
				writeJournal(t, fix.Marker, fix.PlanHash, []string{
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":"wrong-digest-12345"}}}`),
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker2","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp2.Digest),
					`{"phase":"result","operation":"terminal","task":"run-integrate-action1","attempt":"attempt-1","data":{"outcome":{"state":"done"}}}`,
				})
			},
			restore:  func() {},
			wantCode: 1,
			wantMsg:  "checkpoint digest mismatch",
		},
		{
			name: "dirty-worktree",
			mutate: func() {
				writeJournal(t, fix.Marker, fix.PlanHash, []string{
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp1.Digest),
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker2","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp2.Digest),
					`{"phase":"result","operation":"terminal","task":"run-integrate-action1","attempt":"attempt-1","data":{"outcome":{"state":"done"}}}`,
				})
				// Make wt1 dirty
				os.WriteFile(filepath.Join(fix.Worktree1, "untracked.txt"), []byte("stray"), 0644)
			},
			restore: func() {
				os.Remove(filepath.Join(fix.Worktree1, "untracked.txt"))
			},
			wantCode: 1,
			wantMsg:  "worktree is dirty",
		},
		{
			name: "branch-already-exists",
			mutate: func() {
				writeJournal(t, fix.Marker, fix.PlanHash, []string{
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp1.Digest),
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker2","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp2.Digest),
					`{"phase":"result","operation":"terminal","task":"run-integrate-action1","attempt":"attempt-1","data":{"outcome":{"state":"done"}}}`,
				})
				exec.Command("git", "-C", fix.Repo, "branch", "existing-branch").Run()
			},
			restore: func() {
				exec.Command("git", "-C", fix.Repo, "branch", "-D", "existing-branch").Run()
			},
			wantCode: 1,
			wantMsg:  "already exists",
		},
		{
			name: "invalid-branch-name",
			mutate: func() {
				writeJournal(t, fix.Marker, fix.PlanHash, []string{
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker1","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp1.Digest),
					fmt.Sprintf(`{"phase":"result","operation":"terminal","task":"run-integrate-worker2","attempt":"attempt-1","data":{"outcome":{"state":"done","checkpoint_digest":%q}}}`, cp2.Digest),
					`{"phase":"result","operation":"terminal","task":"run-integrate-action1","attempt":"attempt-1","data":{"outcome":{"state":"done"}}}`,
				})
			},
			restore:  func() {},
			wantCode: 1,
			wantMsg:  "invalid branch name",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.mutate()
			defer c.restore()

			branch := "branch-" + c.name
			if c.name == "branch-already-exists" {
				branch = "existing-branch"
			} else if c.name == "invalid-branch-name" {
				branch = "bad..branch"
			}
			out, code := captureBoth(t, func() int {
				return cmdDag([]string{"integrate", "--plan", fix.PlanPath, "--branch", branch})
			})
			if code != c.wantCode || !strings.Contains(out, c.wantMsg) {
				t.Fatalf("exit %d, out %q; want %d containing %q", code, out, c.wantCode, c.wantMsg)
			}
		})
	}
}
