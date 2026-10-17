package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type launchRunnerFunc func(context.Context, string, string, ...string) ([]byte, error)

func (f launchRunnerFunc) Run(c context.Context, d, n string, a ...string) ([]byte, error) {
	return f(c, d, n, a...)
}

func TestLaunchSupervisedIdentity(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, mode := range []string{"terminal", "structured", "malformed", "not-ready", "wrong-dispatch", "wrong-task", "wrong-run", "unsupervised", "show-failed"} {
			t.Run(agent+"/"+mode, func(t *testing.T) {
				wt := t.TempDir()
				calls := 0
				SetRunner(launchRunnerFunc(func(_ context.Context, _ string, n string, a ...string) ([]byte, error) {
					calls++
					key := n + " " + strings.Join(a, " ")
					switch {
					case strings.HasPrefix(key, "orca worktree create"):
						b, _ := json.Marshal(map[string]any{"result": map[string]any{"worktree": map[string]any{"path": wt}}})
						return b, nil
					case strings.HasPrefix(key, "orca orchestration worker-start"):
						if mode == "malformed" {
							return []byte(`{"ok":true,"result":{}}`), nil
						}
						if mode == "not-ready" {
							return []byte(`{"ok":true,"result":{"state":"outcome_unknown","taskId":"task-live","dispatchId":"dispatch-live"}}`), nil
						}
						return []byte(`{"ok":true,"result":{"state":"ready","taskId":"task-live","dispatchId":"dispatch-live","effects":[{"kind":"dispatch_input","role":"agent","state":"accepted","id":"live-worker"}]}}`), nil
					case key == "orca orchestration worker-show --dispatch dispatch-live --json":
						if mode == "show-failed" {
							return nil, fmt.Errorf("unreachable")
						}
						dispatch, task, run, state := "dispatch-live", "task-live", "run", "ready"
						if mode == "wrong-dispatch" {
							dispatch = "foreign"
						}
						if mode == "wrong-task" {
							task = "foreign"
						}
						if mode == "wrong-run" {
							run = "foreign"
						}
						if mode == "unsupervised" {
							state = "unsupervised"
						}
						var terminal any = map[string]any{"handle": "live-worker"}
						if mode == "structured" {
							terminal = nil
						}
						b, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
							"dispatch":    map[string]any{"id": dispatch, "taskId": task, "runId": run, "assigneeHandle": "live-worker"},
							"worker":      map[string]any{"dispatchId": dispatch, "state": state, "agentTerminalHandle": "live-worker"},
							"observation": map[string]any{"exactWorker": true, "status": "live"}, "terminal": terminal,
						}})
						return b, nil
					default:
						t.Fatalf("unexpected operation: %s", key)
						return nil, nil
					}
				}))
				defer SetRunner(nil)
				res, err := Launch(context.Background(), LaunchOptions{Name: "work", BaseBranch: "main", SpecContent: "task", Agent: agent, RepoID: "repo", RepoDir: wt, RunID: "run", CoordHandle: "coord"})
				if mode != "terminal" && mode != "structured" {
					if err == nil || res != nil {
						t.Fatalf("false success: %+v %v", res, err)
					}
					return
				}
				if err != nil || res.TaskID != "task-live" || res.DispatchID != "dispatch-live" || res.WorkerHandle != "live-worker" || (mode == "terminal" && res.TerminalHandle != "live-worker") || (mode == "structured" && res.TerminalHandle != "") || calls != 3 {
					t.Fatalf("result=%+v error=%v calls=%d", res, err, calls)
				}
			})
		}
	}
}

func TestLaunchEffortRejectedBeforeSideEffects(t *testing.T) {
	SetRunner(launchRunnerFunc(func(context.Context, string, string, ...string) ([]byte, error) {
		t.Fatal("side effect before effort validation")
		return nil, nil
	}))
	defer SetRunner(nil)
	for _, effort := range []string{"high; echo injected", "$(id)", "unknown"} {
		_, err := Launch(context.Background(), LaunchOptions{Name: "work", BaseBranch: "main", SpecContent: "task", Agent: "kiro", RepoID: "repo", RepoDir: t.TempDir(), RunID: "run", CoordHandle: "coord", Effort: effort})
		if err == nil {
			t.Fatalf("accepted effort %q", effort)
		}
	}
}

