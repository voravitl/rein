package orca

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockRunner struct {
	handlers map[string]func(args []string) ([]byte, error)
}

func (m *mockRunner) Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	for pattern, h := range m.handlers {
		if strings.HasPrefix(key, pattern) {
			return h(args)
		}
	}
	return []byte(`{"result":{}}`), nil
}

func TestLaunch_Validation(t *testing.T) {
	ctx := context.Background()
	_, err := Launch(ctx, LaunchOptions{})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected name required, got: %v", err)
	}

	_, err = Launch(ctx, LaunchOptions{Name: "task", BaseBranch: "main"})
	if err == nil || !strings.Contains(err.Error(), "spec content is missing") {
		t.Fatalf("expected spec missing, got: %v", err)
	}

	_, err = Launch(ctx, LaunchOptions{Name: "task", BaseBranch: "main", SpecContent: "do work", RunID: "R", CoordHandle: "C", RepoID: "ID", RepoDir: "DIR", Agent: "kiro", Model: "gpt-4"})
	if err == nil || !strings.Contains(err.Error(), "not allowed through Kiro") {
		t.Fatalf("expected kiro model refusal, got: %v", err)
	}
}

func TestLaunch_Success(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca worktree create": func(args []string) ([]byte, error) {
				return []byte(`{"result":{"worktree":{"path":"` + tmpDir + `"}}}`), nil
			},
			"orca terminal create": func(args []string) ([]byte, error) {
				return []byte(`{"result":{"terminal":{"handle":"term_123"}}}`), nil
			},
			"orca terminal list": func(args []string) ([]byte, error) {
				return []byte(`{"result":{"terminals":[{"handle":"term_123"},{"handle":"term_old"}]}}`), nil
			},
			"orca orchestration task-create": func(args []string) ([]byte, error) {
				return []byte(`{"result":{"task":{"id":"task_abc"}}}`), nil
			},
			"orca orchestration dispatch": func(args []string) ([]byte, error) {
				return []byte(`{"result":{"dispatch":{"id":"disp_xyz"},"preamble":"Task preamble"}}`), nil
			},
			"git rev-parse --git-common-dir": func(args []string) ([]byte, error) {
				return []byte(tmpDir), nil
			},
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	res, err := Launch(context.Background(), LaunchOptions{
		Name:        "task1",
		BaseBranch:  "main",
		SpecContent: "Do some work",
		Agent:       "kiro",
		Model:       "claude-sonnet-5.5",
		RepoID:      "repo_1",
		RepoDir:     tmpDir,
		RunID:       "run_1",
		CoordHandle: "term_coord",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TerminalHandle != "term_123" || res.TaskID != "task_abc" || res.DispatchID != "disp_xyz" {
		t.Fatalf("unexpected result fields: %+v", res)
	}

	taskFile := filepath.Join(tmpDir, ".orca-task.md")
	content, err := os.ReadFile(taskFile)
	if err != nil || string(content) != "Task preamble" {
		t.Fatalf("expected .orca-task.md written, got %s, err: %v", string(content), err)
	}
}

func TestWait_SignalDetection(t *testing.T) {
	mock := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca orchestration check": func(args []string) ([]byte, error) {
				return []byte(`{"result":{"deliveryId":"del_1","messages":[{"type":"worker_done","subject":"finished"}]}}`), nil
			},
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	res, err := Wait(context.Background(), WaitOptions{
		CoordHandle: "term_coord",
		TimeoutMs:   1000,
		MaxRounds:   1,
	})
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if res.MessageType != "worker_done" {
		t.Fatalf("expected worker_done, got %s", res.MessageType)
	}
}

