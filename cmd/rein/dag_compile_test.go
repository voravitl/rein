package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/supervisor"
)

func writeTestProvidersConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := providers.Config{
		Providers: map[string]providers.Provider{
			"claude": {
				Agent:  "claude",
				Model:  "claude-opus-5-5",
				Effort: "high",
				Launch: "shell",
				Probe:  []string{"claude", "-v"},
				Billing: &providers.Billing{
					Mode: "subscription",
					Pool: "claude",
					Unit: "subscription_percent",
				},
			},
			"remote-bad": {
				Agent:  "claude",
				Model:  "claude-opus-5-5",
				Launch: "orca",
				Probe:  []string{"claude", "-v"},
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(p, b, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeTestProfile(t *testing.T, dir string, sensitive []string) string {
	t.Helper()
	p := filepath.Join(dir, "profile.json")
	prof := contract.Profile{
		SensitivePaths: sensitive,
	}
	b, err := json.MarshalIndent(prof, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func setupTestGitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	initCmd := exec.Command("git", "init", "-q", dir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.local").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", dir, "config", "core.hooksPath", "").Run()

	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("graphify-out/\n"), 0644)
	f := filepath.Join(dir, "README.md")
	os.WriteFile(f, []byte("# Test Repo\n"), 0644)
	exec.Command("git", "-C", dir, "add", ".").Run()
	exec.Command("git", "-C", dir, "commit", "-q", "-m", "initial commit").Run()

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	base := strings.TrimSpace(string(out))
	return dir, base
}

func captureBoth(t *testing.T, f func() int) (string, int) {
	t.Helper()
	oldOut := os.Stdout
	oldErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	os.Stderr = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	code := f()
	w.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr
	return <-done, code
}

func validCompileSpec(repo, base string) string {
	return fmt.Sprintf(`{
  "schema": "rein_dag_spec/v1",
  "run": "run-test-compile",
  "repo": %q,
  "base": %q,
  "goal": "compile test goal",
  "max_parallel": 2,
  "nodes": [
    {
      "id": "worker1",
      "kind": "worker",
      "title": "worker 1",
      "write": ["internal/**"],
      "depends_on": [],
      "kind_profile": "backend",
      "required_capabilities": ["repository-edit"],
      "expected_context_tokens": 5000,
      "rationale": [{"path": "internal/foo.go", "reason": "implement feature"}],
      "prompt": {
        "task": "task 1",
        "deliverable": "deliverable 1",
        "scope": "scope 1",
        "verify": "verify 1",
        "stop_when": "stop 1"
      },
      "verify": [["/bin/true"]],
      "providers": ["claude"],
      "inputs": [],
      "outputs": []
    },
    {
      "id": "action1",
      "kind": "action",
      "title": "action 1",
      "write": [],
      "depends_on": ["worker1"],
      "prompt": {
        "task": "test action",
        "deliverable": "artifact",
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
}

func TestDagCmdCompileDryRun(t *testing.T) {
	tmp := t.TempDir()
	repo, base := setupTestGitRepo(t)
	specPath := writeDagSpec(t, validCompileSpec(repo, base))
	cfgPath := writeTestProvidersConfig(t, tmp)
	profPath := writeTestProfile(t, tmp, []string{".env", "secret/**"})
	outDir := filepath.Join(tmp, "out-dry")

	args := []string{"compile", "--spec", specPath, "--config", cfgPath, "--profile", profPath, "--out", outDir}
	out, code := captureBoth(t, func() int { return cmdDag(args) })
	if code != 0 {
		t.Fatalf("exit %d, out: %s", code, out)
	}

	planJSONPath := filepath.Join(outDir, "plan.json")
	b, err := os.ReadFile(planJSONPath)
	if err != nil {
		t.Fatalf("failed to read generated plan.json: %v", err)
	}

	var p supervisor.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("invalid json in plan.json: %v", err)
	}
	if p.Schema != 3 || p.Run != "run-test-compile" || p.Execution.MaxParallel != 2 {
		t.Fatalf("unexpected plan fields: %+v", p)
	}
	if len(p.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(p.Tasks))
	}
	if len(p.Pools) != 1 || p.Pools[0] != "claude" {
		t.Fatalf("expected pool claude, got %v", p.Pools)
	}

	planMDPath := filepath.Join(outDir, "plan.md")
	md, err := os.ReadFile(planMDPath)
	if err != nil {
		t.Fatalf("missing plan.md: %v", err)
	}
	// Owner commands must point at the compiled plan in --out, never at <repo>/plan.json,
	// and must authorize every task before the supervisor can launch it.
	wantPlan := filepath.Join(outDir, "plan.json")
	if strings.Contains(string(md), filepath.Join(p.Repo, "plan.json")) {
		t.Fatalf("plan.md points owner commands at repo/plan.json:\n%s", md)
	}
	for _, want := range []string{
		"rein supervisor validate --plan " + wantPlan,
		"--supervisor-plan " + wantPlan,
		"rein supervisor run --plan " + wantPlan,
		"rein dag integrate --plan " + wantPlan,
	} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("plan.md missing %q:\n%s", want, md)
		}
	}
	// A literal <placeholder> pasted into a shell is a redirection.
	block := string(md)[strings.Index(string(md), "```sh"):]
	if strings.ContainsAny(block, "<>") {
		t.Fatalf("plan.md owner commands contain < or >, which a shell parses as redirection:\n%s", block)
	}
	for _, task := range p.Tasks {
		if !strings.Contains(string(md), "rein run allow "+p.Repo+" --task "+task.Task+" ") {
			t.Fatalf("plan.md missing run allow for %s:\n%s", task.Task, md)
		}
	}
}

func TestPlanShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/out/plan.json": "/tmp/out/plan.json",
		"/tmp/my out/p.json": "'/tmp/my out/p.json'",
		"it's":               `'it'\''s'`,
		"$(touch x)":         "'$(touch x)'",
		"":                   "''",
	} {
		if got := planShellQuote(in); got != want {
			t.Errorf("planShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDagCmdCompileApply(t *testing.T) {
	tmp := t.TempDir()
	contractsDir := filepath.Join(tmp, "contracts")
	t.Setenv("PIPELINE_CONTRACTS", contractsDir)

	repo, base := setupTestGitRepo(t)
	specPath := writeDagSpec(t, validCompileSpec(repo, base))
	cfgPath := writeTestProvidersConfig(t, tmp)
	profPath := writeTestProfile(t, tmp, []string{".env", "secret/**"})
	outDir := filepath.Join(tmp, "out-apply")

	args := []string{"compile", "--spec", specPath, "--config", cfgPath, "--profile", profPath, "--out", outDir, "--apply"}
	out, code := captureBoth(t, func() int { return cmdDag(args) })
	if code != 0 {
		t.Fatalf("exit %d, out: %s", code, out)
	}

	planJSONPath := filepath.Join(outDir, "plan.json")
	plan, planHash, err := supervisor.Load(planJSONPath)
	if err != nil {
		t.Fatalf("failed to load and decode plan: %v", err)
	}
	if planHash == "" {
		t.Fatal("empty plan hash")
	}

	// In-process validation directly check
	if err := validateLocalDAGPlan(plan); err != nil {
		t.Fatalf("validateLocalDAGPlan failed on applied plan: %v", err)
	}

	// Check worktrees were created
	for _, task := range plan.Tasks {
		c, err := contract.Load(task.Task)
		if err != nil {
			t.Fatalf("contract.Load(%s) failed: %v", task.Task, err)
		}
		if routing.ContractHash(c) != task.ContractHash {
			t.Fatalf("contract hash mismatch for %s: %s vs %s", task.Task, routing.ContractHash(c), task.ContractHash)
		}
		if fi, err := os.Stat(c.Worktree); err != nil || !fi.IsDir() {
			t.Fatalf("worktree %s not created", c.Worktree)
		}
	}
}

func TestDagCmdCompileRefusals(t *testing.T) {
	tmp := t.TempDir()
	repo, base := setupTestGitRepo(t)
	goodSpec := validCompileSpec(repo, base)
	cfgPath := writeTestProvidersConfig(t, tmp)
	profPath := writeTestProfile(t, tmp, []string{".env"})
	outDir := filepath.Join(tmp, "out-refusals")

	for _, c := range []struct {
		name string
		spec string
		cfg  string
		prof string
		out  string
		want int
		msg  string
	}{
		{
			name: "missing-sensitive-paths",
			spec: writeDagSpec(t, goodSpec),
			cfg:  cfgPath,
			prof: writeTestProfile(t, t.TempDir(), nil),
			out:  outDir,
			want: 1,
			msg:  "sensitive_paths",
		},
		{
			name: "remote-orca-launch-refused",
			spec: writeDagSpec(t, strings.Replace(goodSpec, `"claude"`, `"remote-bad"`, 1)),
			cfg:  cfgPath,
			prof: profPath,
			out:  outDir,
			want: 1,
			msg:  "requires \"shell\"",
		},
		{
			name: "worker-missing-rationale",
			spec: writeDagSpec(t, strings.Replace(goodSpec, `"rationale": [{"path": "internal/foo.go", "reason": "implement feature"}],`, `"rationale": [],`, 1)),
			cfg:  cfgPath,
			prof: profPath,
			out:  outDir,
			want: 1,
			msg:  "rationale",
		},
		{
			name: "rationale-outside-write-globs",
			spec: writeDagSpec(t, strings.Replace(goodSpec, `"internal/foo.go"`, `"outside/foo.go"`, 1)),
			cfg:  cfgPath,
			prof: profPath,
			out:  outDir,
			want: 1,
			msg:  "not covered by write globs",
		},
		{
			name: "action-missing-outputs",
			spec: writeDagSpec(t, strings.Replace(goodSpec, `"outputs": [{"name": "report", "path": "report.json", "max_bytes": 1024}]`, `"outputs": []`, 1)),
			cfg:  cfgPath,
			prof: profPath,
			out:  outDir,
			want: 1,
			msg:  "requires at least one output",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := []string{"compile", "--spec", c.spec, "--config", c.cfg, "--profile", c.prof, "--out", c.out}
			out, code := captureBoth(t, func() int { return cmdDag(args) })
			if code != c.want || !strings.Contains(out, c.msg) {
				t.Fatalf("exit %d, out %q; want %d containing %q", code, out, c.want, c.msg)
			}
		})
	}
}
