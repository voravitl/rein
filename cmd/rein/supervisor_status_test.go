package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func supervisorStatusFixture(t *testing.T) (*e2eEnv, string, supervisor.Plan, string) {
	t.Helper()
	// Unset redirects rather than setting them empty: Git distinguishes the two.
	// Setenv registers restoration of both the original value and its presence.
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
	// This existing fixture canonicalizes its temp root before binding DAG state.
	return localDAGWorkerFixture(t)
}

type supervisorStatusFile struct {
	Mode    fs.FileMode
	ModTime time.Time
	SHA256  [32]byte
	Link    string
}

func supervisorStatusSnapshot(t *testing.T, root string) map[string]supervisorStatusFile {
	t.Helper()
	files := map[string]supervisorStatusFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := supervisorStatusFile{Mode: info.Mode(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.SHA256 = sha256.Sum256(raw)
		} else if info.Mode()&os.ModeSymlink != 0 {
			state.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		files[path] = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func supervisorStatusReadOnly(t *testing.T, e *e2eEnv, args ...string) (string, int) {
	t.Helper()
	before := supervisorStatusSnapshot(t, e.root)
	out, code := capture(t, func() int { return cmdSupervisor(args) })
	after := supervisorStatusSnapshot(t, e.root)
	// Include Git state, source, contracts, quota ledger, marker, journal and any
	// new paths; reading an absent journal must not create it or a timing file.
	if !reflect.DeepEqual(before, after) {
		t.Fatal("status changed fixture paths, contents, modes or mtimes")
	}
	return out, code
}

func supervisorStatusJournal(t *testing.T, e *e2eEnv, p supervisor.Plan, hash, tail string) {
	t.Helper()
	loc, err := run.Locate(e.wt)
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]any{
		"seq": 1, "phase": "header", "operation": "bind",
		"data": map[string]any{"journal_version": 1, "plan_schema": 3, "run": p.Run, "plan_hash": hash},
	}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	raw = append(raw, tail...)
	if err := os.WriteFile(loc.Marker+".graph3-"+hash+".jsonl", raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStatusJSONReadOnlyWithoutControllerLock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tail  string
		state string
	}{
		{name: "missing_journal", state: "ready"},
		{name: "header_only", state: "ready"},
		{
			name:  "unfinished_prepare_intent",
			tail:  `{"seq":2,"task":"task","op_id":"` + strings.Repeat("a", 64) + `","phase":"intent","operation":"prepare"}` + "\n",
			state: "preparing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, path, p, hash := supervisorStatusFixture(t)
			if tc.name != "missing_journal" {
				supervisorStatusJournal(t, e, p, hash, tc.tail)
			}
			controller, err := run.AcquireController(e.wt, hash)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			out, code := supervisorStatusReadOnly(t, e, "status", "--plan", path, "--json")
			if code != 0 {
				t.Fatalf("read-only status while controller is held: exit=%d output=%q", code, out)
			}
			var view struct {
				Run      string `json:"run"`
				PlanHash string `json:"plan_hash"`
				Nodes    []struct {
					Task        string `json:"task"`
					State       string `json:"state"`
					Kind        string `json:"kind"`
					Layer       int    `json:"layer"`
					Attempt     int    `json:"attempt"`
					MaxAttempts int    `json:"max_attempts"`
					ElapsedMS   *int64 `json:"elapsed_ms"`
					Tokens      *int64 `json:"tokens"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal([]byte(out), &view); err != nil {
				t.Fatalf("decode status %q: %v", out, err)
			}
			if view.Run != p.Run || view.PlanHash != hash || len(view.Nodes) != 1 {
				t.Fatalf("status identity/nodes: %s", out)
			}
			node := view.Nodes[0]
			if node.Task != "task" || node.State != tc.state || node.Kind != "worker" || node.Layer != 0 || node.Attempt != 0 || node.MaxAttempts != 1 {
				t.Fatalf("unexpected node for %s: %+v", tc.name, node)
			}
			if node.ElapsedMS != nil || node.Tokens != nil {
				t.Fatalf("untimed journal must show unknown elapsed/tokens: %+v", node)
			}
			if err := controller.Check(); err != nil {
				t.Fatalf("status invalidated the active controller: %v", err)
			}
		})
	}
}

func TestSupervisorStatusTableReadOnly(t *testing.T) {
	e, path, _, _ := supervisorStatusFixture(t)
	out, code := supervisorStatusReadOnly(t, e, "status", "--plan", path)
	if code != 0 {
		t.Fatalf("table status: exit=%d output=%q", code, out)
	}
	for _, want := range []string{"task", "ready", "worker", "layer", "attempt", "tokens", "elapsed"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Fatalf("table lacks %q: %q", want, out)
		}
	}
	if json.Valid([]byte(out)) {
		t.Fatalf("default status must render a human-readable table: %q", out)
	}
}

func TestSupervisorStatusTruncatedTailReturnsChanging(t *testing.T) {
	e, path, p, hash := supervisorStatusFixture(t)
	supervisorStatusJournal(t, e, p, hash, `{"seq":2,"phase":"intent"`)
	started := time.Now()
	out, code := supervisorStatusReadOnly(t, e, "status", "--plan", path, "--json")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("partial-tail retry was not bounded: %s", elapsed)
	}
	if code != 0 {
		t.Fatalf("incomplete final line is changing, not corruption: exit=%d output=%q", code, out)
	}
	var view struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil || view.Status != "changing" {
		t.Fatalf("partial tail must not report false completion: output=%q error=%v", out, err)
	}
}

func TestSupervisorStatusRejectsCompletedJournalCorruption(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail string
	}{
		{name: "malformed_completed_line", tail: "not-json\n"},
		{
			name: "sequence_mismatch",
			tail: `{"seq":3,"task":"task","op_id":"` + strings.Repeat("a", 64) + `","phase":"intent","operation":"prepare"}` + "\n",
		},
		{name: "corrupt_prefix_before_partial_tail", tail: "not-json\n" + `{"seq":3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, path, p, hash := supervisorStatusFixture(t)
			supervisorStatusJournal(t, e, p, hash, tc.tail)
			out, code := supervisorStatusReadOnly(t, e, "status", "--plan", path, "--json")
			if code == 0 {
				t.Fatalf("completed corrupt content was accepted: %q", out)
			}
		})
	}
}

func TestSupervisorStatusWatchTerminalReadOnly(t *testing.T) {
	for _, flag := range []string{"--watch", "--watch=500ms"} {
		t.Run(flag, func(t *testing.T) {
			e, path, p, hash := supervisorStatusFixture(t)
			supervisorStatusJournal(t, e, p, hash, `{"seq":2,"phase":"control","operation":"cancel","error":"explicit cancellation"}`+"\n")
			out, code := supervisorStatusReadOnly(t, e, "status", "--plan", path, "--json", flag)
			if code != 0 || !strings.Contains(out, `"status":"reconciliation"`) || strings.Count(out, `"plan_hash"`) != 1 {
				t.Fatalf("terminal watch must emit one frame then exit: code=%d out=%q", code, out)
			}
		})
	}
}

func TestSupervisorStatusWatchRejectsInvalidDuration(t *testing.T) {
	for _, value := range []string{"garbage", "0", "-1s", "99ms", "false"} {
		t.Run(value, func(t *testing.T) {
			out, code := capture(t, func() int {
				old := os.Stderr
				os.Stderr = os.Stdout
				defer func() { os.Stderr = old }()
				return cmdSupervisor([]string{"status", "--plan", "unused.json", "--watch=" + value})
			})
			if code != 2 || !strings.Contains(out, "duration") {
				t.Fatalf("invalid duration %q: exit=%d out=%q", value, code, out)
			}
		})
	}
	var flag supervisorWatchFlag
	if err := flag.Set("100ms"); err != nil || flag.interval != 100*time.Millisecond {
		t.Fatalf("minimum valid duration: %+v %v", flag, err)
	}
}

func TestSupervisorStatusRejectsOtherSchemas(t *testing.T) {
	for _, schema := range []int{1, 2} {
		t.Run(string(rune('0'+schema)), func(t *testing.T) {
			e, path, p, _ := supervisorStatusFixture(t)
			p.Schema, p.Execution = schema, nil
			p.Tasks[0].Kind = ""
			if schema == 1 {
				p.Tasks = []supervisor.Task{{Task: "task", Attempt: "a", CurrentAttempt: "a", OrcaTask: "o", Dispatch: "d"}}
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := supervisor.Decode(raw); err != nil {
				t.Fatalf("schema rejection fixture must otherwise be valid: %v", err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			before := supervisorStatusSnapshot(t, e.root)
			out, code := capture(t, func() int {
				old := os.Stderr
				os.Stderr = os.Stdout
				defer func() { os.Stderr = old }()
				return cmdSupervisor([]string{"status", "--plan", path})
			})
			if code != 2 || !strings.Contains(out, "requires schema 3") {
				t.Fatalf("schema %d: exit=%d out=%q", schema, code, out)
			}
			if !reflect.DeepEqual(before, supervisorStatusSnapshot(t, e.root)) {
				t.Fatal("schema refusal mutated fixture")
			}
		})
	}
}

func TestSupervisorStatusWatchFramesAndTerminalExit(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		var out, stderr bytes.Buffer
		reads, waits := 0, 0
		code := supervisorStatusLoop(context.Background(), &out, &stderr, supervisorStatusOptions{json: true, watch: true, terminal: terminal, interval: 500 * time.Millisecond}, func(context.Context) (supervisor.DAGStatus, error) {
			reads++
			state := "running"
			if reads == 2 {
				state = "done"
			}
			return supervisor.DAGStatus{Run: "R", Status: "ok", Nodes: []supervisor.DAGNodeStatus{{Task: "A", State: state}}}, nil
		}, func(_ context.Context, duration time.Duration) error {
			waits++
			if duration != 500*time.Millisecond || waits > 1 {
				t.Fatalf("unexpected polling: %s waits=%d", duration, waits)
			}
			return nil
		})
		if code != 0 || reads != 2 || waits != 1 || stderr.Len() != 0 {
			t.Fatalf("watch exit=%d reads=%d waits=%d stderr=%q", code, reads, waits, stderr.String())
		}
		if terminal {
			if strings.Count(out.String(), "\x1b[H\x1b[2J") != 2 {
				t.Fatalf("terminal must redraw each frame: %q", out.String())
			}
		} else if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "\n\n{") {
			t.Fatalf("non-TTY frames need a blank separator without ANSI: %q", out.String())
		}
	}
}

func TestSupervisorStatusWatchInterruptExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, stderr bytes.Buffer
	reads := 0
	code := supervisorStatusLoop(ctx, &out, &stderr, supervisorStatusOptions{watch: true}, func(context.Context) (supervisor.DAGStatus, error) {
		reads++
		return supervisor.DAGStatus{Status: "changing", Nodes: []supervisor.DAGNodeStatus{{State: "done"}}}, nil
	}, func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	})
	if code != 130 || reads != 1 || stderr.Len() != 0 {
		t.Fatalf("interrupted changing snapshot: code=%d reads=%d stderr=%q", code, reads, stderr.String())
	}
}

func TestSupervisorStatusTableGolden(t *testing.T) {
	agent, model, effort := "codex", "gpt-6.1-sol", "high"
	view := supervisor.DAGStatus{Run: "R", Status: "ok", Nodes: []supervisor.DAGNodeStatus{{
		Task: "A", State: "running", Kind: "worker", Layer: 2, Attempt: 1, MaxAttempts: 3,
		Agent: &agent, Model: &model, Effort: &effort,
	}}}
	var out bytes.Buffer
	if err := writeSupervisorStatusTable(&out, view); err != nil {
		t.Fatal(err)
	}
	want := "Run R ok TASK STATE KIND LAYER ATTEMPT AGENT/MODEL ELAPSED TOKENS GUARD A running worker 2 1/3 codex/gpt-6.1-sol:high \u2014 \u2014 \u2014"
	if got := strings.Join(strings.Fields(out.String()), " "); got != want {
		t.Fatalf("table golden:\ngot  %q\nwant %q", got, want)
	}
	if got := statusTableText("model\x1b[2J\nforged\trow"); strings.ContainsAny(got, "\x1b\n\t") {
		t.Fatalf("table retained terminal controls: %q", got)
	}
}

func TestSupervisorStatusTerminalDetectionRejectsRedirects(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if supervisorStatusTerminal(null) {
		t.Fatal("character device /dev/null is not a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if supervisorStatusTerminal(w) {
		t.Fatal("pipe is not a terminal")
	}
}

func TestSupervisorStatusWatchSignalHelper(t *testing.T) {
	path := os.Getenv("REIN_STATUS_SIGNAL_PLAN")
	if path == "" {
		return
	}
	os.Exit(cmdSupervisor([]string{"status", "--plan", path, "--json", "--watch"}))
}

func TestSupervisorStatusWatchOSInterruptExit(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			e, path, _, _ := supervisorStatusFixture(t)
			before := supervisorStatusSnapshot(t, e.root)
			cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorStatusWatchSignalHelper$")
			cmd.Env = append(os.Environ(), "REIN_STATUS_SIGNAL_PLAN="+path)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill()
					<-exited
				}
			})
			firstFrame := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(stdout).ReadString('\n')
				firstFrame <- line
			}()
			select {
			case line := <-firstFrame:
				if !json.Valid([]byte(line)) || !strings.Contains(line, `"state":"ready"`) {
					t.Fatalf("watch did not emit an active JSON frame: %q", line)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("watch did not emit its first frame")
			}
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				waited = true
				if err == nil || cmd.ProcessState.ExitCode() != 130 {
					t.Fatalf("watch signal %s: exit=%d err=%v stderr=%q", signal, cmd.ProcessState.ExitCode(), err, stderr.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("watch did not exit after interrupt")
			}
			if !reflect.DeepEqual(before, supervisorStatusSnapshot(t, e.root)) {
				t.Fatal("interrupted watch mutated fixture")
			}
		})
	}
}
