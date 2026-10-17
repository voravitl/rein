package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

func digestText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func graphFixture(t *testing.T, change func(*Plan)) (Plan, string) {
	t.Helper()
	old, _ := fixture(t)
	loc, err := run.Locate(old.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(config, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	ct, err := contract.Load("task")
	if err != nil {
		t.Fatal(err)
	}
	p := old
	p.Schema = 2
	p.MaxChecks = 10
	p.Tasks = []Task{{Task: "task", ContractHash: routing.ContractHash(ct), ConfigPath: config, ConfigDigest: digestText(`{}`), Profile: json.RawMessage(`{"schema":1}`), Base: "HEAD", MaxAttempts: 2, Launches: map[string][]string{"fake": {"worker", "{model}", "{run}", "{worktree}"}}, Verify: [][]string{{"go", "test", "./..."}}}}
	if change != nil {
		change(&p)
	}
	raw, _ := json.Marshal(p)
	p, hash, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartSupervised(p.Repo, p.Run, nil, run.Owner{SessionID: "owner", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	return p, hash
}
func graphOps(p Plan, events *[]string) Operations {
	attempts := 0
	return Operations{
		Resume:     func(context.Context, *run.Controller, Task, Dispatch, Outcome) error { return nil },
		Revalidate: func(context.Context, *run.Controller, Task, Dispatch, Outcome) error { return nil },
		Prepare: func(ctx context.Context, c *run.Controller, t Task) (*routing.AutoResult, error) {
			attempts++
			*events = append(*events, t.Task+":prepare")
			ct, err := contract.Load(t.Task)
			if err != nil {
				return nil, err
			}
			a := fmt.Sprintf("attempt-%d", attempts)
			return &routing.AutoResult{Attempt: a, Receipt: &routing.Decision{Run: p.Run, Task: t.Task, Attempt: a, DecisionID: "decision-" + a, ContractHash: t.ContractHash, ConfigPath: t.ConfigPath, ConfigHash: t.ConfigDigest, Provider: "fake", Model: "model", Pool: "pool", Worktree: ct.Worktree}}, nil
		},
		Launch: func(ctx context.Context, c *run.Controller, t Task, d *routing.Decision, argv []string) (Dispatch, error) {
			*events = append(*events, t.Task+":launch")
			if !reflect.DeepEqual(argv, []string{"worker", "model", p.Run, d.Worktree}) {
				return Dispatch{}, fmt.Errorf("invalid argv: %v", argv)
			}
			return Dispatch{Task: t.Task, Attempt: d.Attempt, Decision: d.DecisionID, OrcaTask: "orca-" + d.Attempt, Dispatch: "dispatch-" + d.Attempt, Worktree: d.Worktree, Pool: d.Pool}, nil
		},
		Observe: func(ctx context.Context, c *run.Controller, t Task, d Dispatch) (Observation, error) {
			*events = append(*events, t.Task+":observe")
			return Observation{State: Completed, Evidence: "exact completed delivery"}, nil
		},
		Finish: func(ctx context.Context, c *run.Controller, t Task, d Dispatch, o Observation) (Outcome, error) {
			*events = append(*events, t.Task+":finish")
			return Outcome{CheckpointDigest: digestText("checkpoint " + d.Attempt), VerificationDigest: digestText("verification " + d.Attempt)}, nil
		},
	}
}
func TestGraphDAGCompletionRestartAndLastCheckFinishes(t *testing.T) {
	p, h := graphFixture(t, func(p *Plan) {
		p.MaxChecks = 2
		first := p.Tasks[0]
		second := first
		second.Task = "dependent"
		second.DependsOn = []string{"task"}
		ct, _ := contract.Load("task")
		ct.Name = "dependent"
		b, _ := json.Marshal(ct)
		if err := os.WriteFile(contract.PathOf("dependent"), b, 0600); err != nil {
			t.Fatal(err)
		}
		second.ContractHash = routing.ContractHash(ct)
		p.Tasks = []Task{second, first}
	})
	var events []string
	ops := graphOps(p, &events)
	out, err := RunGraph(context.Background(), p, h, ops)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"task:prepare", "task:launch", "task:observe", "task:finish", "dependent:prepare", "dependent:launch", "dependent:observe", "dependent:finish"}
	if !reflect.DeepEqual(events, want) || out.State != routing.WaitHuman || len(out.Completed) != 2 || out.Checks != 2 {
		t.Fatalf("events=%v result=%+v", events, out)
	}
	before := len(events)
	out, err = RunGraph(context.Background(), p, h, ops)
	if err != nil || len(events) != before || len(out.Completed) != 2 {
		t.Fatalf("restart repeated operations: %+v %v %v", out, events, err)
	}
}
func TestGraphQuotaAttemptsRemainBoundedAcrossRestart(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	ops.Observe = func(context.Context, *run.Controller, Task, Dispatch) (Observation, error) {
		return Observation{State: Quota, Evidence: "quota delivery"}, nil
	}
	ops.Finish = func(context.Context, *run.Controller, Task, Dispatch, Observation) (Outcome, error) {
		return Outcome{CheckpointDigest: digestText("safe gone+settled snapshot")}, nil
	}
	out, err := RunGraph(context.Background(), p, h, ops)
	if err != nil || out.State != routing.WaitHuman || !strings.Contains(out.Reason, "attempt budget") {
		t.Fatalf("%+v %v", out, err)
	}
	before := len(events)
	out, err = RunGraph(context.Background(), p, h, ops)
	if err != nil || len(events) != before || !strings.Contains(out.Reason, "attempt budget") {
		t.Fatalf("retry budget reset: %+v %v %v", out, events, err)
	}
}
func TestGraphUnknownLaunchOutcomeNeverRetried(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	launches := 0
	ops.Launch = func(context.Context, *run.Controller, Task, *routing.Decision, []string) (Dispatch, error) {
		launches++
		panic("process died after remote dispatch")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected simulated crash")
			}
		}()
		RunGraph(context.Background(), p, h, ops)
	}()
	_, err := RunGraph(context.Background(), p, h, ops)
	if err == nil || !strings.Contains(err.Error(), "unresolved") || launches != 1 {
		t.Fatalf("unknown dispatch retried: %d %v", launches, err)
	}
}
func TestGraphRejectsChangedConfigBeforeLaunch(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	prepare := ops.Prepare
	ops.Prepare = func(ctx context.Context, c *run.Controller, task Task) (*routing.AutoResult, error) {
		r, e := prepare(ctx, c, task)
		if err := os.WriteFile(task.ConfigPath, []byte(`{"changed":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		return r, e
	}
	_, err := RunGraph(context.Background(), p, h, ops)
	if err == nil || !strings.Contains(err.Error(), "config changed") || len(events) != 1 {
		t.Fatalf("config mutation admitted: %v %v", events, err)
	}
}
func TestGraphRejectsUnverifiedCompletionAndBlocksDependent(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	ops.Finish = func(context.Context, *run.Controller, Task, Dispatch, Observation) (Outcome, error) {
		return Outcome{CheckpointDigest: digestText("checkpoint")}, nil
	}
	out, err := RunGraph(context.Background(), p, h, ops)
	if err == nil || len(out.Completed) != 0 || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("unverified completion: %+v %v", out, err)
	}
	_, err = RunGraph(context.Background(), p, h, ops)
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("invalid evidence result did not fail closed: %v", err)
	}
}
func TestGraphCallbackErrorDurablyBlocksRestart(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	ops.Prepare = func(context.Context, *run.Controller, Task) (*routing.AutoResult, error) {
		return nil, errors.New("provider outcome unknown")
	}
	if _, err := RunGraph(context.Background(), p, h, ops); err == nil {
		t.Fatal("callback error swallowed")
	}
	ops = graphOps(p, &events)
	if _, err := RunGraph(context.Background(), p, h, ops); err == nil || len(events) != 0 {
		t.Fatalf("failed operation retried %v %v", events, err)
	}
}
func TestGraphPlanRejectsCycleAndPartialPlaceholder(t *testing.T) {
	p, _ := graphFixture(t, nil)
	tests := []func(*Plan){func(p *Plan) { p.Tasks[0].DependsOn = []string{"task"} }, func(p *Plan) { p.Tasks[0].Launches["fake"] = []string{"worker", "--model={model}"} }, func(p *Plan) { p.Tasks[0].AllowedUntracked = []string{"../escape"} }, func(p *Plan) { p.Tasks[0].Attempt = "existing" }, func(p *Plan) { p.Tasks[0].MaxAttempts = 4 }}
	for i, mutate := range tests {
		raw, _ := json.Marshal(p)
		var clone Plan
		json.Unmarshal(raw, &clone)
		mutate(&clone)
		raw, _ = json.Marshal(clone)
		if _, _, err := Decode(raw); err == nil {
			t.Fatalf("invalid plan %d accepted", i)
		}
	}
}

func TestGraphInvalidCompletedProofBlocksDependent(t *testing.T) {
	p, h := graphFixture(t, func(p *Plan) {
		first := p.Tasks[0]
		second := first
		second.Task = "dependent"
		second.DependsOn = []string{"task"}
		ct, _ := contract.Load("task")
		ct.Name = "dependent"
		b, _ := json.Marshal(ct)
		os.WriteFile(contract.PathOf("dependent"), b, 0600)
		second.ContractHash = routing.ContractHash(ct)
		p.Tasks = []Task{first, second}
	})
	var events []string
	ops := graphOps(p, &events)
	ops.Revalidate = func(context.Context, *run.Controller, Task, Dispatch, Outcome) error {
		return errors.New("checkpoint missing")
	}
	_, err := RunGraph(context.Background(), p, h, ops)
	if err == nil {
		t.Fatal("invalid done proof admitted")
	}
	for _, event := range events {
		if strings.HasPrefix(event, "dependent:") {
			t.Fatalf("dependent advanced on stale proof: %v", events)
		}
	}
}

func TestGraphPrepareFailureIsNotRetriedOrDiscarded(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	calls := 0
	expected := errors.New("preparation failed")
	ops.Prepare = func(context.Context, *run.Controller, Task) (*routing.AutoResult, error) {
		calls++
		return nil, expected
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := RunGraph(ctx, p, h, ops); !errors.Is(err, expected) {
		t.Fatalf("prepare error discarded: %v (calls=%d)", err, calls)
	}
	if calls != 1 {
		t.Fatalf("prepare repeated %d times", calls)
	}
	if _, err := RunGraph(ctx, p, h, ops); err == nil || calls != 1 {
		t.Fatalf("restart retried unresolved preparation: %v calls=%d", err, calls)
	}
}