func TestLaunchShellArgumentsStayLiteral(t *testing.T) {
	for _, agent := range []string{"kiro", "agy"} {
		t.Run(agent, func(t *testing.T) {
			wt := filepath.Join(t.TempDir(), "work ' $(printf injected) space")
			if err := os.Mkdir(wt, 0700); err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			capture := filepath.Join(binDir, "argv")
			binary := "agy"
			if agent == "kiro" {
				binary = "kiro-cli"
			}
			if err := os.WriteFile(filepath.Join(binDir, binary), []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$ARG_CAPTURE\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
			t.Setenv("ARG_CAPTURE", capture)
			repo := filepath.Join(wt, "repo ' $(printf injected)")
			if err := os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			run := filepath.Join(wt, "run ' $(printf injected)")
			extra := filepath.Join(wt, "extra ' $(printf injected)")
			SetRunner(launchRunnerFunc(func(_ context.Context, _ string, n string, a ...string) ([]byte, error) {
				key := n + " " + strings.Join(a, " ")
				switch {
				case strings.HasPrefix(key, "orca worktree create"):
					b, _ := json.Marshal(map[string]any{"result": map[string]any{"worktree": map[string]any{"path": wt}}})
					return b, nil
				case strings.HasPrefix(key, "orca terminal create"):
					return []byte(`{"result":{"terminal":{"handle":"worker"}}}`), nil
				case strings.HasPrefix(key, "orca orchestration task-create"):
					return []byte(`{"result":{"task":{"id":"task"}}}`), nil
				case strings.HasPrefix(key, "orca orchestration dispatch"):
					return []byte(`{"result":{"dispatch":{"id":"dispatch"},"preamble":"task"}}`), nil
				case strings.HasPrefix(key, "git rev-parse --git-common-dir"):
					return []byte(wt), nil
				case strings.HasPrefix(key, "orca terminal send"):
					for i, v := range a {
						if v == "--text" {
							out, err := exec.Command("sh", "-c", a[i+1]).CombinedOutput()
							if err != nil {
								t.Fatalf("launch shell: %v %s", err, out)
							}
							return []byte(`{"ok":true}`), nil
						}
					}
				}
				t.Fatalf("unexpected command %s", key)
				return nil, nil
			}))
			defer SetRunner(nil)
			_, err := Launch(context.Background(), LaunchOptions{Name: "work", BaseBranch: "main", SpecContent: "task", Agent: agent, RepoID: "repo", RepoDir: repo, RunID: "run", CoordHandle: "coord", RunDir: run, AddDirs: []string{extra}})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			argv := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
			if agent == "agy" {
				want := []string{"--model", "gemini-3.8-flash-high", "--dangerously-skip-permissions", "--add-dir", filepath.Join(repo, ".git"), "--add-dir", run, "--add-dir", extra, "-i"}
				if len(argv) != len(want)+1 || !reflect.DeepEqual(argv[:len(want)], want) {
					t.Fatalf("argv=%q", argv)
				}
			} else if len(argv) != 7 || argv[0] != "chat" || argv[2] != "claude-sonnet-5.5" || argv[4] != "high" {
				t.Fatalf("argv=%q", argv)
			}
			if !strings.Contains(argv[len(argv)-1], wt) {
				t.Fatalf("prompt path corrupted: %q", argv)
			}
		})
	}
}

func TestLaunchDoesNotOverwriteExistingTaskFile(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			wt := t.TempDir()
			target := filepath.Join(t.TempDir(), "original")
			task := filepath.Join(wt, ".orca-task.md")
			if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(target, task); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(task, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			SetRunner(launchRunnerFunc(func(_ context.Context, _ string, n string, a ...string) ([]byte, error) {
				switch key := n + " " + strings.Join(a, " "); {
				case strings.HasPrefix(key, "orca worktree create"):
					b, _ := json.Marshal(map[string]any{"result": map[string]any{"worktree": map[string]any{"path": wt}}})
					return b, nil
				case strings.HasPrefix(key, "orca terminal create"):
					return []byte(`{"result":{"terminal":{"handle":"worker"}}}`), nil
				case strings.HasPrefix(key, "orca orchestration task-create"):
					return []byte(`{"result":{"task":{"id":"task"}}}`), nil
				case strings.HasPrefix(key, "orca orchestration dispatch"):
					return []byte(`{"result":{"dispatch":{"id":"dispatch"},"preamble":"replace"}}`), nil
				default:
					return nil, fmt.Errorf("unexpected command: %s", key)
				}
			}))
			defer SetRunner(nil)
			_, err := Launch(context.Background(), LaunchOptions{Name: "work", BaseBranch: "main", SpecContent: "task", Agent: "agy", RepoID: "repo", RepoDir: wt, RunID: "run", CoordHandle: "coord"})
			if err == nil || !strings.Contains(err.Error(), "write task file") {
				t.Errorf("existing task file accepted: %v", err)
			}
			for _, path := range []string{target, task} {
				b, e := os.ReadFile(path)
				if e != nil || string(b) != "keep" {
					t.Errorf("overwrote %s: %q %v", path, b, e)
				}
			}
		})
	}
}

func TestExtractJSONPathRequiresNonemptyString(t *testing.T) {
	for _, value := range []string{`null`, `""`, `"  "`, `42`, `{}`, `[]`, `true`} {
		if v, err := extractJSONPath([]byte(`{"result":{"id":`+value+`}}`), "result.id"); err == nil {
			t.Errorf("accepted %s as %q", value, v)
		}
	}
}
