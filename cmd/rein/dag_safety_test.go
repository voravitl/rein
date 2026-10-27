package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func TestActionAggregateOutputBounds(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("12"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		bytes   int64
		entries int
		want    string
	}{
		{"exact_sum_and_count", 4, 2, ""},
		{"aggregate_bytes_only", 3, 2, "action output byte bound exceeded"},
		{"entry_count_only", 4, 1, "action output entry count exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateActionOutputs(root, &supervisor.ActionSpec{MaxOutputFiles: tc.entries, MaxOutputTotalBytes: tc.bytes})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("exact-bound positive control: %v", err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("want specific %q, got %v", tc.want, err)
			}
		})
	}
}

// This component fixture persists real evidence and calls finalizeAction. Only
// the Docker control plane is simulated; its attach runs a real local process.
func TestActionFinalizationRejectsFailedEvidence(t *testing.T) {
	for _, condition := range []string{"successful", "nonzero_exit", "truncated", "timed_out", "cancelled", "failed_observation"} {
		t.Run(condition, func(t *testing.T) {
			env, planPath, p, _ := localDAGWorkerFixture(t)
			index := os.Getenv("PIPELINE_CONTRACTS")
			_, task, a, inspection := policyFixture(t)
			t.Setenv("PIPELINE_CONTRACTS", index)
			task.ContractHash, task.Base, task.MaxAttempts = env.hash, p.Tasks[0].Base, 1
			task.Verify = [][]string{{"/bin/true"}}
			p.Tasks = []supervisor.Task{task}
			hash := bindDAGPlan(t, env, planPath, p)
			a.Run, a.PlanHash, a.Kind = p.Run, hash, "action"
			a.Worktree, a.ContractHash, a.SourceRevision = env.wt, task.ContractHash, task.Base
			a.SourceDigest, a.InputDigest = graphHash([]byte("component source")), supervisor.InputDigest(nil)
			a.Argv = task.Action.Argv
			a.Action = &supervisor.ActionReceipt{DaemonID: "simulated-daemon", Version: 1, Run: a.Run, PlanHash: a.PlanHash,
				Task: a.Task, Attempt: a.Attempt, ReceiptID: a.ReceiptID, ContractHash: a.ContractHash,
				SourceRevision: a.SourceRevision, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest,
				ArgvDigest: graphHash(dagEncode(task.Action.Argv)), SandboxDigest: graphHash(dagEncode(task.Action.Sandbox))}
			root := dagAttemptRoot(p, a)
			for i, name := range []string{"source", "inputs", "out"} {
				if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
					t.Fatal(err)
				}
				inspection.Mounts[i].Source = filepath.Join(root, name)
			}
			inspection.Config.Labels["rein.plan"] = hash
			inspection.State.StartedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
			inspection.State.FinishedAt = time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
			e := actionExecution{Run: p.Run, PlanHash: hash, Task: task.Task, Attempt: a.Attempt, ReceiptID: a.ReceiptID,
				SourceDigest: a.SourceDigest, InputDigest: a.InputDigest, Name: "rein-action-" + a.Attempt,
				ContainerID: inspection.ID, Argv: task.Action.Argv, Gone: true, Removed: true, Inspection: inspection}
			observation := supervisor.Completed
			// Keep the observation Completed for each persisted failure flag so a
			// second clause cannot mask removal of the specific condition.
			switch condition {
			case "nonzero_exit":
				e.ExitCode, e.Inspection.State.ExitCode = 17, 17
			case "truncated":
				e.Truncated = true
			case "timed_out":
				e.TimedOut = true
			case "cancelled":
				e.Cancelled = true
			case "failed_observation":
				observation = supervisor.Failed
			}
			raw := dagEncode(e)
			if err := supervisor.WriteImmutable(filepath.Join(root, "action-execution.json"), raw, 0400); err != nil {
				t.Fatal(err)
			}
			if _, loaded, err := loadActionExecution(p, task, a); err != nil || string(loaded) != string(raw) {
				t.Fatalf("identity/cleanup/policy/timestamp positive control: %v", err)
			}
			r := supervisor.ExecutionResult{Observation: supervisor.Observation{State: observation, Evidence: graphHash(raw)}, Evidence: raw, Cancelled: e.Cancelled}
			control, err := run.AcquireController(p.Repo, hash)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			fake := filepath.Join(env.root, "simulated-docker-verifier")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nset -eu\ntest \"$5\" = start\ntest \"$6\" = --attach\ntest \"$#\" -eq 7\nprintf ran > \"$0.ran\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			backendCalls, creates, removes := 0, 0, 0
			current := inspection
			backend := actionBackend
			actionBackend = func() (actionDocker, error) {
				backendCalls++
				return actionDocker{binary: fake, socket: "/simulated/socket", config: "/simulated/config", callFn: func(ctx context.Context, args ...string) ([]byte, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					switch args[0] {
					case "info":
						return []byte(a.Action.DaemonID), nil
					case "create":
						creates++
						for i, arg := range args {
							if arg == "--name" && i+1 < len(args) {
								current.Name = "/" + args[i+1]
							}
						}
						current.ID = graphHash([]byte(current.Name))
						current.Config.Entrypoint, current.Config.Cmd = task.Verify[0][:1], task.Verify[0][1:]
						current.Mounts[2].RW = false
						current.State.ExitCode = 0
						return []byte(current.ID), nil
					case "inspect":
						return dagEncode([]actionInspect{current}), nil
					case "rm":
						removes++
						return nil, nil
					case "ps":
						return nil, nil
					default:
						return nil, errors.New("unexpected simulated verifier operation")
					}
				}}, nil
			}
			t.Cleanup(func() { actionBackend = backend })
			out, err := finalizeAction(context.Background(), control, p, task, a, r)
			if err != nil {
				t.Fatalf("must reach finalization decision without unrelated authority/backend error: %v", err)
			}
			if out.ExecutionDigest != graphHash(raw) || out.CheckpointDigest != a.SourceDigest {
				t.Fatalf("finalization evidence binding changed: %+v", out)
			}
			verificationPath := filepath.Join(root, "action-verification.json")
			if condition != "successful" {
				if out.State != routing.WaitHuman || out.Reason != "action failed, exceeded bounds or was cancelled" || out.VerificationDigest != "" {
					t.Fatalf("%s must stop at failed-evidence gate: %+v", condition, out)
				}
				if backendCalls != 0 || creates != 0 || removes != 0 {
					t.Fatalf("failed evidence reached verifier/backend: calls=%d creates=%d removes=%d", backendCalls, creates, removes)
				}
				if _, err := os.Stat(verificationPath); !os.IsNotExist(err) {
					t.Fatalf("failed evidence published successful verification: %v", err)
				}
				if _, err := os.Stat(fake + ".ran"); !os.IsNotExist(err) {
					t.Fatalf("failed evidence executed verifier: %v", err)
				}
				return
			}
			if out.State != routing.WaitNone || out.Reason != "" || backendCalls != 1 || creates != 1 || removes != 1 {
				t.Fatalf("matching positive control did not verify/clean up: %+v calls=%d creates=%d removes=%d", out, backendCalls, creates, removes)
			}
			verified, err := supervisor.ReadArtifact(root, "action-verification.json", 1<<20)
			var checks []actionExecution
			if err != nil || json.Unmarshal(verified, &checks) != nil || len(checks) != 1 || out.VerificationDigest != graphHash(verified) || !checks[0].Gone || !checks[0].Removed || checks[0].ExitCode != 0 {
				t.Fatalf("positive verification artifact: %q %v", verified, err)
			}
			if b, err := os.ReadFile(fake + ".ran"); err != nil || strings.TrimSpace(string(b)) != "ran" {
				t.Fatalf("positive simulated verifier did not execute: %q %v", b, err)
			}
		})
	}
}
