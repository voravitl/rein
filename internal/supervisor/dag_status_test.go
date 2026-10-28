package supervisor

import (
	"bytes"
	"context"
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

func dagStatusFixture(t *testing.T, dependencies map[string][]string) (Plan, string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, value)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	p, _ := dagFixture(t, 1, dependencies)
	repo, err := filepath.EvalSymlinks(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	p.Repo = repo
	for _, key := range []string{"REIN_RUN_DIR", "PIPELINE_CONTRACTS"} {
		canonical, err := filepath.EvalSymlinks(os.Getenv(key))
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, canonical)
	}
	for i := range p.Tasks {
		p.Tasks[i].MaxAttempts = 2
		p.Tasks[i].Action.Idempotent = true
	}
	return rebindDAGFixture(t, p)
}

func dagStatusLog(t *testing.T, p Plan, hash string) *dagLog {
	t.Helper()
	loc, err := run.Locate(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	l := &dagLog{path: dagJournalPath(loc.Marker, hash)}
	if _, err := l.load(p, hash); err != nil {
		t.Fatal(err)
	}
	return l
}

func dagStatusIntent(t *testing.T, l *dagLog, task, operation, attempt string) dagEntry {
	t.Helper()
	e := dagEntry{Task: task, Operation: operation, Attempt: attempt, Phase: "intent", OpID: digestText(fmt.Sprintf("%s-%d", task, l.seq+1))}
	if err := l.append(e); err != nil {
		t.Fatal(err)
	}
	return e
}

func dagStatusResult(t *testing.T, l *dagLog, intent dagEntry, value any) {
	t.Helper()
	intent.Phase, intent.Data = "result", mustJSON(value)
	if err := l.append(intent); err != nil {
		t.Fatal(err)
	}
}

// Build real strict envelopes, using the existing fake operations only to
// construct valid receipts/artifact evidence. No provider or Docker is invoked.
func dagStatusReach(t *testing.T, p Plan, hash, stage string) *dagLog {
	t.Helper()
	l := dagStatusLog(t, p, hash)
	if stage == "ready" || stage == "pending" {
		return l
	}
	if stage == "global_failure" {
		if err := l.append(dagEntry{Phase: "control", Operation: "cancel", Error: "owner cancelled"}); err != nil {
			t.Fatal(err)
		}
		return l
	}
	task := p.Tasks[0]
	prepare := dagStatusIntent(t, l, task.Task, "prepare", "")
	if stage == "preparing" {
		return l
	}
	if stage == "failed" {
		prepare.Phase, prepare.Error = "error", "fixture preparation failed"
		if err := l.append(prepare); err != nil {
			t.Fatal(err)
		}
		return l
	}
	ops := fakeDAGOps(t, p)
	a, err := ops.Prepare(context.Background(), nil, task, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dagStatusResult(t, l, prepare, a)
	if stage == "running" {
		return l
	}
	execute := dagStatusIntent(t, l, task.Task, "execute", a.Attempt)
	if stage == "execute_intent" {
		return l
	}
	execution, err := ops.Execute(context.Background(), nil, task, a)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "wait" || stage == "retry" {
		execution.Observation.State = Failed
	}
	if stage == "cancelled" {
		execution.Cancelled = true
	}
	dagStatusResult(t, l, execute, execution)
	if stage == "finalizing" {
		return l
	}
	finalize := dagStatusIntent(t, l, task.Task, "finalize", a.Attempt)
	if stage == "finalize_intent" {
		return l
	}
	terminal, err := ops.Finalize(context.Background(), nil, task, a, execution)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "retry" {
		terminal.Retry = true
	}
	dagStatusResult(t, l, finalize, terminal)
	return l
}

func TestDAGStatusStateGoldensAndStrictReplayParity(t *testing.T) {
	for _, tc := range []struct {
		stage  string
		states []string
		count  int
	}{
		{"ready", []string{"ready", "pending", "pending"}, 0},
		{"pending", []string{"ready", "pending", "pending"}, 0},
		{"preparing", []string{"preparing", "pending", "pending"}, 0},
		{"running", []string{"running", "pending", "pending"}, 1},
		{"execute_intent", []string{"running", "pending", "pending"}, 1},
		{"finalizing", []string{"finalizing", "pending", "pending"}, 1},
		{"finalize_intent", []string{"finalizing", "pending", "pending"}, 1},
		{"done", []string{"done", "ready", "pending"}, 1},
		{"wait", []string{"wait", "blocked", "blocked"}, 1},
		{"failed", []string{"failed", "blocked", "blocked"}, 0},
		{"cancelled", []string{"cancelled", "blocked", "blocked"}, 1},
		{"retry", []string{"retry", "pending", "pending"}, 1},
		{"global_failure", []string{"failed", "failed", "failed"}, 0},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			p, hash := dagStatusFixture(t, map[string][]string{"A": {}, "B": {"A"}, "C": {"B"}})
			l := dagStatusReach(t, p, hash, tc.stage)
			if _, err := l.read(p, hash, true); err != nil {
				t.Fatalf("golden must pass existing strict observe validation: %v", err)
			}
			if err := os.Remove(dagTimesPath(l.path)); err != nil {
				t.Fatal(err)
			}
			view, err := ReadDAGStatus(context.Background(), p, hash)
			if err != nil || len(view.Nodes) != 3 {
				t.Fatalf("status %+v: %v", view, err)
			}
			for i, n := range view.Nodes {
				attempt := 0
				if i == 0 {
					attempt = tc.count
				}
				want := DAGNodeStatus{Task: p.Tasks[i].Task, Kind: "action", Layer: i, State: tc.states[i], Attempt: attempt, MaxAttempts: 2}
				if !reflect.DeepEqual(n, want) {
					t.Fatalf("node golden:\ngot  %+v\nwant %+v", n, want)
				}
			}
		})
	}
}

