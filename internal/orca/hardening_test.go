package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type safetyRunner struct {
	t   *testing.T
	run func(string) ([]byte, error)
}

func (r safetyRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	k := name + " " + strings.Join(args, " ")
	if r.run == nil {
		r.t.Fatalf("unexpected command: %s", k)
	}
	return r.run(k)
}
func safetyMock(t *testing.T, f func(string) ([]byte, error)) {
	t.Helper()
	SetRunner(safetyRunner{t, f})
	t.Cleanup(func() { SetRunner(nil) })
}

func TestHardeningCloseGitFailures(t *testing.T) {
	for _, stage := range []string{"git status", "git branch", "git for-each-ref", "git rev-parse"} {
		for _, out := range []string{"", "refs/remotes/origin/main\n"} {
			t.Run(stage+out, func(t *testing.T) {
				safetyMock(t, func(k string) ([]byte, error) {
					if strings.HasPrefix(k, stage) {
						return []byte(out), errors.New("git failed")
					}
					switch {
					case strings.HasPrefix(k, "git status"):
						return nil, nil
					case strings.HasPrefix(k, "git branch"):
						return []byte("worker"), nil
					case strings.HasPrefix(k, "git for-each-ref"):
						return []byte("refs/heads/worker"), nil
					}
					t.Fatalf("unsafe command %s", k)
					return nil, nil
				})
				res, err := Close(context.Background(), CloseOptions{WorktreePath: t.TempDir(), TerminalHandle: "worker", DispatchID: "dispatch"})
				if err == nil || res.Safe {
					t.Fatalf("expected fail closed: %+v %v", res, err)
				}
			})
		}
	}
}
func TestHardeningCloseRefs(t *testing.T) {
	for _, ref := range []string{"", "fatal: broken", "refs/heads/worker", "refs/remotes/origin/main"} {
		t.Run(ref, func(t *testing.T) {
			safetyMock(t, func(k string) ([]byte, error) {
				switch {
				case strings.HasPrefix(k, "git status"):
					return nil, nil
				case strings.HasPrefix(k, "git branch"):
					return []byte("worker"), nil
				case strings.HasPrefix(k, "git for-each-ref"):
					return []byte(ref), nil
				case strings.HasPrefix(k, "git rev-parse"):
					return []byte("abc"), nil
				}
				t.Fatalf("unexpected %s", k)
				return nil, nil
			})
			res, err := Close(context.Background(), CloseOptions{WorktreePath: t.TempDir(), CheckOnly: true})
			safe := ref == "refs/remotes/origin/main"
			if (err == nil) != safe || res.Safe != safe {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}
func TestHardeningLaunchTokens(t *testing.T) {
	for _, token := range []string{"has space", "x;y", "x'y", "x\ny", "$(id)", "`id`"} {
		for _, field := range []string{"agent", "model"} {
			t.Run(field+token, func(t *testing.T) {
				safetyMock(t, nil)
				o := LaunchOptions{Name: "task", BaseBranch: "main", SpecContent: "task", RunID: "r", CoordHandle: "c", RepoID: "repo", RepoDir: t.TempDir(), Agent: "agy", Model: "valid"}
				if field == "agent" {
					o.Agent = token
				} else {
					o.Model = token
				}
				if _, err := Launch(context.Background(), o); err == nil {
					t.Fatal("accepted invalid token")
				}
			})
		}
	}
}
func TestHardeningLaunchModelAndFailure(t *testing.T) {
	for _, agent := range []string{"kiro", "kiro-cli", "agy", "antigravity", "claude", "codex"} {
		for _, scenario := range []string{"custom", "failure", "default"} {
			fail := scenario == "failure"
			t.Run(agent+"/"+scenario, func(t *testing.T) {
				wt := t.TempDir()
				model := "custom-model_1.2"
				if strings.HasPrefix(agent, "kiro") {
					model = "claude-opus-5.5"
				}
				inputModel := model
				if scenario == "default" {
					inputModel = ""
					switch agent {
					case "kiro", "kiro-cli":
						model = "claude-sonnet-5.5"
					case "agy", "antigravity":
						model = "gemini-3.8-flash-high"
					case "claude":
						model = "claude-sonnet-5-5"
					case "codex":
						model = "gpt-6.1-sol"
					}
				}
				started := false
				safetyMock(t, func(k string) ([]byte, error) {
					switch {
					case strings.HasPrefix(k, "orca worktree create"):
						b, _ := json.Marshal(map[string]any{"result": map[string]any{"worktree": map[string]any{"path": wt}}})
						return b, nil
					case strings.HasPrefix(k, "orca terminal create"):
						return []byte(`{"result":{"terminal":{"handle":"worker"}}}`), nil
					case strings.HasPrefix(k, "orca terminal list"):
						return []byte(`{"result":{"terminals":[]}}`), nil
					case strings.HasPrefix(k, "orca orchestration task-create"):
						return []byte(`{"result":{"task":{"id":"task"}}}`), nil
					case strings.HasPrefix(k, "orca orchestration dispatch"):
						return []byte(`{"result":{"dispatch":{"id":"dispatch"},"preamble":"task"}}`), nil
					case strings.HasPrefix(k, "git rev-parse --git-common-dir"):
						return []byte(wt), nil
					case strings.HasPrefix(k, "orca terminal send"), strings.HasPrefix(k, "orca orchestration worker-start"):
						started = true
						if !strings.Contains(k, "--model "+model) && !strings.Contains(k, "--model '"+model+"'") {
							t.Fatalf("model lost: %s", k)
						}
						if fail {
							return []byte("start failed"), errors.New("failed")
						}
						return []byte(`{"ok":true,"result":{"state":"ready","taskId":"task","dispatchId":"dispatch","effects":[{"kind":"dispatch_input","role":"agent","id":"worker","state":"accepted"}]}}`), nil
					case strings.HasPrefix(k, "orca orchestration worker-show"):
						return []byte(`{"ok":true,"result":{"dispatch":{"id":"dispatch","taskId":"task","runId":"r","assigneeHandle":"worker"},"worker":{"dispatchId":"dispatch","state":"ready","agentTerminalHandle":"worker"},"observation":{"exactWorker":true,"status":"live"},"terminal":{"handle":"worker"}}}`), nil
					}
					t.Fatalf("unexpected %s", k)
					return nil, nil
				})
				res, err := Launch(context.Background(), LaunchOptions{Name: "task", BaseBranch: "main", SpecContent: "task", RunID: "r", CoordHandle: "c", RepoID: "repo", RepoDir: wt, Agent: agent, Model: inputModel})
				if !started {
					t.Fatal("not started")
				}
				if fail {
					if err == nil || res != nil {
						t.Fatalf("false success %+v %v", res, err)
					}
				} else if err != nil || res.Model != model {
					t.Fatalf("model/result %+v %v", res, err)
				}
			})
		}
	}
}
func TestHardeningDrainErrors(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprint(partial, malformed), func(t *testing.T) {
				n := 0
				safetyMock(t, func(k string) ([]byte, error) {
					if !strings.HasPrefix(k, "orca orchestration check") {
						t.Fatalf("unexpected %s", k)
					}
					n++
					if partial && n == 1 {
						return []byte(`{"result":{"deliveryId":"d","messages":[{"type":"worker_done"}]}}`), nil
					}
					if malformed {
						return []byte("broken"), nil
					}
					return nil, errors.New("offline")
				})
				res, err := Drain(context.Background(), "coord", true)
				if err == nil {
					t.Fatal("swallowed error")
				}
				if partial && (res == nil || len(res.Messages) != 1) {
					t.Fatalf("lost partial result %+v", res)
				}
				if !partial && res != nil {
					t.Fatalf("initial error must return nil %+v", res)
				}
			})
		}
	}
}
func TestHardeningWaitStructured(t *testing.T) {
	for _, sig := range []string{"worker_done", "question", "escalation"} {
		t.Run(sig, func(t *testing.T) {
			out := "process log\n{\"result\":{\"messages\":[{\"type\":\n\t\"" + sig + "\"}]}}"
			safetyMock(t, func(k string) ([]byte, error) { return []byte(out), nil })
			res, err := Wait(context.Background(), WaitOptions{CoordHandle: "coord", MaxRounds: 1})
			if err != nil || res.MessageType != sig || res.Payload == nil || res.Raw != out {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
	for _, out := range []string{`{"metadata":{"type":"worker_done"},"result":{"messages":[{"type":"heartbeat","body":"waiter_exists"}]}}`, `{"result":{"messages":[{"type":"worker_done"}]}`, `{"result":{"messages":[]},"error":{"code":"runtime_error","message":"worker_done"}}`} {
		t.Run(out, func(t *testing.T) {
			safetyMock(t, func(k string) ([]byte, error) { return []byte(out), nil })
			res, err := Wait(context.Background(), WaitOptions{CoordHandle: "coord", MaxRounds: 1})
			if err == nil || res != nil {
				t.Fatalf("false signal %+v %v", res, err)
			}
		})
	}
}
func TestHardeningTidyExactCoordinator(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			closes := 0
			safetyMock(t, func(k string) ([]byte, error) {
				if k == "orca terminal list --json" {
					return []byte(`{"result":{"terminals":[{"handle":"coord","title":"~/shell","worktreePath":"/work/coord"},{"handle":"worker","title":"~/shell","worktreePath":"/work/worker-coord"}]}}`), nil
				}
				if k == "orca terminal close --terminal worker --tab --json" && apply {
					closes++
					return nil, nil
				}
				t.Fatalf("unexpected %s", k)
				return nil, nil
			})
			res, err := Tidy(context.Background(), TidyOptions{WorkspacesDir: "/work", CoordWorktree: "coord", Apply: apply})
			if err != nil || res.Terminals[0].Category != "foreign" || res.Terminals[1].Category != "shell_candidate" {
				t.Fatalf("%+v %v", res, err)
			}
			if closes != 0 {
				t.Fatal("wrong closes")
			}
		})
	}
}

func TestHardeningDrainAcknowledgement(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			calls := 0
			safetyMock(t, func(k string) ([]byte, error) {
				calls++
				if calls == 1 && k == "orca orchestration check --terminal coord --json" {
					return []byte(`{"result":{"deliveryId":"d","messages":[{"type":"heartbeat"},{"type":"worker_done"}]}}`), nil
				}
				if ack && calls == 2 && k == "orca orchestration check --terminal coord --json --ack d" {
					return []byte(`{"result":{"messages":[]}}`), nil
				}
				t.Fatalf("unexpected command: %s", k)
				return nil, nil
			})
			res, err := Drain(context.Background(), "coord", ack)
			if err != nil || res.Heartbeats != 1 || len(res.Messages) != 1 || (!ack && calls != 1) || (ack && calls != 2) {
				t.Fatalf("result=%+v error=%v calls=%d", res, err, calls)
			}
		})
	}
}