func TestDrain_SeparatesHeartbeatsAndMessages(t *testing.T) {
	callCount := 0
	mock := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca orchestration check": func(args []string) ([]byte, error) {
				callCount++
				if callCount == 1 {
					return []byte(`{"result":{"deliveryId":"del_1","messages":[{"type":"heartbeat"},{"type":"heartbeat"},{"type":"worker_done","subject":"done"}]}}`), nil
				}
				return []byte(`{"result":{"deliveryId":"","messages":[]}}`), nil
			},
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	res, err := Drain(context.Background(), "term_coord", true)
	if err != nil {
		t.Fatalf("unexpected drain error: %v", err)
	}
	if res.Heartbeats != 2 {
		t.Errorf("expected 2 heartbeats, got %d", res.Heartbeats)
	}
	if len(res.Messages) != 1 || res.Messages[0].Type != "worker_done" {
		t.Errorf("expected 1 worker_done message, got %+v", res.Messages)
	}
}

func TestClose_RefusesDirtyWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"git status --porcelain": func(args []string) ([]byte, error) {
				return []byte(" M dirty.go\n"), nil
			},
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	_, err := Close(context.Background(), CloseOptions{
		WorktreePath: tmpDir,
	})
	if err == nil || !strings.Contains(err.Error(), "REFUSED: uncommitted changes") {
		t.Fatalf("expected dirty refusal, got: %v", err)
	}
}

func TestClose_RefusesUniqueCommits(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"git status --porcelain": func(args []string) ([]byte, error) {
				return []byte(""), nil
			},
			"git branch --show-current": func(args []string) ([]byte, error) {
				return []byte("feature-branch\n"), nil
			},
			"git for-each-ref": func(args []string) ([]byte, error) {
				// Only contains itself, no other ref
				return []byte("refs/heads/feature-branch\n"), nil
			},
			"git rev-parse --short HEAD": func(args []string) ([]byte, error) {
				return []byte("abc1234\n"), nil
			},
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	_, err := Close(context.Background(), CloseOptions{
		WorktreePath: tmpDir,
	})
	if err == nil || !strings.Contains(err.Error(), "unique commits") {
		t.Fatalf("expected unique commits refusal, got: %v", err)
	}
}

func TestOrcaSubjectComposesAgentAndModel(t *testing.T) {
	if got := OrcaSubject("t-fe", "agy", "gpt-5"); got != "rein t-fe · agy:gpt-5" {
		t.Fatalf("composed subject %q", got)
	}
}

