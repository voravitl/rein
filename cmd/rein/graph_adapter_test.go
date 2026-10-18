package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func localGraphFixture(t *testing.T, worker, verify string, configure ...func(*e2eEnv, *supervisor.Plan)) (*e2eEnv, string, supervisor.Plan) {
	t.Helper()
	e := newE2E(t, "shell")
	t.Setenv("ORCA_REPO_DIR", "")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = e.wt
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, b, err)
		}
	}
	git("init", "-q")
	os.Mkdir(filepath.Join(e.wt, "src"), 0700)
	os.WriteFile(filepath.Join(e.wt, "src", "a.go"), []byte("baseline\n"), 0600)
	git("add", "src/a.go")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-qm", "baseline")
	binary := filepath.Join(e.root, "agy")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+worker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := supervisor.Plan{Schema: 2, Run: "graph", Repo: e.wt, Terminal: "local", Source: "test-cli", Version: "1", MaxChecks: 4, TimeoutMS: 30000, PollIntervalMS: 1, Pools: []string{"antigravity"}, Tasks: []supervisor.Task{{Task: "task", ProcessPolicy: "non_detaching", ContractHash: e.hash, Profile: e.pj, ConfigPath: e.cfgPath, ConfigDigest: graphHash(cfg), Base: "HEAD", MaxAttempts: 2, Launches: map[string][]string{"w": {binary, "--model", "{model}", "--effort", "{effort}", "--print", "approved task"}}, Verify: [][]string{{"sh", "-c", verify}}}}}
	for _, f := range configure {
		f(e, &p)
	}
	cfg, err = os.ReadFile(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p.Tasks[0].ConfigDigest = graphHash(cfg)
	raw, _ := json.Marshal(p)
	_, hash, err := supervisor.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(e.root, "plan.json")
	os.WriteFile(planPath, raw, 0600)
	if _, err := run.StartSupervised(e.wt, p.Run, nil, run.Owner{SessionID: "test-owner", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Allow(e.wt, "task", "test-approved graph task"); err != nil {
		t.Fatal(err)
	}
	return e, planPath, p
}

func TestGraphCLIActualLocalHarnessRoutingCheckpointVerifyAndRestart(t *testing.T) {
	e, path, _ := localGraphFixture(t, "printf 'fixed\\n' > src/a.go\nprintf 'done\\n'", "test \"$(cat src/a.go)\" = fixed")
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 {
		t.Fatalf("graph exit=%d output=%s", code, out)
	}
	var result supervisor.GraphResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Completed) != 1 || result.Completed[0] != "task" || result.State != routing.WaitHuman {
		t.Fatalf("%+v", result)
	}
	b, err := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d routing.Decision
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if err := routing.VerifyCheckpoint("task", d.Attempt); err != nil {
		t.Fatal(err)
	}
	if busy := routing.TaskBusy("task"); busy != "" {
		t.Fatal(busy)
	}
	holds, err := budget.HoldsFor(d.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range holds {
		if h.State == "reserved" {
			t.Fatalf("hold remained reserved: %+v", h)
		}
	}
	if _, err := os.Stat(graphEvidencePath(d.Attempt, "verification")); err != nil {
		t.Fatal(err)
	}
	out, code = capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 || !strings.Contains(out, "task") {
		t.Fatalf("restart %d %s", code, out)
	}
	b, _ = os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	var after routing.Decision
	json.Unmarshal(b, &after)
	if after.Attempt != d.Attempt {
		t.Fatal("restart launched another attempt")
	}
}

func TestGraphCLIVerifierMutationFailsClosed(t *testing.T) {
	_, path, _ := localGraphFixture(t, "printf 'fixed\\n' > src/a.go", "printf 'unreviewed\\n' > src/a.go")
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code == 0 {
		t.Fatalf("mutating verifier accepted: %s", out)
	}
	var result supervisor.GraphResult
	json.Unmarshal([]byte(out), &result)
	if len(result.Completed) != 0 {
		t.Fatal("mutating verifier unlocked completion")
	}
	if _, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code == 0 {
		t.Fatal("ambiguous verification retried")
	}
}

func TestGraphCLIRejectsUnsupportedPoliciesAndRemoteBeforePrepare(t *testing.T) {
	e, path, p := localGraphFixture(t, "exit 0", "true")
	p.Pools = nil
	raw, _ := json.Marshal(p)
	os.WriteFile(path, raw, 0600)
	if got := cmdSupervisor([]string{"validate", "--plan", path}); got != 2 {
		t.Fatalf("missing pool exit %d", got)
	}
	p.Pools = []string{"agy/credits"}
	for _, policy := range []string{"", "detaching"} {
		p.Tasks[0].ProcessPolicy = policy
		raw, _ := json.Marshal(p)
		os.WriteFile(path, raw, 0600)
		if got := cmdSupervisor([]string{"validate", "--plan", path}); got != 2 {
			t.Fatalf("policy %q exit %d", policy, got)
		}
	}
	p.Tasks[0].ProcessPolicy = "non_detaching"
	p.Tasks[0].Launches["w"] = []string{"orca", "orchestration", "worker-start"}
	raw, _ = json.Marshal(p)
	os.WriteFile(path, raw, 0600)
	if got := cmdSupervisor([]string{"run", "--plan", path}); got != 2 {
		t.Fatalf("remote execution accepted %d", got)
	}
	if _, err := os.Stat(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json")); !os.IsNotExist(err) {
		t.Fatalf("unsupported plan performed preparation: %v", err)
	}
}

func TestGraphCLIRevalidatesCompletedEvidenceOnRestart(t *testing.T) {
	for _, kind := range []string{"worktree", "verification", "checkpoint", "execution"} {
		t.Run(kind, func(t *testing.T) {
			e, path, _ := localGraphFixture(t, "printf 'fixed\\n' > src/a.go", "test \"$(cat src/a.go)\" = fixed")
			if out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code != 0 {
				t.Fatalf("initial %d %s", code, out)
			}
			b, err := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
			if err != nil {
				t.Fatal(err)
			}
			var d routing.Decision
			json.Unmarshal(b, &d)
			switch kind {
			case "execution":
				b, _ := os.ReadFile(graphEvidencePath(d.Attempt, "execution"))
				var e graphExecution
				json.Unmarshal(b, &e)
				e.ExitCode = 17
				writeGraphEvidence(graphEvidencePath(d.Attempt, "execution"), e)
			case "worktree":
				os.WriteFile(filepath.Join(e.wt, "src", "a.go"), []byte("tampered\n"), 0600)
			case "verification":
				os.Remove(graphEvidencePath(d.Attempt, "verification"))
			case "checkpoint":
				os.Remove(filepath.Join(e.root, "contracts", "routes", "checkpoints", "task", d.Attempt+".json"))
			}
			out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
			if code == 0 {
				t.Fatalf("stale completion reused: %s", out)
			}
			var result supervisor.GraphResult
			json.Unmarshal([]byte(out), &result)
			if len(result.Completed) != 0 {
				t.Fatalf("reported stale completion: %+v", result)
			}
		})
	}
}

func TestGraphCLISurvivingDescendantRetainsLeaseAndHold(t *testing.T) {
	_, path, _ := localGraphFixture(t, "sleep 2 >/dev/null 2>&1 &\nexit 0", "true")
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code == 0 {
		t.Fatalf("surviving descendant accepted: %s", out)
	}
	if why := routing.TaskBusy("task"); why == "" {
		t.Fatal("surviving descendant lost task lease")
	}
	holds, err := budget.Outstanding()
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) == 0 {
		t.Fatal("unknown writer released reserved funds")
	}
}

func TestControlledLaunchRejectsDifferentPreparedAttemptBeforeExecution(t *testing.T) {
	e, path, _ := localGraphFixture(t, "printf executed > \"$GRAPH_EXECUTION_SENTINEL\"", "true")
	sentinel := filepath.Join(e.root, "executed")
	t.Setenv("GRAPH_EXECUTION_SENTINEL", sentinel)
	p, hash, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	prepared, err := graphOperations(p).Prepare(context.Background(), control, p.Tasks[0])
	if err != nil {
		t.Fatal(err)
	}
	expected := *prepared.Receipt
	expected.Attempt = "stale-attempt"
	var result graphExecution
	args := []string{"--task", "task", "--run", p.Run, "--", filepath.Join(e.root, "agy"), "--model", prepared.Receipt.Model, "--effort", prepared.Receipt.Effort, "--print", "task"}
	if code := routeLaunch(context.Background(), args, control, &result, &expected); code == 0 {
		t.Fatal("changed receipt accepted")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("rejected receipt executed worker")
	}
	if routing.ReceiptUsed(prepared.Attempt) {
		t.Fatal("rejected receipt consumed execution")
	}
}