func TestHardeningWaitCancellationAndRetry(t *testing.T) {
	for _, out := range []string{`{"error":{"code":"waiter_exists","message":"busy"}}`, "offline"} {
		t.Run(out, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			safetyMock(t, func(k string) ([]byte, error) {
				cancel()
				return []byte(out), errors.New("check failed")
			})
			start := time.Now()
			if _, err := Wait(ctx, WaitOptions{CoordHandle: "coord", MaxRounds: 2}); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
				t.Fatalf("cancellation: %v elapsed=%s", err, time.Since(start))
			}
		})
	}
	calls := 0
	safetyMock(t, func(k string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("transient")
		}
		return []byte(`{"result":{"messages":[{"type":"question"}]}}`), nil
	})
	res, err := Wait(context.Background(), WaitOptions{CoordHandle: "coord", MaxRounds: 2})
	if err != nil || res.SignalRound != 2 {
		t.Fatalf("retry: %+v %v", res, err)
	}
}

func TestHardeningCloseRuntimeFailures(t *testing.T) {
	for _, stage := range []string{"worker-release", "terminal close", "worktree rm"} {
		t.Run(stage, func(t *testing.T) {
			safetyMock(t, func(k string) ([]byte, error) {
				if strings.Contains(k, stage) {
					return []byte("failed"), errors.New("API failure")
				}
				switch {
				case strings.HasPrefix(k, "git status"):
					return nil, nil
				case strings.HasPrefix(k, "git branch"):
					return []byte("worker"), nil
				case strings.HasPrefix(k, "git for-each-ref"):
					return []byte("refs/remotes/origin/main"), nil
				case strings.HasPrefix(k, "orca orchestration worker-release"):
					return []byte(`{"result":{"state":"released"}}`), nil
				case strings.HasPrefix(k, "orca terminal close"):
					return []byte(`{"result":{}}`), nil
				case strings.HasPrefix(k, "orca terminal list"):
					return []byte(`{"result":{"terminals":[],"truncated":false,"totalCount":0,"hostScope":{"hostIds":["local"],"omittedHostIds":[]}}}`), nil
				}
				t.Fatalf("unexpected command after failed stage: %s", k)
				return nil, nil
			})
			dispatch := "dispatch"
			if stage == "terminal close" {
				dispatch = ""
			}
			_, err := Close(context.Background(), CloseOptions{WorktreePath: t.TempDir(), TerminalHandle: "worker", DispatchID: dispatch})
			if err == nil || errors.Is(err, ErrDirtyWorktree) || errors.Is(err, ErrUniqueCommits) {
				t.Fatalf("runtime error: %v", err)
			}
		})
	}
}
