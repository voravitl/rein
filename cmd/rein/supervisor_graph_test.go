package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

// RED stage for PR-C terminal graph. Every test below compiles against the
// existing base and reaches `rein supervisor status`; on base the first graph
// call fails because `--view`/`--seq` are not defined flags (exit 2,
// "flag provided but not defined"). Fixture sanity checks run before the
// first graph call so a RED result can only come from the missing flags.

// Reuse the status fixture's canonical repository and binding, with inert action
// descriptions and distinct ownership directories. Status must execute none of them.
func supervisorGraphFixture(t *testing.T, dependencies map[string][]string) (*e2eEnv, string, supervisor.Plan, string) {
	t.Helper()
	e, path, p, _ := supervisorStatusFixture(t)
	repo, err := filepath.EvalSymlinks(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	p.Repo = repo
	base, rootHash := p.Tasks[0].Base, p.Tasks[0].ContractHash
	p.Tasks = nil
	for _, name := range []string{"task", "left", "right", "join", "leaf"} {
		deps, ok := dependencies[name]
		if !ok {
			continue
		}
		hash := rootHash
		if name != "task" {
			c := *e.c
			c.Name, c.RunCopy = name, ""
			c.Worktree = filepath.Join(e.root, "graph-node-"+name)
			c.ReportPath = filepath.Join(e.root, "reports", name+".md")
			if err := os.Mkdir(c.Worktree, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(contract.PathOf(name), dagEncode(c), 0600); err != nil {
				t.Fatal(err)
			}
			hash = routing.ContractHash(&c)
		}
		p.Tasks = append(p.Tasks, supervisor.Task{
			Task: name, Kind: "action", ContractHash: hash, Base: base,
			MaxAttempts: 1, DependsOn: deps, Verify: [][]string{{"/bin/true"}},
			Action: &supervisor.ActionSpec{
				Argv: []string{"/bin/true"}, TimeoutMS: 1000, MaxOutputBytes: 64,
				MaxOutputFiles: 1, MaxOutputTotalBytes: 64,
				Sandbox: supervisor.ActionSandbox{Backend: "docker", Image: "sha256:" + strings.Repeat("a", 64)},
			},
		})
	}
	hash := bindDAGPlan(t, e, path, p)
	loaded, gotHash, err := supervisor.Load(path)
	if err != nil || gotHash != hash {
		t.Fatalf("graph fixture plan binding: hash=%s err=%v", gotHash, err)
	}
	if err := validateLocalDAGPlan(loaded); err != nil {
		t.Fatalf("graph fixture must pass existing local validation: %v", err)
	}
	if _, err := supervisor.ReadDAGStatus(context.Background(), loaded, hash); err != nil {
		t.Fatalf("graph fixture must pass existing status reader: %v", err)
	}
	return e, path, loaded, hash
}

// supervisorGraphReadOnly runs the CLI and returns stdout, stderr and exit code.
// The fixture tree (Git state, contracts, marker, journal, timing sidecar and
// any new path) must be byte-, mode- and mtime-identical afterwards.
func supervisorGraphReadOnly(t *testing.T, e *e2eEnv, args ...string) (string, string, int) {
	t.Helper()
	before := supervisorStatusSnapshot(t, e.root)
	var stdout string
	code := 0
	stderr, _ := capture(t, func() int {
		old := os.Stderr
		os.Stderr = os.Stdout
		defer func() { os.Stderr = old }()
		stdout, code = capture(t, func() int { return cmdSupervisor(args) })
		return code
	})
	if !reflect.DeepEqual(before, supervisorStatusSnapshot(t, e.root)) {
		t.Fatal("graph status changed fixture paths, contents, modes or mtimes")
	}
	return stdout, stderr, code
}

func requireSupervisorGraph(t *testing.T, out, stderr string, code int) {
	t.Helper()
	if code != 0 {
		t.Fatalf("terminal graph must be accepted: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if out == "" || json.Valid([]byte(out)) || strings.ContainsAny(out, "\x1b\r\x00") {
		t.Fatalf("captured graph must be stable plain terminal text: %q", out)
	}
	if strings.Contains(out, "<html") || strings.Contains(out, "```mermaid") {
		t.Fatalf("graph must be drawn in the terminal: %q", out)
	}
}

func requireSupervisorGraphAttempt(t *testing.T, out string, attempt int) {
	t.Helper()
	// Accept compact counters or a labelled count; card borders/spacing are free.
	pattern := fmt.Sprintf(`(?i)(\b%d\s*/\s*1\b|\battempt\s*[:=]?\s*%d\b)`, attempt, attempt)
	if !regexp.MustCompile(pattern).MatchString(out) {
		t.Fatalf("graph lacks attempt %d metadata: %q", attempt, out)
	}
}

func requireSupervisorGraphWidth(t *testing.T, out string, width int) {
	t.Helper()
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if n := utf8.RuneCountInString(line); n > width {
			t.Fatalf("graph line %d has %d columns, exceeding width %d: %q", i+1, n, width, out)
		}
	}
}

func requireSupervisorGraphNodes(t *testing.T, out string, p supervisor.Plan) {
	t.Helper()
	for _, task := range p.Tasks {
		if !strings.Contains(out, task.Task) {
			t.Fatalf("graph dropped node %q: %q", task.Task, out)
		}
	}
}

func TestSupervisorGraphExplicitTablePreservesDefault(t *testing.T) {
	e, path, _, _ := supervisorStatusFixture(t)
	want, wantErr, wantCode := supervisorGraphReadOnly(t, e, "status", "--plan", path)
	if wantCode != 0 || want == "" {
		t.Fatalf("default table control must succeed on base: exit=%d stdout=%q stderr=%q", wantCode, want, wantErr)
	}
	got, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "table")
	if code != 0 || got != want || stderr != wantErr {
		t.Fatalf("explicit table must match default: default=(%d,%q,%q) table=(%d,%q,%q)", wantCode, want, wantErr, code, got, stderr)
	}
}

// Missing journal: root ready, fanout children and join pending. The controller
// stays held throughout; status must neither need nor disturb it.
func TestSupervisorGraphFanoutJoinStableReadOnly(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	t.Setenv("NO_COLOR", "1")
	e, path, p, hash := supervisorGraphFixture(t, map[string][]string{
		"task": {}, "left": {"task"}, "right": {"task"}, "join": {"left", "right"},
	})
	controller, err := run.AcquireController(e.wt, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	out, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
	requireSupervisorGraph(t, out, stderr, code)
	again, againErr, againCode := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
	if againCode != 0 || again != out || againErr != stderr {
		t.Fatalf("unchanged snapshot must have stable node/edge order: first=%q second=%q stderr=%q", out, again, againErr)
	}
	requireSupervisorGraphNodes(t, out, p)
	requireSupervisorGraphWidth(t, out, 120)
	root, left, right, join := strings.Index(out, "task"), strings.Index(out, "left"), strings.Index(out, "right"), strings.Index(out, "join")
	if root >= left || root >= right || left >= join || right >= join {
		t.Fatalf("root, parallel children and join must appear top-to-bottom by layer: %q", out)
	}
	for _, metadata := range []string{"ready", "pending", "action"} {
		if !strings.Contains(strings.ToLower(out), metadata) {
			t.Fatalf("graph lacks %q metadata: %q", metadata, out)
		}
	}
	requireSupervisorGraphAttempt(t, out, 0)
	table, _, tableCode := supervisorGraphReadOnly(t, e, "status", "--plan", path)
	if tableCode != 0 || out == table {
		t.Fatalf("graph must draw dependencies beyond the table: graph=%q table=%q", out, table)
	}
	if err := controller.Check(); err != nil {
		t.Fatalf("graph status invalidated active controller: %v", err)
	}
}

// Two plans with identical node metadata and layers but different edges must
// draw differently at every width, so narrow output cannot silently drop edges.
func TestSupervisorGraphDistinguishesDependenciesWithIdenticalLayers(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("COLUMNS", strconv.Itoa(width))
			e, path, p, hash := supervisorGraphFixture(t, map[string][]string{
				"task": {}, "left": {"task"}, "right": {"task"}, "join": {"left", "right"}, "leaf": {"right"},
			})
			before, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
			if err != nil {
				t.Fatal(err)
			}
			first, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
			requireSupervisorGraph(t, first, stderr, code)
			requireSupervisorGraphNodes(t, first, p)
			requireSupervisorGraphWidth(t, first, width)
			p.Tasks[3].DependsOn = []string{"left"}
			p.Tasks[4].DependsOn = []string{"left", "right"}
			nextHash := bindDAGPlan(t, e, path, p)
			p, _, err = supervisor.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := supervisor.ReadDAGStatus(context.Background(), p, nextHash)
			if err != nil || !reflect.DeepEqual(before.Nodes, after.Nodes) {
				t.Fatalf("topology control must keep all table node metadata identical: before=%+v after=%+v err=%v", before.Nodes, after.Nodes, err)
			}
			second, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
			requireSupervisorGraph(t, second, stderr, code)
			requireSupervisorGraphNodes(t, second, p)
			requireSupervisorGraphWidth(t, second, width)
			// A plan-hash caption alone must not count as drawing the changed edges.
			first = strings.ReplaceAll(first, hash, "PLAN_HASH")
			second = strings.ReplaceAll(second, nextHash, "PLAN_HASH")
			if first == second {
				t.Fatalf("different dependencies with identical layers must draw different graphs at width %d: %q", width, first)
			}
		})
	}
}

