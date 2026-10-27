package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/run"
)

func safetyDAGJournal(t *testing.T, p Plan, hash string) *dagLog {
	t.Helper()
	l := &dagLog{path: filepath.Join(t.TempDir(), "journal.jsonl")}
	if _, err := l.load(p, hash); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestDAGReplayExactIntentResultPairing(t *testing.T) {
	for _, phase := range []string{"result", "error"} {
		t.Run(phase, func(t *testing.T) {
			for _, mismatch := range []string{"matching", "op_id", "operation", "attempt"} {
				t.Run(mismatch, func(t *testing.T) {
					p, hash := dagFixture(t, 1, map[string][]string{"A": {}})
					l := safetyDAGJournal(t, p, hash)
					intent := dagEntry{Phase: "intent", Operation: "prepare", Task: "A", OpID: digestText("operation")}
					if err := l.append(intent); err != nil {
						t.Fatal(err)
					}
					e := intent
					e.Phase = phase
					if phase == "error" {
						e.Error = "known failed operation"
					} else {
						a, err := fakeDAGOps(t, p).Prepare(context.Background(), nil, p.Tasks[0], nil, nil, nil)
						if err != nil {
							t.Fatal(err)
						}
						e.Data = mustJSON(a)
					}
					switch mismatch {
					case "op_id":
						e.OpID = digestText("different operation")
					case "operation":
						e.Operation = "execute"
					case "attempt":
						e.Attempt = "different-attempt"
					}
					if err := l.append(e); err != nil {
						t.Fatal(err)
					}
					// Observation exercises pairing without a later pending/failed
					// reconciliation masking it, especially for error entries.
					states, err := l.read(p, hash, true)
					if mismatch != "matching" {
						if err == nil || err.Error() != "DAG result lacks exact intent" {
							t.Fatalf("%s mismatch must fail exact pairing: %v", mismatch, err)
						}
						return
					}
					if err != nil {
						t.Fatalf("matching %s positive control: %v", phase, err)
					}
					want := ""
					if phase == "result" {
						want = "prepared"
					}
					if states["A"].Stage != want {
						t.Fatalf("matching result stage %q, want %q", states["A"].Stage, want)
					}
				})
			}
		})
	}
}

func TestDAGStandaloneCancelFenceBeforeCallbacks(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "success"
		if cancelled {
			name = "standalone_cancel"
		}
		t.Run(name, func(t *testing.T) {
			p, hash := dagFixture(t, 1, map[string][]string{"A": {}})
			loc, err := run.Locate(p.Repo)
			if err != nil {
				t.Fatal(err)
			}
			l := &dagLog{path: dagJournalPath(loc.Marker, hash)}
			if _, err := l.load(p, hash); err != nil {
				t.Fatal(err)
			}
			if cancelled {
				if err := l.append(dagEntry{Phase: "control", Operation: "cancel", Error: "explicit cancellation"}); err != nil {
					t.Fatal(err)
				}
			}
			ops := fakeDAGOps(t, p)
			prepares, executes := 0, 0
			prepare, execute := ops.Prepare, ops.Execute
			ops.Prepare = func(ctx context.Context, c *run.Controller, task Task, inputs []ResolvedInput, prior *PreparedAttempt, terminal *TerminalResult) (PreparedAttempt, error) {
				prepares++
				return prepare(ctx, c, task, inputs, prior, terminal)
			}
			ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
				executes++
				return execute(ctx, c, task, a)
			}
			result, err := RunDAG(context.Background(), p, hash, ops)
			if cancelled {
				if err == nil || err.Error() != "DAG has failed or unresolved side effects; explicit reconciliation required" || result.Status != "reconciliation" {
					t.Fatalf("standalone cancel: %+v %v", result, err)
				}
				if prepares != 0 || executes != 0 {
					t.Fatalf("cancel admitted callbacks: prepare=%d execute=%d", prepares, executes)
				}
			} else if err != nil || result.Status != "verified" || prepares != 1 || executes != 1 {
				t.Fatalf("success control: %+v %v prepare=%d execute=%d", result, err, prepares, executes)
			}
		})
	}
}

