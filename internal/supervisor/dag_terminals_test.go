package supervisor

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/run"
)

// ReadDAGTerminals reads what RunDAG actually journals (finalize results), so
// integrate sees the same done/wait states as status.
func TestReadDAGTerminalsFromRealRun(t *testing.T) {
	p, h := dagFixture(t, 2, map[string][]string{"A": {}, "B": {}, "C": {"A", "B"}})
	ops := fakeDAGOps(t, p)
	execute := ops.Execute
	ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
		r, err := execute(ctx, c, task, a)
		if task.Task == "B" {
			r.Observation.State = Failed
		}
		return r, err
	}
	if _, err := RunDAG(context.Background(), p, h, ops); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDAGTerminals(context.Background(), p, h)
	if err != nil {
		t.Fatal(err)
	}
	if a := got["A"]; a.State != "done" || a.Attempt == "" || !validDigest(a.Outcome.CheckpointDigest) {
		t.Fatalf("A: %+v", a)
	}
	if b := got["B"]; b.State != "wait" || b.Outcome.Reason != "known fixture failure" {
		t.Fatalf("B: %+v", b)
	}
	if c := got["C"]; c.State != "blocked" || c.Attempt != "" {
		t.Fatalf("C: %+v", c)
	}
}

func TestReadDAGTerminalsRefusesUnfinishedOrTamperedJournal(t *testing.T) {
	p, h := dagFixture(t, 1, map[string][]string{"A": {}})
	if _, err := ReadDAGTerminals(context.Background(), p, h); err == nil {
		t.Fatal("missing journal accepted")
	}
	if _, err := RunDAG(context.Background(), p, h, fakeDAGOps(t, p)); err != nil {
		t.Fatal(err)
	}
	loc, err := run.Locate(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	path := dagJournalPath(loc.Marker, h)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A partial trailing line means the supervisor is still writing.
	if err := os.WriteFile(path, append(append([]byte(nil), raw...), `{"seq":`...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDAGTerminals(context.Background(), p, h); err == nil || !strings.Contains(err.Error(), "still being written") {
		t.Fatalf("partial journal: %v", err)
	}
	// A forged terminal line that skips the intent is rejected by replay.
	forged := append(append([]byte(nil), raw...), []byte(`{"seq":999,"task":"A","phase":"result","operation":"terminal","data":{"outcome":{"state":"done"}}}`+"\n")...)
	if err := os.WriteFile(path, forged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDAGTerminals(context.Background(), p, h); err == nil {
		t.Fatal("forged terminal accepted")
	}
}