func TestDAGStatusWorkerLaunchAndUnattributedGuard(t *testing.T) {
	p, _ := dagStatusFixture(t, map[string][]string{"A": {}})
	task := &p.Tasks[0]
	task.Kind, task.Action, task.ProcessPolicy = "worker", nil, "non_detaching"
	task.ConfigPath, task.ConfigDigest = filepath.Join(p.Repo, "providers.json"), digestText("{}")
	task.Profile = []byte(`{"schema":1}`)
	task.Launches = map[string][]string{"fake": {"worker", "{model}"}}
	p, hash := rebindDAGFixture(t, p)
	task = &p.Tasks[0]
	ct, err := contract.Load(task.Task)
	if err != nil {
		t.Fatal(err)
	}
	a := PreparedAttempt{Number: 1, Run: p.Run, PlanHash: hash, Task: task.Task, Kind: "worker", Attempt: "attempt-1", ReceiptID: "receipt-1", ContractHash: task.ContractHash, Worktree: ct.Worktree, SourceRevision: task.Base, SourceDigest: digestText("source"), InputDigest: InputDigest(nil)}
	a.Worker = &routing.AutoResult{Attempt: a.Attempt, Receipt: &routing.Decision{
		Run: p.Run, Task: a.Task, Attempt: a.Attempt, DecisionID: a.ReceiptID,
		ContractHash: a.ContractHash, ConfigPath: task.ConfigPath, ConfigHash: task.ConfigDigest,
		Provider: "fake", Agent: "codex", Model: "receipt-model", Effort: "low", Pool: "pool", Worktree: a.Worktree,
	}, Launch: &routing.Launch{Agent: "codex", Model: "launch-model", Effort: "high"}}
	l := dagStatusLog(t, p, hash)
	dagStatusResult(t, l, dagStatusIntent(t, l, a.Task, "prepare", ""), a)
	if _, err := l.read(p, hash, true); err != nil {
		t.Fatalf("worker fixture invalid: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(contract.SeenPath(a.Task)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contract.SeenPath(a.Task), []byte("2026-10-28T00:00:00Z codex PreToolUse shell gen=prior-attempt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := ReadDAGStatus(context.Background(), p, hash)
	if err != nil {
		t.Fatal(err)
	}
	n := view.Nodes[0]
	if n.Agent == nil || *n.Agent != "codex" || n.Model == nil || *n.Model != "launch-model" || n.Effort == nil || *n.Effort != "high" || n.Tokens != nil || n.GuardLine != nil {
		t.Fatalf("receipt display or stale guard attribution: %+v", n)
	}
}

func TestDAGStatusConcurrentRunDoesNotAcquireController(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	ops := fakeDAGOps(t, p)
	started, release := make(chan struct{}), make(chan struct{})
	execute := ops.Execute
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ExecutionResult{}, ctx.Err()
		}
		return execute(ctx, c, task, a)
	}
	type outcome struct {
		result GraphResult
		err    error
	}
	finished := make(chan outcome, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		result, err := RunDAG(ctx, p, hash, ops)
		finished <- outcome{result, err}
	}()
	select {
	case <-started:
	case got := <-finished:
		t.Fatalf("fake run failed before execute: %+v", got)
	case <-time.After(3 * time.Second):
		t.Fatal("fake run did not start")
	}
	// Release even if an assertion fails, then drain before test cleanup removes
	// fixture paths or restores the process environment.
	defer func() {
		close(release)
		select {
		case got := <-finished:
			if got.err != nil || got.result.Status != "verified" || got.result.Checks != 1 || len(got.result.Completed) != 1 || got.result.Completed[0] != "A" {
				t.Errorf("observation changed run outcome: %+v %v", got.result, got.err)
			}
		case <-time.After(3 * time.Second):
			cancel()
			t.Error("fake run did not finish")
		}
	}()
	loc, err := run.Locate(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	journal := dagJournalPath(loc.Marker, hash)
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ReadDAGStatus(context.Background(), p, hash)
	if err != nil || view.Terminal() || len(view.Nodes) != 1 || view.Nodes[0].State != "running" {
		t.Fatalf("status must observe active locked run: %+v %v", view, err)
	}
	after, err := os.ReadFile(journal)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("observation modified active journal")
	}
}