func TestIsExecutableName(t *testing.T) {
	for name, want := range map[string]bool{
		"orca": true, "orca-dev": true, "orca-ide": true,
		"ORCA": false, "orca-idx": false, "orcar": false, "": false,
	} {
		if got := IsExecutableName(name); got != want {
			t.Errorf("IsExecutableName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLaunchTitlesCarryComposedSubject(t *testing.T) {
	for _, tt := range []struct {
		agent     string
		model     string
		titleFlag string
	}{
		{agent: "claude", model: "claude-sonnet-5-5", titleFlag: "--task-title"},
		{agent: "agy", model: "gpt-5", titleFlag: "--title"},
	} {
		t.Run(tt.agent, func(t *testing.T) {
			want := OrcaSubject("work", tt.agent, tt.model)
			tmpDir := t.TempDir()
			var got string
			var sawTitle bool
			record := func(args []string) {
				for i, v := range args {
					if v == tt.titleFlag && i+1 < len(args) {
						got, sawTitle = args[i+1], true
					}
				}
			}
			mock := &mockRunner{
				handlers: map[string]func(args []string) ([]byte, error){
					"orca worktree create": func(args []string) ([]byte, error) {
						return []byte(`{"result":{"worktree":{"path":"` + tmpDir + `"}}}`), nil
					},
					"orca terminal create": func(args []string) ([]byte, error) {
						record(args)
						return []byte(`{"result":{"terminal":{"handle":"term_123"}}}`), nil
					},
					"orca orchestration worker-start": func(args []string) ([]byte, error) {
						record(args)
						return []byte(`{"ok":true,"result":{"state":"ready","taskId":"task","dispatchId":"dispatch","effects":[{"kind":"dispatch_input","role":"agent","state":"accepted","id":"worker"}]}}`), nil
					},
					"orca orchestration worker-show --dispatch dispatch --json": func(args []string) ([]byte, error) {
						return []byte(`{"ok":true,"result":{"dispatch":{"id":"dispatch","taskId":"task","runId":"run","assigneeHandle":"worker"},"worker":{"dispatchId":"dispatch","state":"ready","agentTerminalHandle":"worker"},"observation":{"exactWorker":true,"status":"live"},"terminal":null}}`), nil
					},
					"orca orchestration task-create": func(args []string) ([]byte, error) {
						return []byte(`{"result":{"task":{"id":"task_abc"}}}`), nil
					},
					"orca orchestration dispatch": func(args []string) ([]byte, error) {
						return []byte(`{"result":{"dispatch":{"id":"disp_xyz"},"preamble":"Task preamble"}}`), nil
					},
					"git rev-parse --git-common-dir": func(args []string) ([]byte, error) {
						return []byte(tmpDir), nil
					},
				},
			}
			SetRunner(mock)
			defer SetRunner(nil)

			res, err := Launch(context.Background(), LaunchOptions{
				Name:        "work",
				BaseBranch:  "main",
				SpecContent: "do work",
				Agent:       tt.agent,
				Model:       tt.model,
				RepoID:      "repo_1",
				RepoDir:     tmpDir,
				RunID:       "run",
				CoordHandle: "term_coord",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !sawTitle {
				t.Fatalf("missing %s flag in command", tt.titleFlag)
			}
			if got != want {
				t.Fatalf("title %q after %s, want %q", got, tt.titleFlag, want)
			}
			if res.Subject != want {
				t.Fatalf("LaunchResult.Subject %q, want %q", res.Subject, want)
			}
		})
	}
}

func TestTidy_AuditsCategories(t *testing.T) {
	mock := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca terminal list": func(args []string) ([]byte, error) {
				return []byte(`{
					"result": {
						"terminals": [
							{"handle": "t1", "title": "~/orca/workspaces/rein/feat-1", "worktreePath": "/home/user/orca/workspaces/rein/feat-1"},
							{"handle": "t2", "title": "feat-1-worker", "worktreePath": "/home/user/orca/workspaces/rein/feat-1"},
							{"handle": "t3", "title": "other", "worktreePath": "/home/user/other"}
						]
					}
				}`), nil
			},
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	res, err := Tidy(context.Background(), TidyOptions{
		WorkspacesDir: "/home/user/orca/workspaces",
	})
	if err != nil {
		t.Fatalf("unexpected tidy error: %v", err)
	}
	if len(res.Terminals) != 3 {
		t.Fatalf("expected 3 terminals, got %d", len(res.Terminals))
	}
	if res.Terminals[0].Category != "shell_candidate" {
		t.Errorf("expected shell_candidate, got %s", res.Terminals[0].Category)
	}
	if res.Terminals[1].Category != "active_worker" {
		t.Errorf("expected active_worker, got %s", res.Terminals[1].Category)
	}
	if res.Terminals[2].Category != "foreign" {
		t.Errorf("expected foreign, got %s", res.Terminals[2].Category)
	}
}

func TestExtractJSONPath(t *testing.T) {
	raw := []byte(`{"result":{"worktree":{"path":"/path/to/wt"},"id":123}}`)
	val, err := extractJSONPath(raw, "result.worktree.path")
	if err != nil || val != "/path/to/wt" {
		t.Fatalf("expected /path/to/wt, got %q, err: %v", val, err)
	}
	val, err = extractJSONPath(raw, "result.id")
	if err == nil {
		t.Fatalf("expected non-string id refusal, got %q", val)
	}
	_, err = extractJSONPath(raw, "result.nonexistent")
	if !errors.Is(err, err) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}