func TestSupervisorGraphPartialTailIsChanging(t *testing.T) {
	e, path, p, hash := supervisorGraphFixture(t, map[string][]string{"task": {}, "left": {"task"}})
	supervisorStatusJournal(t, e, p, hash, `{"seq":2,"phase":"intent"`)
	started := time.Now()
	out, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("partial graph snapshot was not bounded: %s", elapsed)
	}
	requireSupervisorGraph(t, out, stderr, code)
	for _, want := range []string{"changing", "task", "ready", "left", "pending"} {
		if !strings.Contains(out, want) {
			t.Fatalf("partial tail must preserve prefix nodes and mark changing; missing %q: %q", want, out)
		}
	}
}

func TestSupervisorGraphRejectsCompletedJournalCorruption(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"malformed_line", "not-json\n"},
		{"sequence_gap", `{"seq":3,"task":"task","phase":"intent","operation":"prepare","op_id":"` + strings.Repeat("a", 64) + `"}` + "\n"},
		{"malformed_before_partial", "not-json\n" + `{"seq":3`},
		{"invalid_lifecycle_identity", `{"seq":2,"phase":"lifecycle","operation":"invocation_return","data":{}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, path, p, hash := supervisorGraphFixture(t, map[string][]string{"task": {}})
			supervisorStatusJournal(t, e, p, hash, tc.tail)
			// Control: the existing table reader already rejects this journal.
			tableOut, tableErr, tableCode := supervisorGraphReadOnly(t, e, "status", "--plan", path)
			if tableCode != 1 || tableOut != "" || tableErr == "" {
				t.Fatalf("fixture must be a corrupt complete journal for the base reader: exit=%d stdout=%q stderr=%q", tableCode, tableOut, tableErr)
			}
			out, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
			if code != 1 || out != "" || stderr == "" || strings.Contains(stderr, "flag provided but not defined") {
				t.Fatalf("corrupt journal must fail observation, not flag parsing: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

// A known root failure marks the root failed and blocks both fanout paths and
// the join; the graph must show every node and the reconciliation state.
func TestSupervisorGraphFailureBlocksBothJoinPaths(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	e, path, p, hash := supervisorGraphFixture(t, map[string][]string{
		"task": {}, "left": {"task"}, "right": {"task"}, "join": {"left", "right"},
	})
	opID := strings.Repeat("a", 64)
	supervisorStatusJournal(t, e, p, hash,
		`{"seq":2,"task":"task","op_id":"`+opID+`","phase":"intent","operation":"prepare"}`+"\n"+
			`{"seq":3,"task":"task","op_id":"`+opID+`","phase":"error","operation":"prepare","error":"fixture preparation failed"}`+"\n")
	strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
	if err != nil || !strict.Terminal() || strict.Status != "reconciliation" {
		t.Fatalf("failure fixture must be a terminal reconciliation snapshot: %+v err=%v", strict, err)
	}
	for i, state := range []string{"failed", "blocked", "blocked", "blocked"} {
		if strict.Nodes[i].Task != p.Tasks[i].Task || strict.Nodes[i].State != state {
			t.Fatalf("failure fixture node states: %+v", strict.Nodes)
		}
	}
	out, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
	requireSupervisorGraph(t, out, stderr, code)
	requireSupervisorGraphNodes(t, out, p)
	requireSupervisorGraphWidth(t, out, 80)
	for _, want := range []string{"failed", "blocked", "reconciliation"} {
		if !strings.Contains(out, want) {
			t.Fatalf("failure graph lacks %q: %q", want, out)
		}
	}
	if n := strings.Count(out, "blocked"); n < 3 {
		t.Fatalf("both fanout paths and the join must be shown blocked (got %d): %q", n, out)
	}
}

// A global cancel blocks the root and both join paths. The pipe is not a TTY,
// so watch emits one plain terminal frame and exits.
func TestSupervisorGraphWatchTerminalSnapshotUsesPlainPipeOutput(t *testing.T) {
	e, path, p, hash := supervisorGraphFixture(t, map[string][]string{
		"task": {}, "left": {"task"}, "right": {"task"}, "join": {"left", "right"},
	})
	// The durable cancel reason carries hostile terminal bytes; whatever the
	// graph shows must stay plain text (no ESC, CR, NUL or BEL reaches stdout).
	supervisorStatusJournal(t, e, p, hash, `{"seq":2,"phase":"control","operation":"cancel","error":"explicit cancellation \u001b[31mred\u001b[0m\u0007\r"}`+"\n")
	strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
	if err != nil || !strict.Terminal() || strict.Status != "reconciliation" {
		t.Fatalf("cancel fixture must be a terminal reconciliation snapshot: %+v err=%v", strict, err)
	}
	for _, n := range strict.Nodes {
		if n.State != "blocked" {
			t.Fatalf("cancel fixture must block every node: %+v", strict.Nodes)
		}
	}
	out, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph", "--watch")
	requireSupervisorGraph(t, out, stderr, code)
	requireSupervisorGraphNodes(t, out, p)
	if !strings.Contains(out, "reconciliation") || !strings.Contains(out, "blocked") {
		t.Fatalf("terminal watch frame must show cancellation state: %q", out)
	}
	if strings.ContainsAny(out, "\x07") {
		t.Fatalf("terminal watch frame leaked control bytes: %q", out)
	}
}

func TestSupervisorGraphRejectsInvalidOptionsBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tail    string
		missing bool
		args    []string
	}{
		{name: "unknown_view", args: []string{"--view", "unknown"}},
		{name: "empty_view", args: []string{"--view="}},
		{name: "graph_json", args: []string{"--view", "graph", "--json"}},
		{name: "zero_seq", args: []string{"--view", "graph", "--seq", "0"}},
		{name: "negative_seq", args: []string{"--view", "graph", "--seq", "-1"}},
		{name: "non_integer_seq", args: []string{"--view", "graph", "--seq", "invalid"}},
		{name: "seq_watch", args: []string{"--view", "graph", "--seq", "1", "--watch"}},
		{name: "seq_json", args: []string{"--seq", "1", "--json"}},
		// A complete header row exists, so a missing requires-graph check would
		// replay seq 1 as a table and exit 0 instead of rejecting.
		{name: "seq_without_view", args: []string{"--seq", "1"}},
		{name: "seq_table_view", args: []string{"--view", "table", "--seq", "1"}},
		{name: "seq_beyond_entries", args: []string{"--view", "graph", "--seq", "2"}},
		{name: "seq_partial_tail_not_complete", tail: `{"seq":2,"phase":"intent"`, args: []string{"--view", "graph", "--seq", "2"}},
		{name: "seq_missing_journal", missing: true, args: []string{"--view", "graph", "--seq", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, path, p, hash := supervisorGraphFixture(t, map[string][]string{"task": {}})
			if !tc.missing {
				supervisorStatusJournal(t, e, p, hash, tc.tail)
			}
			args := append([]string{"status", "--plan", path}, tc.args...)
			out, stderr, code := supervisorGraphReadOnly(t, e, args...)
			if code == 0 || out != "" || stderr == "" || strings.Contains(stderr, "flag provided but not defined") {
				t.Fatalf("recognized invalid options must be rejected before rendering/writes: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

// The lifecycle rows deliberately shift task-event sequence numbers. A selector
// counting only task events (or omitting the header) chooses the wrong states.
func TestSupervisorGraphSequenceIncludesHeaderAndLifecycle(t *testing.T) {
	e, path, p, hash := supervisorGraphFixture(t, map[string][]string{"task": {}})
	loc, err := run.Locate(e.wt)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	event := supervisor.GraphInvocationEvent{GraphInvocationIdentity: supervisor.GraphInvocationIdentity{
		InvocationID: strings.Repeat("b", 64), Run: p.Run, PlanHash: hash, Repo: p.Repo,
		MarkerDigest: graphHash(marker), PID: os.Getpid(), PIDStart: 1,
	}}
	task := p.Tasks[0]
	a := supervisor.PreparedAttempt{
		Number: 1, Run: p.Run, PlanHash: hash, Task: task.Task, Kind: "action",
		Attempt: "graph-attempt", ReceiptID: "graph-receipt", ContractHash: task.ContractHash,
		Worktree: e.wt, SourceRevision: task.Base, SourceDigest: graphHash([]byte("source")),
		InputDigest: supervisor.InputDigest(nil), Argv: task.Action.Argv,
	}
	a.Action = &supervisor.ActionReceipt{
		DaemonID: "fixture-daemon", Version: 1, Run: a.Run, PlanHash: hash, Task: a.Task,
		Attempt: a.Attempt, ReceiptID: a.ReceiptID, ContractHash: a.ContractHash,
		SourceRevision: a.SourceRevision, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest,
		ArgvDigest: graphHash(dagEncode(task.Action.Argv)), SandboxDigest: graphHash(dagEncode(task.Action.Sandbox)),
	}
	execution := supervisor.ExecutionResult{
		Dispatch:    supervisor.Dispatch{Task: a.Task, Attempt: a.Attempt, Decision: a.ReceiptID, Dispatch: a.ReceiptID, Worktree: a.Worktree, OrcaTask: "fixture:graph"},
		Observation: supervisor.Observation{State: supervisor.Failed, Evidence: graphHash([]byte("executed"))},
	}
	terminal := supervisor.TerminalResult{Outcome: supervisor.Outcome{
		State: routing.WaitHuman, Reason: "known fixture failure",
		ExecutionDigest: execution.Observation.Evidence, CheckpointDigest: graphHash([]byte("checkpoint")),
	}}
	exit := 0
	returned := event
	returned.Result = &supervisor.GraphResult{Status: "partial", State: routing.WaitHuman, Checks: 1, Failed: []supervisor.TaskStatus{{Task: "task", Reason: "known fixture failure"}}}
	returned.CLIExit, returned.Outcome = &exit, "graph_returned"
	entry := func(seq int, phase, operation, attempt, opID string, data any) string {
		row := map[string]any{"seq": seq, "phase": phase, "operation": operation}
		if opID != "" {
			row["task"], row["op_id"] = "task", opID
		}
		if attempt != "" {
			row["attempt"] = attempt
		}
		if data != nil {
			row["data"] = data
		}
		return string(dagEncode(row)) + "\n"
	}
	prepareID, executeID, finalizeID := strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64)
	tail := entry(2, "lifecycle", "invocation_start", "", "", event) +
		entry(3, "intent", "prepare", "", prepareID, nil) +
		entry(4, "result", "prepare", "", prepareID, a) +
		entry(5, "intent", "execute", a.Attempt, executeID, nil) +
		entry(6, "result", "execute", a.Attempt, executeID, execution) +
		entry(7, "intent", "finalize", a.Attempt, finalizeID, nil) +
		entry(8, "result", "finalize", a.Attempt, finalizeID, terminal) +
		entry(9, "lifecycle", "invocation_return", "", "", returned)
	supervisorStatusJournal(t, e, p, hash, tail)
	journal := loc.Marker + ".graph3-" + hash + ".jsonl"
	full, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	states := []string{"ready", "ready", "preparing", "running", "running", "finalizing", "finalizing", "wait", "wait"}
	lines := strings.SplitAfter(string(full), "\n")
	if len(lines) != len(states)+1 || lines[len(states)] != "" {
		t.Fatalf("fixture must contain nine complete entries, including header/lifecycle: %q", full)
	}
	controller, err := run.AcquireController(e.wt, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	for i, state := range states {
		t.Run(strconv.Itoa(i+1)+"_"+state, func(t *testing.T) {
			prefix := strings.Join(lines[:i+1], "")
			if err := os.WriteFile(journal, []byte(prefix), 0600); err != nil {
				t.Fatal(err)
			}
			strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
			attempt := 0
			if i >= 3 {
				attempt = 1
			}
			if err != nil || len(strict.Nodes) != 1 || strict.Nodes[0].State != state || strict.Nodes[0].Attempt != attempt || strict.InvocationActive != (i >= 1 && i < 8) {
				t.Fatalf("strict prefix fixture seq=%d: %+v err=%v", i+1, strict, err)
			}
			prefixOut, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph")
			requireSupervisorGraph(t, prefixOut, stderr, code)
			if err := os.WriteFile(journal, full, 0600); err != nil {
				t.Fatal(err)
			}
			out, stderr, code := supervisorGraphReadOnly(t, e, "status", "--plan", path, "--view", "graph", "--seq", strconv.Itoa(i+1))
			requireSupervisorGraph(t, out, stderr, code)
			if !strings.Contains(out, state) || !strings.Contains(out, "task") {
				t.Fatalf("seq=%d must show strict prefix state %s: %q", i+1, state, out)
			}
			requireSupervisorGraphAttempt(t, out, attempt)
			// Compare the node state/counter, allowing a replay sequence caption.
			for _, other := range []string{"ready", "preparing", "running", "finalizing", "wait"} {
				word := regexp.MustCompile(`\b` + other + `\b`)
				if word.MatchString(out) != word.MatchString(prefixOut) {
					t.Fatalf("seq=%d differs from strict live prefix: replay=%q prefix=%q", i+1, out, prefixOut)
				}
			}
			if strings.Contains(out, "changing") {
				t.Fatalf("complete historical prefix must not be changing: %q", out)
			}
			if err := controller.Check(); err != nil {
				t.Fatalf("seq=%d observation invalidated held controller: %v", i+1, err)
			}
		})
	}
}

// Direct, bounded check of the display-flag validator. Every rejection must be
// decided here, before any plan load or status loop; a seq+watch combination
// that slipped through would otherwise poll a non-terminal snapshot forever.
func TestSupervisorStatusViewOptionsTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		view    string
		json    bool
		watch   bool
		seqSet  bool
		seqText string
		want    int
		err     string
	}{
		{name: "default_table", view: "table"},
		{name: "table_json", view: "table", json: true},
		{name: "table_watch", view: "table", watch: true},
		{name: "table_json_watch", view: "table", json: true, watch: true},
		{name: "graph", view: "graph"},
		{name: "graph_watch", view: "graph", watch: true},
		{name: "graph_seq_1", view: "graph", seqSet: true, seqText: "1", want: 1},
		{name: "graph_seq_42", view: "graph", seqSet: true, seqText: "42", want: 42},
		{name: "empty_view", view: "", err: "invalid --view"},
		{name: "unknown_view", view: "unknown", err: "invalid --view"},
		{name: "uppercase_view", view: "Graph", err: "invalid --view"},
		{name: "unknown_view_seq", view: "tree", seqSet: true, seqText: "1", err: "invalid --view"},
		{name: "graph_json", view: "graph", json: true, err: "--view graph cannot be combined with --json"},
		{name: "graph_json_seq", view: "graph", json: true, seqSet: true, seqText: "1", err: "--view graph cannot be combined with --json"},
		{name: "seq_json", view: "table", json: true, seqSet: true, seqText: "1", err: "--seq cannot be combined with --json"},
		{name: "seq_watch_graph", view: "graph", watch: true, seqSet: true, seqText: "1", err: "--seq cannot be combined with --watch"},
		{name: "seq_watch_table", view: "table", watch: true, seqSet: true, seqText: "1", err: "--seq cannot be combined with --watch"},
		{name: "seq_without_graph", view: "table", seqSet: true, seqText: "1", err: "--seq requires --view graph"},
		{name: "seq_without_graph_invalid_value", view: "table", seqSet: true, seqText: "0", err: "--seq requires --view graph"},
		{name: "seq_zero", view: "graph", seqSet: true, seqText: "0", err: "invalid --seq"},
		{name: "seq_negative", view: "graph", seqSet: true, seqText: "-1", err: "invalid --seq"},
		{name: "seq_non_integer", view: "graph", seqSet: true, seqText: "invalid", err: "invalid --seq"},
		{name: "seq_fraction", view: "graph", seqSet: true, seqText: "1.5", err: "invalid --seq"},
		{name: "seq_empty", view: "graph", seqSet: true, seqText: "", err: "invalid --seq"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := supervisorStatusViewOptions(tc.view, tc.json, tc.watch, tc.seqSet, tc.seqText)
			if tc.err == "" {
				if err != nil || got != tc.want {
					t.Fatalf("valid options rejected or wrong seq: got=%d err=%v want=%d", got, err, tc.want)
				}
				return
			}
			if err == nil || got != 0 || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("invalid options must be rejected with %q: got=%d err=%v", tc.err, got, err)
			}
		})
	}
}