func TestDAGActionReceiptDigestsBeforeExecute(t *testing.T) {
	for _, field := range []string{"matching", "argv", "sandbox"} {
		t.Run(field, func(t *testing.T) {
			p, hash := dagFixture(t, 1, map[string][]string{"A": {}})
			ops := fakeDAGOps(t, p)
			prepare, execute := ops.Prepare, ops.Execute
			executed := false
			ops.Prepare = func(ctx context.Context, c *run.Controller, task Task, inputs []ResolvedInput, prior *PreparedAttempt, terminal *TerminalResult) (PreparedAttempt, error) {
				a, err := prepare(ctx, c, task, inputs, prior, terminal)
				if err == nil {
					if err := preparedIdentity(p, task, a); err != nil {
						t.Fatalf("intact receipt positive control: %v", err)
					}
					if field == "argv" {
						a.Action.ArgvDigest = digestText("other argv")
					} else if field == "sandbox" {
						a.Action.SandboxDigest = digestText("other sandbox")
					}
				}
				return a, err
			}
			ops.Execute = func(ctx context.Context, c *run.Controller, task Task, a PreparedAttempt) (ExecutionResult, error) {
				executed = true
				return execute(ctx, c, task, a)
			}
			result, err := RunDAG(context.Background(), p, hash, ops)
			if field == "matching" {
				if err != nil || !executed || result.Status != "verified" {
					t.Fatalf("matching receipt: %+v %v executed=%v", result, err, executed)
				}
			} else if err == nil || err.Error() != "invalid action receipt union or digest" || executed {
				t.Fatalf("%s receipt: %v executed=%v", field, err, executed)
			}
		})
	}
}

func TestDAGReplayDependencyOrder(t *testing.T) {
	for _, task := range []string{"A", "B"} {
		t.Run(task, func(t *testing.T) {
			p, hash := dagFixture(t, 2, map[string][]string{"A": {}, "B": {"A"}})
			l := safetyDAGJournal(t, p, hash)
			if err := l.append(dagEntry{Phase: "intent", Operation: "prepare", Task: task, OpID: digestText(task)}); err != nil {
				t.Fatal(err)
			}
			// Observe replay directly so pending-intent reconciliation cannot
			// mask the dependency check while A has not reached done.
			_, err := l.read(p, hash, true)
			if task == "A" {
				if err != nil {
					t.Fatalf("independent prepare positive control: %v", err)
				}
			} else if err == nil || err.Error() != "DAG journal dependency violation" {
				t.Fatalf("dependent prepare must fail dependency order: %v", err)
			}
		})
	}
}

func TestDAGReplayActiveAttemptBound(t *testing.T) {
	p, hash := dagFixture(t, 2, map[string][]string{"A": {}, "B": {}, "C": {}})
	l := safetyDAGJournal(t, p, hash)
	for i, task := range p.Tasks {
		if err := l.append(dagEntry{Phase: "intent", Operation: "prepare", Task: task.Task, OpID: digestText(task.Task)}); err != nil {
			t.Fatal(err)
		}
		_, err := l.read(p, hash, true)
		if i < p.Execution.MaxParallel {
			if err != nil {
				t.Fatalf("in-bound open intents positive control: %v", err)
			}
		} else if err == nil || err.Error() != "journal exceeds active attempt bound" {
			t.Fatalf("overflow must fail active bound, not end-of-log reconciliation: %v", err)
		}
	}
}

func TestArtifactMaterializeSecondReadHash(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		name := "intact"
		if corrupt {
			name = "between_verification_and_copy"
		}
		t.Run(name, func(t *testing.T) {
			store, source, m := artifactFixture(t)
			if err := os.WriteFile(filepath.Join(source, "result"), []byte("exact bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			ref, err := PublishArtifacts(store, source, m, []ArtifactOutput{{"result", "result", 64}})
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyArtifacts(store, ref); err != nil {
				t.Fatalf("intact pre-copy verification: %v", err)
			}
			dest := filepath.Join(t.TempDir(), "consumer")
			t.Cleanup(func() { _ = os.Chmod(dest, 0700) })
			reads := 0
			reader := func(root, name string, max int64) ([]byte, error) {
				reads++
				// This callback runs only after the helper's real VerifyArtifacts.
				if err := VerifyArtifacts(store, ref); err != nil {
					t.Fatalf("verification must pass before injected change: %v", err)
				}
				if corrupt {
					path := filepath.Join(root, name)
					if err := os.Chmod(path, 0600); err != nil {
						return nil, err
					}
					if err := os.WriteFile(path, []byte("EXACT BYTES"), 0600); err != nil {
						return nil, err
					}
				}
				return ReadArtifact(root, name, max)
			}
			err = materializeInputs(store, dest, []ResolvedInput{{"A", "result", "alias", ref}}, reader)
			if reads != 1 {
				t.Fatalf("copy reader calls=%d, want 1", reads)
			}
			if corrupt {
				if err == nil || err.Error() != "input changed during materialization" {
					t.Fatalf("second-read hash must reject same-size change: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dest, "alias")); !os.IsNotExist(err) {
					t.Fatalf("corrupted input reached consumer: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(filepath.Join(dest, "alias"))
				if err != nil || strings.TrimSpace(string(b)) != "exact bytes" {
					t.Fatalf("positive consumer bytes=%q: %v", b, err)
				}
			}
		})
	}
}
