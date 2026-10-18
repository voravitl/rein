package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/routing"
)

func TestRouteOrcaRejectsAmbiguousEndpointBeforeLaunch(t *testing.T) {
	c := &contract.Contract{Worktree: t.TempDir()}
	base := []string{"orca", "orchestration", "worker-start", "--run", "run", "--agent", "antigravity", "--worktree", "path:" + c.Worktree, "--model", "m"}
	for _, flags := range [][]string{
		{"--environment", "home", "--environment=other"},
		{"--pairing-code=secret", "--pairing-code", "other"},
		{"--environment="},
		{"--pairing-code"},
	} {
		if _, _, err := launchIdentity(append(append([]string(nil), base...), flags...), c, "run"); err == nil {
			t.Errorf("accepted ambiguous endpoint flags %v", flags)
		}
	}
}

func TestRouteOrcaObservationKeepsRecordedFence(t *testing.T) {
	const ready = `{"ok":true,"result":{"dispatch":{"id":"actual-dispatch","taskId":"actual-task","runId":"run","assigneeHandle":"worker"},"worker":{"dispatchId":"actual-dispatch","state":"ready","agentTerminalHandle":"worker","startOptions":{"launch":{"effective":{"agent":"antigravity","model":"gemini-w-high","effort":"high"}}}},"observation":{"exactWorker":true,"status":"live"},"terminal":null}}`
	for _, tc := range []struct {
		name, observation string
		ok                bool
	}{
		{"structured-terminal-null", ready, true},
		{"terminal", strings.Replace(ready, `"terminal":null`, `"terminal":{"handle":"worker"}`, 1), true},
		{"wrong-run", strings.Replace(ready, `"runId":"run"`, `"runId":"foreign"`, 1), false},
		{"wrong-task", strings.Replace(ready, `"taskId":"actual-task"`, `"taskId":"foreign"`, 1), false},
		{"wrong-dispatch", strings.Replace(ready, `"id":"actual-dispatch"`, `"id":"foreign"`, 1), false},
		{"wrong-worker-handle", strings.Replace(ready, `"agentTerminalHandle":"worker"`, `"agentTerminalHandle":"foreign"`, 1), false},
		{"effective-model-mismatch", strings.Replace(ready, `"gemini-w-high"`, `"other-model"`, 1), false},
		{"effective-agent-mismatch", strings.Replace(ready, `"antigravity"`, `"claude"`, 1), false},
		{"effective-effort-mismatch", strings.Replace(ready, `"effort":"high"`, `"effort":"low"`, 1), false},
		{"requested-only", strings.Replace(ready, `"effective"`, `"requested"`, 1), false},
		{"inexact", strings.Replace(ready, `"exactWorker":true`, `"exactWorker":false`, 1), false},
		{"not-addressable", strings.Replace(ready, `"status":"live"`, `"status":"live","addressable":false`, 1), false},
		{"malformed", "not-json", false},
		{"overflow", strings.Repeat(" ", 1<<20) + ready, false},
		{"timeout", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newE2E(t, "orca")
			if out, code := capture(t, func() int {
				return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", e.profile, "--config", e.cfgPath, "--timeout", "30s"})
			}); code != 0 {
				t.Fatalf("prepare (%d): %s", code, out)
			}
			cli := filepath.Join(e.root, "orca")
			observation := filepath.Join(e.root, "observation.json")
			argvLog, envLog, calls := filepath.Join(e.root, "show-argv"), filepath.Join(e.root, "show-env"), filepath.Join(e.root, "calls")
			leasePath := filepath.Join(contract.IndexDir(), "routes", "leases", "task.json")
			t.Setenv("TEST_ORCA_OBSERVATION", observation)
			t.Setenv("TEST_ORCA_LEASE", leasePath)
			t.Setenv("TEST_ORCA_ARGV", argvLog)
			t.Setenv("TEST_ORCA_ENV", envLog)
			t.Setenv("TEST_ORCA_CALLS", calls)
			if err := os.WriteFile(observation, []byte(tc.observation), 0600); err != nil {
				t.Fatal(err)
			}
			body := `#!/bin/sh
case "$2" in
worker-start)
 printf 'start\n' >> "$TEST_ORCA_CALLS"
 printf '%s\n' '{"ok":true,"result":{"state":"ready","taskId":"actual-task","dispatchId":"actual-dispatch","effects":[{"kind":"dispatch_input","role":"agent","state":"accepted","id":"worker"}]}}';;
worker-show)
 printf 'show\n' >> "$TEST_ORCA_CALLS"
 grep '"orca_task": "actual-task"' "$TEST_ORCA_LEASE" >/dev/null || exit 91
 grep '"dispatch": "actual-dispatch"' "$TEST_ORCA_LEASE" >/dev/null || exit 92
 printf '%s\000' "$@" > "$TEST_ORCA_ARGV"
 printf '%s\n' "$PWD" "$ADVISE_TASK" "$ADVISE_RUN" > "$TEST_ORCA_ENV"
 printf 'codesign diagnostic on stderr only\n' >&2
 if [ ! -s "$TEST_ORCA_OBSERVATION" ]; then exec sleep 30; fi
 cat "$TEST_ORCA_OBSERVATION";;
*) exit 2;;
esac
`
			if err := os.WriteFile(cli, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			args := []string{"--task", "task", "--run", "run", "--", cli, "orchestration", "worker-start", "--agent", "antigravity", "--run", "run", "--worktree", "path:" + e.wt, "--model", "gemini-w-high", "--effort", "high", "--environment=home", "--pairing-code", "fixture-secret", "--on", "execution-host"}
			ctx := context.Background()
			if tc.name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
			}
			if code := routeLaunch(ctx, args, nil, nil, nil); (code == 0) != tc.ok {
				t.Errorf("observation launch exit=%d want success=%t", code, tc.ok)
			}
			raw, err := os.ReadFile(leasePath)
			if err != nil {
				t.Fatal(err)
			}
			var lease routing.Lease
			if err := json.Unmarshal(raw, &lease); err != nil {
				t.Fatal(err)
			}
			if lease.OrcaTask != "actual-task" || lease.Dispatch != "actual-dispatch" || !lease.Dispatched || routing.TaskBusy("task") == "" {
				t.Fatalf("observation lost remote fence: %+v", lease)
			}
			got, err := os.ReadFile(argvLog)
			want := "orchestration\x00worker-show\x00--dispatch\x00actual-dispatch\x00--json\x00--environment=home\x00--pairing-code\x00fixture-secret\x00"
			if err != nil || string(got) != want {
				t.Fatalf("observation endpoint or selectors changed: %q err=%v", got, err)
			}
			got, err = os.ReadFile(envLog)
			if err != nil || string(got) != contract.Real(e.wt)+"\ntask\nrun\n" {
				t.Fatalf("observation did not preserve launch cwd/environment: %q err=%v", got, err)
			}
			got, err = os.ReadFile(calls)
			if err != nil || string(got) != "start\nshow\n" {
				t.Fatalf("unexpected dispatch/observation calls: %q err=%v", got, err)
			}
			rows, err := ledger.LoadStrict("")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.Kind == "routing_exit" && row.Run == "run" {
					found = true
					if row.OK == nil || *row.OK != tc.ok || row.Minutes != nil {
						t.Errorf("remote observation recorded wrong success/duration: %+v", row)
					}
				}
				if strings.Contains(row.Notes, "fixture-secret") || strings.Contains(row.Notes, "codesign diagnostic") {
					t.Fatal("observation secret/output leaked into ledger")
				}
			}
			if !found {
				t.Fatal("missing exit ledger row")
			}
		})
	}
}
