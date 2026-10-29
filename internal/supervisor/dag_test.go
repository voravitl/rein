package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

func dagActionTask(name, hash, base string) Task {
	return Task{Task: name, Kind: "action", ContractHash: hash, Base: base, MaxAttempts: 1, Verify: [][]string{{"/bin/true"}}, Action: &ActionSpec{Argv: []string{"/bin/true"}, TimeoutMS: 1000, MaxOutputBytes: 4096, MaxOutputFiles: 10, MaxOutputTotalBytes: 4096, Sandbox: ActionSandbox{Backend: "docker", Image: "sha256:" + strings.Repeat("a", 64)}}}
}
func dagFixture(t *testing.T, parallel int, dependencies map[string][]string) (Plan, string) {
	t.Helper()
	old, _ := fixture(t)
	loc, err := run.Locate(old.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = old.Repo
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(b))
	ct, err := contract.Load("task")
	if err != nil {
		t.Fatal(err)
	}
	p := old
	p.Schema = 3
	p.MaxChecks = 100
	p.TimeoutMS = 5000
	p.Execution = &ExecutionPolicy{parallel, "isolate_known"}
	p.Tasks = nil
	// Stable ordering makes replay and completed/blocked identities deterministic.
	for _, name := range []string{"A", "B", "C", "D", "E"} {
		deps, ok := dependencies[name]
		if !ok {
			continue
		}
		copy := *ct
		copy.Name = name
		raw, _ := json.Marshal(copy)
		if err := os.WriteFile(contract.PathOf(name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		task := dagActionTask(name, routing.ContractHash(&copy), base)
		task.DependsOn = deps
		p.Tasks = append(p.Tasks, task)
	}
	raw, _ := json.Marshal(p)
	p, hash, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartSupervised(p.Repo, p.Run, nil, run.Owner{SessionID: "dag-owner", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	return p, hash
}
func fakeDAGOps(t *testing.T, p Plan) DAGOperations {
	t.Helper()
	store := t.TempDir()
	source := t.TempDir()
	return DAGOperations{
		Authority: func(context.Context, *run.Controller) error { return nil },
		Prepare: func(ctx context.Context, c *run.Controller, task Task, inputs []ResolvedInput, prior *PreparedAttempt, terminal *TerminalResult) (PreparedAttempt, error) {
			ct, err := contract.Load(task.Task)
			if err != nil {
				return PreparedAttempt{}, err
			}
			attempt, err := NewDAGID()
			if err != nil {
				return PreparedAttempt{}, err
			}
			receipt, err := NewDAGID()
			if err != nil {
				return PreparedAttempt{}, err
			}
			number := 1
			if prior != nil {
				number = prior.Number + 1
			}
			a := PreparedAttempt{Number: number, Run: p.Run, PlanHash: digestBytes(p.raw), Task: task.Task, Kind: task.Kind, Attempt: attempt, ReceiptID: receipt, ContractHash: task.ContractHash, Worktree: ct.Worktree, SourceRevision: task.Base, SourceDigest: digestText("source"), InputDigest: InputDigest(inputs), Inputs: inputs, Argv: task.Action.Argv}
			a.Action = &ActionReceipt{"fixture-daemon", 1, a.Run, a.PlanHash, a.Task, a.Attempt, a.ReceiptID, a.ContractHash, a.SourceRevision, a.SourceDigest, a.InputDigest, digestBytes(mustJSON(task.Action.Argv)), digestBytes(mustJSON(task.Action.Sandbox))}
			return a, nil
		},
		Execute: func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
			return ExecutionResult{Dispatch: Dispatch{Task: task.Task, Attempt: a.Attempt, Decision: a.ReceiptID, Dispatch: a.ReceiptID, Worktree: a.Worktree, OrcaTask: "fixture:" + a.Attempt}, Observation: Observation{Completed, digestText("executed " + a.Attempt)}}, nil
		},
		Finalize: func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt, e ExecutionResult) (TerminalResult, error) {
			out := Outcome{ExecutionDigest: e.Observation.Evidence, CheckpointDigest: digestText("checkpoint " + a.Attempt), VerificationDigest: digestText("verified " + a.Attempt)}
			if e.Cancelled {
				return TerminalResult{Outcome: Outcome{ExecutionDigest: out.ExecutionDigest, CheckpointDigest: out.CheckpointDigest, State: routing.WaitHuman, Reason: "cancelled"}, Cancelled: true}, nil
			}
			if e.Observation.State == Failed {
				out.State = routing.WaitHuman
				out.Reason = "known fixture failure"
				return TerminalResult{Outcome: out}, nil
			}
			for _, o := range task.Outputs {
				file := filepath.Join(source, o.Path)
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					return TerminalResult{}, err
				}
				if err := os.WriteFile(file, []byte(task.Task), 0600); err != nil {
					return TerminalResult{}, err
				}
			}
			m := ArtifactManifest{Version: 1, Run: a.Run, PlanHash: a.PlanHash, Task: a.Task, Attempt: a.Attempt, Receipt: a.ReceiptID, ContractHash: a.ContractHash, SourceRevision: a.SourceRevision, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest, ExecutionDigest: out.ExecutionDigest, VerificationDigest: out.VerificationDigest, CheckpointDigest: out.CheckpointDigest}
			ref, err := PublishArtifacts(store, source, m, task.Outputs)
			return TerminalResult{Outcome: out, Artifacts: &ref}, err
		},
		Revalidate: func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt, terminal TerminalResult) error {
			if terminal.Artifacts != nil {
				return VerifyArtifacts(store, *terminal.Artifacts)
			}
			return nil
		},
	}
}