func TestDAGStatusReplayIgnoresMissingAndCorruptTimingSidecar(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	ops := fakeDAGOps(t, p)
	control, err := RunDAG(context.Background(), p, hash, ops)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := run.Locate(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	journal := dagJournalPath(loc.Marker, hash)
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, corruption := range []string{"missing", "malformed", "unmatched"} {
		t.Run(corruption, func(t *testing.T) {
			sidecar := dagTimesPath(journal)
			if corruption == "missing" {
				if err := os.Remove(sidecar); err != nil {
					t.Fatal(err)
				}
			} else {
				raw := "not-json\n"
				if corruption == "unmatched" {
					raw = `{"seq":999,"at":"2026-10-28T00:00:00Z"}` + "\n"
				}
				if err := os.WriteFile(sidecar, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			again, err := RunDAG(context.Background(), p, hash, ops)
			if err != nil || !bytes.Equal(mustJSON(again), mustJSON(control)) {
				t.Fatalf("sidecar changed authoritative replay: %+v %v", again, err)
			}
			after, err := os.ReadFile(journal)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("replay changed journal bytes")
			}
			view, err := ReadDAGStatus(context.Background(), p, hash)
			if err != nil || view.Nodes[0].ElapsedMS != nil {
				t.Fatalf("missing/corrupt times must remain unknown: %+v %v", view, err)
			}
		})
	}
}

func TestDAGStatusTimingAppendFailureIsAdvisory(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	loc, err := run.Locate(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	l := &dagLog{path: dagJournalPath(loc.Marker, hash)}
	if err := os.Mkdir(dagTimesPath(l.path), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := l.load(p, hash); err != nil || l.seq != 1 {
		t.Fatalf("sidecar failure changed authoritative header append: seq=%d err=%v", l.seq, err)
	}
	if _, err := RunDAG(context.Background(), p, hash, fakeDAGOps(t, p)); err != nil {
		t.Fatalf("sidecar failure changed run result: %v", err)
	}
	failed := &dagLog{path: filepath.Join(t.TempDir(), "missing", "journal.jsonl")}
	if err := failed.append(dagEntry{Phase: "header", Operation: "bind"}); err == nil || failed.seq != 0 {
		t.Fatalf("failed journal append unexpectedly succeeded: seq=%d err=%v", failed.seq, err)
	}
	if _, err := os.Stat(dagTimesPath(failed.path)); !os.IsNotExist(err) {
		t.Fatalf("failed authoritative append wrote timing: %v", err)
	}
}

func TestDAGStatusElapsedGoldenAndBackwardsClock(t *testing.T) {
	for _, state := range []string{"running", "done", "retry", "wait", "cancelled", "failed"} {
		t.Run(state, func(t *testing.T) {
			p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
			l := dagStatusReach(t, p, hash, state)
			base := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
			for _, backwards := range []bool{false, true} {
				var sidecar bytes.Buffer
				for seq := 1; seq <= l.seq; seq++ {
					offset := time.Duration(seq) * time.Second
					if backwards {
						offset = -offset
					}
					sidecar.Write(mustJSON(dagTimeEntry{Seq: seq, At: base.Add(offset).Format(time.RFC3339Nano)}))
					sidecar.WriteByte('\n')
				}
				if err := os.WriteFile(dagTimesPath(l.path), sidecar.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				now := base.Add(10 * time.Second)
				if backwards {
					now = base.Add(-10 * time.Second)
				}
				view, err := readDAGStatus(context.Background(), p, hash, func() time.Time { return now })
				if err != nil {
					t.Fatal(err)
				}
				want := int64(l.seq-2) * 1000
				if state == "running" || state == "retry" {
					want = 8000
				}
				if backwards {
					want = 0
				}
				if got := view.Nodes[0].ElapsedMS; got == nil || *got != want {
					t.Fatalf("state=%s backwards=%v: elapsed=%v want=%d", state, backwards, got, want)
				}
			}
		})
	}
}

func TestDAGStatusStrictParserRejectsReplayCorruption(t *testing.T) {
	for _, corruption := range []string{"header", "sequence", "pairing", "dependency", "receipt", "unknown_field", "duplicate_field", "lifecycle"} {
		t.Run(corruption, func(t *testing.T) {
			p, hash := dagStatusFixture(t, map[string][]string{"A": {}, "B": {"A"}})
			l := dagStatusReach(t, p, hash, "done")
			raw, err := os.ReadFile(l.path)
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
			var entries []dagEntry
			for _, line := range lines {
				var e dagEntry
				if err := strictDecode(line, &e); err != nil {
					t.Fatal(err)
				}
				entries = append(entries, e)
			}
			switch corruption {
			case "header":
				entries[0].Data = mustJSON(dagHeader{1, 3, p.Run, digestText("other plan")})
			case "sequence":
				entries[1].Seq = 99
			case "pairing":
				entries[2].OpID = digestText("different intent")
			case "dependency":
				entries[1].Task = "B"
			case "receipt":
				var a PreparedAttempt
				if err := strictDecode(entries[2].Data, &a); err != nil {
					t.Fatal(err)
				}
				a.SourceDigest = "invalid"
				entries[2].Data = mustJSON(a)
			case "lifecycle":
				entries = append(entries, dagEntry{Seq: len(entries) + 1, Phase: "lifecycle", Operation: "invocation_return", Data: []byte(`{}`)})
			}
			var modified bytes.Buffer
			for _, e := range entries {
				modified.Write(mustJSON(e))
				modified.WriteByte('\n')
			}
			raw = modified.Bytes()
			if corruption == "unknown_field" {
				raw = bytes.Replace(raw, []byte(`{"seq":1`), []byte(`{"extra":true,"seq":1`), 1)
			}
			if corruption == "duplicate_field" {
				raw = bytes.Replace(raw, []byte(`{"seq":1`), []byte(`{"seq":1,"seq":1`), 1)
			}
			if err := os.WriteFile(l.path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, replayError := l.read(p, hash, true)
			_, statusError := ReadDAGStatus(context.Background(), p, hash)
			if replayError == nil || statusError == nil || replayError.Error() != statusError.Error() {
				t.Fatalf("strict parser drift: replay=%v status=%v", replayError, statusError)
			}
		})
	}
}

func TestDAGStatusPartialTailAfterTerminalIsNotTerminal(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusReach(t, p, hash, "done")
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(`{"seq":8`)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("partial fixture write: %v %v", writeErr, closeErr)
	}
	view, err := ReadDAGStatus(context.Background(), p, hash)
	if err != nil || view.Status != "changing" || view.Terminal() {
		t.Fatalf("partial tail must override terminal prefix: %+v %v", view, err)
	}
}

func TestDAGStatusLayersIndependentOfPlanOrder(t *testing.T) {
	p, _ := dagStatusFixture(t, map[string][]string{"A": {}, "B": {"A"}, "C": {"B"}})
	p.Tasks = []Task{p.Tasks[2], p.Tasks[0], p.Tasks[1]}
	p, hash := rebindDAGFixture(t, p)
	view, err := ReadDAGStatus(context.Background(), p, hash)
	if err != nil {
		t.Fatal(err)
	}
	for i, layer := range []int{2, 0, 1} {
		if view.Nodes[i].Layer != layer {
			t.Fatalf("plan order changed layer: %+v", view.Nodes)
		}
	}
}

func TestDAGStatusTimingHookTracksSuccessfulJournalSequences(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusReach(t, p, hash, "done")
	raw, err := os.ReadFile(dagTimesPath(l.path))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) != l.seq {
		t.Fatalf("timing must follow every successful journal append: lines=%d seq=%d", len(lines), l.seq)
	}
	for i, line := range lines {
		var e dagTimeEntry
		if err := strictDecode(line, &e); err != nil || e.Seq != i+1 || !strings.HasSuffix(e.At, "Z") {
			t.Fatalf("invalid timing row %q: %v", line, err)
		}
		if _, err := time.Parse(time.RFC3339Nano, e.At); err != nil {
			t.Fatal(err)
		}
	}
	view, err := ReadDAGStatus(context.Background(), p, hash)
	if err != nil || view.Nodes[0].ElapsedMS == nil || *view.Nodes[0].ElapsedMS < 0 {
		t.Fatalf("generated sidecar cannot display elapsed: %+v %v", view, err)
	}
}

func TestDAGStatusLatestInvocationSummary(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	ops := fakeDAGOps(t, p)
	authority := ops.Authority
	ops.InvocationID = digestText("first invocation")
	ops.Authority = func(context.Context, *run.Controller) error {
		return errors.New("authority temporarily unavailable before admission")
	}
	if _, err := RunDAG(context.Background(), p, hash, ops); err == nil {
		t.Fatal("fixture should record a failed invocation without task intents")
	}
	view, err := ReadDAGStatus(context.Background(), p, hash)
	if err != nil || view.Status != "reconciliation" {
		t.Fatalf("latest failed invocation: %+v %v", view, err)
	}
	ops.Authority = authority
	ops.InvocationID = digestText("second invocation")
	if _, err := RunDAG(context.Background(), p, hash, ops); err != nil {
		t.Fatal(err)
	}
	view, err = ReadDAGStatus(context.Background(), p, hash)
	if err != nil || view.Status != "done" || view.Nodes[0].State != "done" {
		t.Fatalf("historical reconciliation hid a later successful return: %+v %v", view, err)
	}
}

func TestDAGStatusTimingCannotAliasAuthoritativeState(t *testing.T) {
	for _, target := range []string{"journal", "marker"} {
		for _, link := range []string{"symlink", "hardlink"} {
			t.Run(target+"/"+link, func(t *testing.T) {
				p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
				l := dagStatusLog(t, p, hash)
				loc, err := run.Locate(p.Repo)
				if err != nil {
					t.Fatal(err)
				}
				marker, err := os.ReadFile(loc.Marker)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(dagTimesPath(l.path)); err != nil {
					t.Fatal(err)
				}
				name := l.path
				if target == "marker" {
					name = loc.Marker
				}
				create := os.Symlink
				if link == "hardlink" {
					create = os.Link
				}
				if err := create(name, dagTimesPath(l.path)); err != nil {
					t.Fatal(err)
				}
				dagStatusIntent(t, l, "A", "prepare", "")
				if _, err := l.read(p, hash, true); err != nil {
					t.Fatalf("timing sidecar corrupted authoritative journal: %v", err)
				}
				after, err := os.ReadFile(loc.Marker)
				if err != nil || !bytes.Equal(marker, after) {
					t.Fatal("timing sidecar corrupted marker")
				}
			})
		}
	}
}

func dagStatusReadPromptly(t *testing.T, p Plan, hash string, unblock func()) (DAGStatus, error) {
	t.Helper()
	type result struct {
		view DAGStatus
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		view, err := ReadDAGStatus(context.Background(), p, hash)
		finished <- result{view, err}
	}()
	select {
	case got := <-finished:
		return got.view, got.err
	case <-time.After(500 * time.Millisecond):
		if unblock != nil {
			unblock()
		}
		select {
		case <-finished:
		case <-time.After(500 * time.Millisecond):
			t.Error("status reader remained blocked after FIFO release")
		}
		t.Fatal("status file read exceeded 500ms")
		return DAGStatus{}, nil
	}
}

func TestDAGStatusFIFOTimingSidecarReturnsUnknownPromptly(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusReach(t, p, hash, "done")
	path := dagTimesPath(l.path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	unblock := makeDAGStatusFIFO(t, path)
	view, err := dagStatusReadPromptly(t, p, hash, unblock)
	if err != nil || view.Status != "done" || len(view.Nodes) != 1 || view.Nodes[0].ElapsedMS != nil {
		t.Fatalf("advisory FIFO must leave elapsed unknown without failing status: %+v %v", view, err)
	}
}

func TestDAGStatusFIFOJournalFailsPromptly(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusReach(t, p, hash, "done")
	if err := os.Remove(l.path); err != nil {
		t.Fatal(err)
	}
	unblock := makeDAGStatusFIFO(t, l.path)
	view, err := dagStatusReadPromptly(t, p, hash, unblock)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO journal must fail promptly: %+v %v", view, err)
	}
}

func TestDAGStatusOversizedTimingSidecarReturnsUnknown(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusReach(t, p, hash, "done")
	path := dagTimesPath(l.path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Leading whitespace keeps every timing row valid, so this regression fails
	// if the size cap is removed rather than passing on malformed JSON alone.
	limit := dagStatusTimingReadLimit(l.seq)
	padded := append(bytes.Repeat([]byte{' '}, int(limit)+1), raw...)
	if err := os.WriteFile(path, padded, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := dagStatusReadPromptly(t, p, hash, nil)
	if err != nil || view.Status != "done" || len(view.Nodes) != 1 || view.Nodes[0].ElapsedMS != nil {
		t.Fatalf("oversized telemetry must leave elapsed unknown without failing status: %+v %v", view, err)
	}
	if got := dagStatusTimingReadLimit(1 << 30); got != 4<<20 {
		t.Fatalf("timing read limit must cap at 4 MiB: %d", got)
	}
}

func TestDAGStatusNonRegularJournalFailsPromptly(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
			l := dagStatusReach(t, p, hash, "done")
			if err := os.Rename(l.path, l.path+".original"); err != nil {
				t.Fatal(err)
			}
			if kind == "directory" {
				if err := os.Mkdir(l.path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(l.path+".original", l.path); err != nil {
				t.Fatal(err)
			}
			view, err := dagStatusReadPromptly(t, p, hash, nil)
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("nonregular journal must fail promptly: %+v %v", view, err)
			}
		})
	}
}
