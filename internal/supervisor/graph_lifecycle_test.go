package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

func TestGraphInvocationCompletionReuseAndFreshResume(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	ops.InvocationID = digestText("first")
	out, err := RunGraph(context.Background(), p, h, ops)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ReadGraphInvocation(p, h, ops.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Return == nil || *record.Return.CLIExit != 0 || record.Return.Outcome != "graph_returned" || record.Return.Result.State != routing.WaitHuman || out.State != routing.WaitHuman {
		t.Fatalf("invalid return: %+v", record)
	}
	if record.Start.PID != os.Getpid() || record.Start.PIDStart <= 0 || record.Start.GraphInvocationIdentity != record.Return.GraphInvocationIdentity {
		t.Fatalf("identity mismatch: %+v", record)
	}
	before := len(events)
	if _, err = RunGraph(context.Background(), p, h, ops); err == nil || !strings.Contains(err.Error(), "already used") || len(events) != before {
		t.Fatalf("reused invocation: %v %v", err, events)
	}
	ops.InvocationID = digestText("fresh")
	if _, err = RunGraph(context.Background(), p, h, ops); err != nil || len(events) != before {
		t.Fatalf("fresh resume repeated work: %v %v", err, events)
	}
	if _, err = ReadGraphInvocation(p, h, digestText("missing")); err == nil {
		t.Fatal("missing invocation accepted")
	}
}

func TestGraphInvocationRefusalDoesNotClearTaskError(t *testing.T) {
	p, h := graphFixture(t, nil)
	calls := 0
	var events []string
	ops := graphOps(p, &events)
	ops.InvocationID = digestText("refusal")
	ops.Prepare = func(context.Context, *run.Controller, Task) (*routing.AutoResult, error) {
		calls++
		return nil, errors.New("model calls disabled by policy")
	}
	_, err := RunGraph(context.Background(), p, h, ops)
	if err == nil {
		t.Fatal("refusal swallowed")
	}
	record, err := ReadGraphInvocation(p, h, ops.InvocationID)
	if err != nil || record.Return == nil || *record.Return.CLIExit != 1 || !strings.Contains(record.Return.Error, "calls disabled") {
		t.Fatalf("%+v %v", record, err)
	}
	ops.InvocationID = digestText("refusal restart")
	if _, err = RunGraph(context.Background(), p, h, ops); err == nil || calls != 1 {
		t.Fatalf("task error bypassed: %v calls=%d", err, calls)
	}
}

func TestGraphInvocationPanicLeavesStartAndPreservesRecoveryFence(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	ops.InvocationID = digestText("panic")
	ops.Launch = func(context.Context, *run.Controller, Task, *routing.Decision, []string) (Dispatch, error) {
		panic("unknown remote outcome")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected panic")
			}
		}()
		RunGraph(context.Background(), p, h, ops)
	}()
	record, err := ReadGraphInvocation(p, h, ops.InvocationID)
	if err != nil || record.Return != nil {
		t.Fatalf("panic fabricated RETURN: %+v %v", record, err)
	}
	ops.InvocationID = digestText("after panic")
	if _, err = RunGraph(context.Background(), p, h, ops); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unknown operation resumed: %v", err)
	}
}

func TestGraphInvocationMalformedNonceBeforeOperations(t *testing.T) {
	p, h := graphFixture(t, nil)
	for _, id := range []string{"bad", strings.Repeat("A", 64)} {
		var events []string
		ops := graphOps(p, &events)
		ops.InvocationID = id
		if _, err := RunGraph(context.Background(), p, h, ops); err == nil || len(events) != 0 {
			t.Fatalf("malformed nonce admitted: %v %v", err, events)
		}
	}
	_, _, log, err := invocationBinding(p, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(log.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed nonce wrote journal: %v", err)
	}
}

func TestGraphInvocationRejectsForgedLifecycle(t *testing.T) {
	p, h := graphFixture(t, nil)
	var events []string
	ops := graphOps(p, &events)
	ops.InvocationID = digestText("valid")
	if _, err := RunGraph(context.Background(), p, h, ops); err != nil {
		t.Fatal(err)
	}
	_, _, log, err := invocationBinding(p, h)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	for _, scenario := range []string{"different pid", "different marker", "duplicate start", "unknown field", "contradictory exit", "invalid task row"} {
		t.Run(scenario, func(t *testing.T) {
			changed := append([]string(nil), lines...)
			var e graphEntry
			if err := json.Unmarshal([]byte(changed[len(changed)-1]), &e); err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(e.Data, &event); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "different pid":
				event["pid"] = float64(os.Getpid() + 1)
			case "different marker":
				event["marker_digest"] = digestText("forged")
			case "unknown field":
				event["fake"] = true
			case "contradictory exit":
				event["cli_exit"] = 1
			case "duplicate start":
				changed = append(changed, changed[0])
			case "invalid task row":
				changed = append(changed, `{"task":"unknown","phase":"result","operation":"prepare"}`)
			}
			if scenario != "duplicate start" && scenario != "invalid task row" {
				e.Data, _ = json.Marshal(event)
				b, _ := json.Marshal(e)
				changed[len(changed)-1] = string(b)
			}
			if err := os.WriteFile(log.path, []byte(strings.Join(changed, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadGraphInvocation(p, h, ops.InvocationID); err == nil {
				t.Fatal("forged lifecycle accepted")
			}
		})
	}
}

func TestGraphInvocationPersistenceAndRevokedMarkerFailClosed(t *testing.T) {
	t.Run("start persistence", func(t *testing.T) {
		p, h := graphFixture(t, nil)
		c, err := run.AcquireController(p.Repo, h)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := (graphLog{path: t.TempDir()}).startInvocation(p, h, digestText("cannot append"), c); err == nil {
			t.Fatal("start persistence failure swallowed")
		}
	})
	t.Run("return persistence", func(t *testing.T) {
		p, h := graphFixture(t, nil)
		c, err := run.AcquireController(p.Repo, h)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _, log, err := invocationBinding(p, h)
		if err != nil {
			t.Fatal(err)
		}
		start, err := log.startInvocation(p, h, digestText("return failure"), c)
		if err != nil {
			t.Fatal(err)
		}
		if err := (graphLog{path: t.TempDir()}).returnInvocation(start, c, GraphResult{}, nil); err == nil {
			t.Fatal("return persistence failure swallowed")
		}
	})
	t.Run("revoked marker", func(t *testing.T) {
		p, h := graphFixture(t, nil)
		_, _, log, err := invocationBinding(p, h)
		if err != nil {
			t.Fatal(err)
		}
		loc, err := run.Locate(p.Repo)
		if err != nil {
			t.Fatal(err)
		}
		var events []string
		ops := graphOps(p, &events)
		ops.InvocationID = digestText("revoked")
		ops.Prepare = func(context.Context, *run.Controller, Task) (*routing.AutoResult, error) {
			if err := os.Remove(loc.Marker); err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("revoked")
		}
		if _, err := RunGraph(context.Background(), p, h, ops); err == nil {
			t.Fatal("revocation accepted")
		}
		raw, err := os.ReadFile(log.path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "invocation_return") {
			t.Fatal("revoked controller wrote RETURN")
		}
	})
}