func rebindDAGFixture(t *testing.T, p Plan) (Plan, string) {
	t.Helper()
	loc, err := run.Locate(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	p, hash, err := Decode(mustJSON(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartSupervised(p.Repo, p.Run, nil, run.Owner{SessionID: "dag-rebound", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	return p, hash
}
func TestDAGFanInVerifiedArtifactBindings(t *testing.T) {
	p, _ := dagFixture(t, 2, map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B", "C"}})
	for i := range p.Tasks {
		p.Tasks[i].Outputs = []ArtifactOutput{{"result", "result", 64}}
	}
	p.Tasks[1].Inputs = []ArtifactInput{{"A", "result", "a"}}
	p.Tasks[2].Inputs = []ArtifactInput{{"A", "result", "a"}}
	p.Tasks[3].Inputs = []ArtifactInput{{"B", "result", "b"}, {"C", "result", "c"}}
	p, h := rebindDAGFixture(t, p)
	ops := fakeDAGOps(t, p)
	prepare := ops.Prepare
	ops.Prepare = func(ctx context.Context, c *run.Controller, task Task, inputs []ResolvedInput, prior *PreparedAttempt, terminal *TerminalResult) (PreparedAttempt, error) {
		for _, in := range inputs {
			m := in.Artifact.Manifest
			if m.Run != p.Run || m.PlanHash != h || m.Task != in.Producer || len(m.Outputs) != 1 || m.Outputs[0].SHA256 != digestText(in.Producer) {
				return PreparedAttempt{}, errors.New("fan-in did not receive verified exact predecessor bytes")
			}
		}
		return prepare(ctx, c, task, inputs, prior, terminal)
	}
	out, err := RunDAG(context.Background(), p, h, ops)
	if err != nil || out.Status != "verified" || len(out.Completed) != 4 {
		t.Fatalf("fan-in %+v %v", out, err)
	}
}
func TestDAGRetryFreshIdentityAndBudget(t *testing.T) {
	p, _ := dagFixture(t, 1, map[string][]string{"A": {}})
	p.Tasks[0].Action.Idempotent = true
	p.Tasks[0].MaxAttempts = 2
	p, h := rebindDAGFixture(t, p)
	ops := fakeDAGOps(t, p)
	execute := ops.Execute
	finalize := ops.Finalize
	attempts := []string{}
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		r, err := execute(ctx, c, task, a)
		if a.Number == 1 {
			r.Observation.State = Failed
		}
		return r, err
	}
	ops.Finalize = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt, e ExecutionResult) (TerminalResult, error) {
		attempts = append(attempts, a.Attempt)
		r, err := finalize(ctx, c, task, a, e)
		if a.Number == 1 {
			r.Retry = true
		}
		return r, err
	}
	out, err := RunDAG(context.Background(), p, h, ops)
	if err != nil || out.Checks != 2 || out.Status != "verified" || len(attempts) != 2 || attempts[0] == attempts[1] {
		t.Fatalf("retry %+v attempts=%v %v", out, attempts, err)
	}
}
func TestDAGUnknownExecutionIsGlobalAndDrains(t *testing.T) {
	p, h := dagFixture(t, 2, map[string][]string{"A": {}, "B": {}, "C": {"A"}})
	ops := fakeDAGOps(t, p)
	execute := ops.Execute
	finalize := ops.Finalize
	var drained atomic.Bool
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		switch task.Task {
		case "A":
			return ExecutionResult{}, errors.New("dispatch acknowledgement lost")
		case "B":
			<-ctx.Done()
			r, err := execute(ctx, c, task, a)
			r.Cancelled = true
			return r, err
		default:
			return ExecutionResult{}, errors.New("descendant of unknown dispatch launched")
		}
	}
	ops.Finalize = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt, e ExecutionResult) (TerminalResult, error) {
		if ctx.Err() != nil {
			return TerminalResult{}, errors.New("cleanup context stale")
		}
		drained.Store(true)
		return finalize(ctx, c, task, a, e)
	}
	out, err := RunDAG(context.Background(), p, h, ops)
	if err == nil || out.State != routing.WaitBlockedReconciliation || !drained.Load() {
		t.Fatalf("global uncertainty %+v drained=%v %v", out, drained.Load(), err)
	}
	if _, err := RunDAG(context.Background(), p, h, ops); err == nil {
		t.Fatal("unknown dispatch restarted")
	}
}
func TestDAGInvocationExactJournalIdentity(t *testing.T) {
	p, h := dagFixture(t, 1, map[string][]string{"A": {}})
	ops := fakeDAGOps(t, p)
	ops.InvocationID = digestText("dag invocation")
	out, err := RunDAG(context.Background(), p, h, ops)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := ReadGraphInvocation(p, h, ops.InvocationID)
	if err != nil || invocation.Return == nil || invocation.Return.PlanHash != h || invocation.Return.Result.Status != out.Status {
		t.Fatalf("invocation %+v %v", invocation, err)
	}
	if _, err := RunDAG(context.Background(), p, h, ops); err == nil {
		t.Fatal("invocation identity reused")
	}
}

func TestDAGPlanValidation(t *testing.T) {
	p, _ := dagFixture(t, 2, map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B", "C"}})
	cases := map[string]func(*Plan){
		"policy missing":          func(p *Plan) { p.Execution = nil },
		"zero parallel":           func(p *Plan) { p.Execution.MaxParallel = 0 },
		"excess parallel":         func(p *Plan) { p.Execution.MaxParallel = 33 },
		"failure policy":          func(p *Plan) { p.Execution.FailurePolicy = "continue_all" },
		"missing kind":            func(p *Plan) { p.Tasks[0].Kind = "" },
		"action routing":          func(p *Plan) { p.Tasks[0].ConfigPath = "/provider.json" },
		"floating image":          func(p *Plan) { p.Tasks[0].Action.Sandbox.Image = "golang:1.26-bookworm" },
		"floating source":         func(p *Plan) { p.Tasks[0].Base = "HEAD" },
		"retry not idempotent":    func(p *Plan) { p.Tasks[0].MaxAttempts = 2 },
		"timeout":                 func(p *Plan) { p.Tasks[0].Action.TimeoutMS = p.TimeoutMS + 1 },
		"output bound":            func(p *Plan) { p.Tasks[0].Action.MaxOutputBytes = 1<<20 + 1 },
		"missing dep":             func(p *Plan) { p.Tasks[0].DependsOn = []string{"missing"} },
		"cycle":                   func(p *Plan) { p.Tasks[0].DependsOn = []string{"D"} },
		"duplicate dep":           func(p *Plan) { p.Tasks[1].DependsOn = []string{"A", "A"} },
		"traversal":               func(p *Plan) { p.Tasks[0].Outputs = []ArtifactOutput{{"bad", "../escape", 1}} },
		"backslash":               func(p *Plan) { p.Tasks[0].Outputs = []ArtifactOutput{{"bad", "a\\b", 1}} },
		"missing producer output": func(p *Plan) { p.Tasks[1].Inputs = []ArtifactInput{{"A", "unknown", "result"}} },
		"indirect input":          func(p *Plan) { p.Tasks[3].Inputs = []ArtifactInput{{"A", "unknown", "result"}} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var clone Plan
			if err := json.Unmarshal(mustJSON(p), &clone); err != nil {
				t.Fatal(err)
			}
			change(&clone)
			if _, _, err := Decode(mustJSON(clone)); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	raw := mustJSON(p)
	for _, bad := range [][]byte{append([]byte(`{"schema":3,`), raw[1:]...), []byte(strings.Replace(string(raw), `"max_parallel":2`, `"max_parallel":2,"unknown":1`, 1))} {
		if _, _, err := Decode(bad); err == nil {
			t.Fatal("duplicate/unknown field accepted")
		}
	}
}
func TestDAGLegacyFieldRejectionAndOmission(t *testing.T) {
	p, _ := graphFixture(t, nil)
	raw := mustJSON(p)
	for _, key := range []string{"execution", "kind", "action", "inputs", "outputs"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("legacy encoding includes %s", key)
		}
	}
	for _, value := range []string{"null", `{"max_parallel":1,"failure_policy":"isolate_known"}`} {
		bad := strings.TrimSuffix(string(raw), "}") + `,"execution":` + value + `}`
		if _, _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("schema 2 accepted execution extension")
		}
	}
}
func TestDAGParallelRendezvousAndBound(t *testing.T) {
	p, h := dagFixture(t, 2, map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B", "C"}})
	ops := fakeDAGOps(t, p)
	execute := ops.Execute
	ready := make(chan string, 2)
	release := make(chan struct{})
	var live, peak atomic.Int32
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		n := live.Add(1)
		defer live.Add(-1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		if task.Task == "B" || task.Task == "C" {
			ready <- task.Task
			select {
			case <-release:
			case <-ctx.Done():
				return ExecutionResult{}, ctx.Err()
			}
		}
		return execute(ctx, c, task, a)
	}
	type result struct {
		out GraphResult
		err error
	}
	finished := make(chan result, 1)
	go func() { out, err := RunDAG(context.Background(), p, h, ops); finished <- result{out, err} }()
	names := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-ready:
			names[name] = true
		case <-time.After(3 * time.Second):
			close(release)
			<-finished
			t.Fatal("independent nodes did not rendezvous")
		}
	}
	close(release)
	r := <-finished
	if r.err != nil || r.out.Status != "verified" || !reflect.DeepEqual(r.out.Completed, []string{"A", "B", "C", "D"}) || !names["B"] || !names["C"] || peak.Load() != 2 {
		t.Fatalf("result=%+v err=%v names=%v peak=%d", r.out, r.err, names, peak.Load())
	}
	// Successful replay must neither prepare nor execute again.
	ops.Prepare = func(context.Context, *run.Controller, Task, []ResolvedInput, *PreparedAttempt, *TerminalResult) (PreparedAttempt, error) {
		return PreparedAttempt{}, errors.New("unexpected relaunch")
	}
	again, err := RunDAG(context.Background(), p, h, ops)
	if err != nil || !reflect.DeepEqual(again.Completed, r.out.Completed) {
		t.Fatalf("restart %+v %v", again, err)
	}
}
func TestDAGFailureIsolation(t *testing.T) {
	p, h := dagFixture(t, 2, map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B"}, "E": {}})
	ops := fakeDAGOps(t, p)
	execute := ops.Execute
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		if task.Task == "D" {
			return ExecutionResult{}, errors.New("blocked descendant executed")
		}
		r, err := execute(ctx, c, task, a)
		if task.Task == "B" {
			r.Observation.State = Failed
		}
		return r, err
	}
	out, err := RunDAG(context.Background(), p, h, ops)
	if err != nil || out.State != routing.WaitHuman || out.Status != "partial" || !reflect.DeepEqual(out.Completed, []string{"A", "C", "E"}) || len(out.Failed) != 1 || out.Failed[0].Task != "B" || len(out.Blocked) != 1 || out.Blocked[0].Task != "D" {
		t.Fatalf("%+v %v", out, err)
	}
	again, err := RunDAG(context.Background(), p, h, ops)
	if err != nil || !reflect.DeepEqual(again.Completed, out.Completed) {
		t.Fatalf("isolated replay %+v %v", again, err)
	}
}
func TestDAGCancelFreshDrainAndReplayFence(t *testing.T) {
	p, h := dagFixture(t, 2, map[string][]string{"A": {}, "B": {}, "C": {}})
	ops := fakeDAGOps(t, p)
	execute := ops.Execute
	finalize := ops.Finalize
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	var drained atomic.Int32
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		r, err := execute(ctx, c, task, a)
		r.Cancelled = true
		return r, err
	}
	ops.Finalize = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt, e ExecutionResult) (TerminalResult, error) {
		if ctx.Err() != nil {
			return TerminalResult{}, errors.New("cleanup received cancelled context")
		}
		drained.Add(1)
		return finalize(ctx, c, task, a, e)
	}
	done := make(chan error, 1)
	go func() { _, err := RunDAG(ctx, p, h, ops); done <- err }()
	<-started
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if drained.Load() != 2 {
		t.Fatalf("drained=%d", drained.Load())
	}
	if _, err := RunDAG(context.Background(), p, h, ops); err == nil {
		t.Fatal("cancelled authorization replayed")
	}
}
func TestDAGReplayUnmatchedIntentAndExactPlan(t *testing.T) {
	p, h := dagFixture(t, 1, map[string][]string{"A": {}})
	ops := fakeDAGOps(t, p)
	loc, _ := run.Locate(p.Repo)
	log := &dagLog{path: dagJournalPath(loc.Marker, h)}
	if _, err := log.load(p, h); err != nil {
		t.Fatal(err)
	}
	id, _ := NewDAGID()
	if err := log.append(dagEntry{Task: "A", Phase: "intent", Operation: "prepare", OpID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunDAG(context.Background(), p, h, ops); err == nil {
		t.Fatal("unmatched intent replayed")
	}
	clone := p
	clone.Tasks = append([]Task(nil), p.Tasks...)
	clone.Tasks[0].Base = strings.Repeat("b", 40)
	if _, err := RunDAG(context.Background(), clone, h, ops); err == nil {
		t.Fatal("mutated approved plan accepted")
	}
}

func artifactFixture(t *testing.T) (string, string, ArtifactManifest) {
	t.Helper()
	return t.TempDir(), t.TempDir(), ArtifactManifest{Version: 1, Run: "run", PlanHash: digestText("plan"), Task: "A", Attempt: "attempt", Receipt: "receipt", ContractHash: digestText("contract"), SourceRevision: strings.Repeat("a", 40), SourceDigest: digestText("source"), InputDigest: InputDigest(nil), ExecutionDigest: digestText("execution"), VerificationDigest: digestText("verification"), CheckpointDigest: digestText("checkpoint")}
}
func TestArtifactIntegrityIdentityAndTraversal(t *testing.T) {
	store, source, m := artifactFixture(t)
	if err := os.WriteFile(filepath.Join(source, "result"), []byte("exact bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := PublishArtifacts(store, source, m, []ArtifactOutput{{"result", "result", 64}})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(store, ref); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "inputs")
	t.Cleanup(func() { _ = os.Chmod(dest, 0700) })
	inputs := []ResolvedInput{{"A", "result", "alias", ref}}
	if err := MaterializeInputs(store, dest, inputs); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "alias")); err != nil || string(b) != "exact bytes" {
		t.Fatalf("input %q %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(source, "result"), []byte("producer changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(store, ref); err != nil {
		t.Fatalf("published copy aliased producer: %v", err)
	}
	if _, err := PublishArtifacts(store, source, m, []ArtifactOutput{{"result", "result", 64}}); err == nil {
		t.Fatal("immutable manifest replaced")
	}
	changed := ref
	changed.Manifest.Run = "other"
	if err := VerifyArtifacts(store, changed); err == nil {
		t.Fatal("wrong run binding accepted")
	}
	alias := inputs
	alias[0].Producer = "B"
	if err := MaterializeInputs(store, filepath.Join(t.TempDir(), "bad-inputs"), alias); err == nil {
		t.Fatal("wrong consumer producer accepted")
	}
	published := filepath.Join(artifactDir(store, m), "result")
	if err := os.Chmod(published, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(published, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(store, ref); err == nil {
		t.Fatal("tampered bytes accepted")
	}
	t.Run("same_length_corruption", testArtifactSameLengthCorruption)
	for _, name := range []string{"../result", "/result", "a/../../result", "a\\result", "./result"} {
		if _, err := ReadArtifact(source, name, 64); err == nil {
			t.Fatalf("accepted traversal %q", name)
		}
	}
}
func testArtifactSameLengthCorruption(t *testing.T) {
	store, source, m := artifactFixture(t)
	original, corrupted := []byte("exact bytes"), []byte("EXACT BYTES")
	if len(original) != len(corrupted) || string(original) == string(corrupted) {
		t.Fatal("fixture must change bytes without changing length")
	}
	if err := os.WriteFile(filepath.Join(source, "result"), original, 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := PublishArtifacts(store, source, m, []ArtifactOutput{{"result", "result", 64}})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []ResolvedInput{{"A", "result", "alias", ref}}
	valid := filepath.Join(t.TempDir(), "valid-inputs")
	t.Cleanup(func() { _ = os.Chmod(valid, 0700) })
	if err := VerifyArtifacts(store, ref); err != nil {
		t.Fatalf("intact artifact positive control: %v", err)
	}
	if err := MaterializeInputs(store, valid, inputs); err != nil {
		t.Fatalf("intact consumer positive control: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(valid, "alias")); err != nil || string(b) != string(original) {
		t.Fatalf("intact consumer bytes %q: %v", b, err)
	}
	published := filepath.Join(artifactDir(store, m), "result")
	if err := os.Chmod(published, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(published, corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(store, ref); err == nil || err.Error() != "artifact content hash mismatch" {
		t.Fatalf("same-length corruption must fail the content hash check: %v", err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid-inputs")
	if err := MaterializeInputs(store, invalid, inputs); err == nil || err.Error() != "artifact content hash mismatch" {
		t.Fatalf("same-length consumer corruption must fail the content hash check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(invalid, "alias")); !os.IsNotExist(err) {
		t.Fatalf("tampered bytes reached consumer: %v", err)
	}
}

func TestArtifactLinksTypesAndBounds(t *testing.T) {
	_, source, _ := artifactFixture(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "leaf")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArtifact(source, "leaf", 64); err == nil {
		t.Fatal("leaf symlink accepted")
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(source, "parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArtifact(source, "parent/secret", 64); err == nil {
		t.Fatal("parent symlink accepted")
	}
	if err := os.Link(outside, filepath.Join(source, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArtifact(source, "hard", 64); err == nil {
		t.Fatal("hardlink accepted")
	}
	if err := os.Mkdir(filepath.Join(source, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArtifact(source, "directory", 64); err == nil {
		t.Fatal("directory accepted")
	}
	if err := os.WriteFile(filepath.Join(source, "big"), make([]byte, 65), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArtifact(source, "big", 64); err == nil {
		t.Fatal("oversized output accepted")
	}
}
