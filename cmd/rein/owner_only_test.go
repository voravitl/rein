package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/verdict"
)

var agentMarkerCases = []struct{ name, kind string }{
	{"CLAUDE_CODE_SESSION_ID", "claude-code"}, {"CLAUDE_PID", "claude-code"}, {"CLAUDECODE", "claude-code"},
	{"ZCODE_APP_VERSION", "zcode"}, {"ZCODE_PROCESS_LABEL", "zcode"},
	{"CODEX_THREAD_ID", "codex"}, {"HERMES_SESSION_ID", "hermes"},
	{"ANTIGRAVITY_AGENT", "antigravity"}, {"ANTIGRAVITY_CONVERSATION_ID", "antigravity"}, {"GEMINI_CLI", "antigravity"},
	{"OPENCODE", "opencode"}, {"OPENCODE_PID", "opencode"},
}

func clearAgentMarkers(t *testing.T) {
	t.Helper()
	for _, marker := range agentMarkerCases {
		t.Setenv(marker.name, "")
	}
}

func fixtureOwnerTerminal(t *testing.T, terminal bool) {
	t.Helper()
	clearAgentMarkers(t)
	old := ownerStdinTerminal
	ownerStdinTerminal = func() bool { return terminal }
	t.Cleanup(func() { ownerStdinTerminal = old })
}

func unsetGitPaths(t *testing.T) {
	t.Helper()
	// Git path overrides must be absent: Git treats an empty value as a path.
	for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		value, present := os.LookupEnv(name)
		t.Cleanup(func() {
			var err error
			if present {
				err = os.Setenv(name, value)
			} else {
				err = os.Unsetenv(name)
			}
			if err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
		})
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

func TestOwnerOnlyCodexCannotAllow(t *testing.T) {
	fixtureOwnerTerminal(t, true)
	unsetGitPaths(t)
	for _, name := range []string{"REIN_RUN_DIR", "REIN_PROFILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("PIPELINE_RUNS", t.TempDir())
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "init")

	if code := cmdRun([]string{"start", root, "--run", "owner-only", "--session", "s", "--pid", strconv.Itoa(os.Getpid())}); code != 0 {
		t.Fatalf("ordinary start = %d, want 0", code)
	}
	loc, err := run.Locate(root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := run.Load(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Allowed) != 0 {
		t.Fatalf("fixture already has allowances: %+v", m.Allowed)
	}
	before, err := os.ReadFile(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_THREAD_ID", "codex-owner-only")
	if code := cmdRun([]string{"allow", root, "--task", "t", "--reason", "r"}); code != 2 {
		t.Errorf("allow under Codex = %d, want 2", code)
	}
	m, err = run.Load(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Allowed) != 0 {
		t.Errorf("allow under Codex added allowances: %+v", m.Allowed)
	}
	after, err := os.ReadFile(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("allow under Codex changed the run marker")
	}
}

var ownerCommands = []string{
	"rein run start --supervisor-plan", "rein run allow", "rein run end --abandon",
	"rein budget raise", "rein route reconcile",
}

type ownerFixture struct {
	root, fresh, marker, freshMarker, plan, hold, raises string
	stateFiles                                           []string
}

func newOwnerFixture(t *testing.T) *ownerFixture {
	t.Helper()
	unsetGitPaths(t)
	base := contract.Real(t.TempDir())
	t.Setenv("REIN_RUN_DIR", "")
	t.Setenv("REIN_PROFILE", "")
	t.Setenv("ORCA_REPO_DIR", "")
	t.Setenv("PIPELINE_RUNS", filepath.Join(base, "runs"))
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(base, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(base, "ledger.jsonl"))
	f := &ownerFixture{root: filepath.Join(base, "repo"), fresh: filepath.Join(base, "fresh"), plan: filepath.Join(base, "plan.json")}
	for _, root := range []string{f.root, f.fresh} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		gitIn(t, root, "init", "-q", "-b", "main")
		gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "fixture")
	}
	t.Chdir(f.root)
	if code := cmdRun([]string{"start", f.root, "--run", "owner-only", "--session", "s", "--pid", strconv.Itoa(os.Getpid())}); code != 0 {
		t.Fatalf("fixture start = %d", code)
	}
	loc, err := run.Locate(f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.marker = loc.Marker
	f.freshMarker = filepath.Join(f.fresh, ".git", run.MarkerFile)
	f.raises = filepath.Join(filepath.Dir(f.marker), "raises-owner-only.jsonl")
	c := &contract.Contract{Name: "owner-task", Worktree: f.root, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(base, "report.md")}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if err := budget.Raise(f.marker, "p", 1, "fixture"); err != nil {
		t.Fatal(err)
	}
	h, err := budget.Reserve(f.marker, nil, budget.ReserveRequest{Run: "owner-only", Task: c.Name, Attempt: "at-1",
		Items: []budget.Item{{Pool: "p", Unit: "tokens", Amount: 1, Kind: "funding"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.hold = h.ID
	// Model a stale reservation whose PID has been reused by this process.
	start, err := run.ProcStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	h.OwnerStart = start + 1
	holdsFile := filepath.Join(contract.IndexDir(), "holds", "holds.json")
	body, err := json.Marshal(map[string]*budget.Hold{h.ID: h})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(holdsFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	body = []byte(fmt.Sprintf(`{"schema":1,"run":"owner-supervised","repo":%q,"terminal":"coord","source":"fixture","version":"1","max_checks":1,"timeout_ms":1000,"poll_interval_ms":1,"tasks":[{"task":"t","attempt":"a","current_attempt":"a","orca_task":"ot","dispatch":"d"}]}`, f.fresh))
	if err := os.WriteFile(f.plan, body, 0o600); err != nil {
		t.Fatal(err)
	}
	f.stateFiles = []string{f.marker, f.freshMarker, f.raises, holdsFile, os.Getenv("PIPELINE_LEDGER")}
	return f
}

func (f *ownerFixture) invoke(command string) int {
	switch command {
	case "rein run start --supervisor-plan":
		return cmdRun([]string{"start", f.fresh, "--run", "owner-supervised", "--session", "s", "--pid", strconv.Itoa(os.Getpid()), "--supervisor-plan", f.plan})
	case "rein run allow":
		return cmdRun([]string{"allow", f.root, "--task", "t", "--reason", "r"})
	case "rein run end --abandon":
		return cmdRun([]string{"end", f.root, "--abandon", "--reason", "r"})
	case "rein budget raise":
		return cmdBudgetRaise([]string{"--pool", "p", "--amount", "2", "--reason", "r"})
	case "rein route reconcile":
		return cmdRoute([]string{"reconcile", "--hold", f.hold, "--reason", "r"})
	}
	panic("unknown test command")
}

func ownerState(t *testing.T, files []string) map[string]string {
	t.Helper()
	state := make(map[string]string)
	for _, path := range files {
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			state[path] = "absent"
		} else if err != nil {
			t.Fatal(err)
		} else {
			state[path] = "present:" + string(body)
		}
	}
	return state
}

func ownerStderr(t *testing.T, invoke func() int) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old; r.Close(); w.Close() }()
	done := make(chan string, 1)
	go func() { body, _ := io.ReadAll(r); done <- string(body) }()
	code := invoke()
	w.Close()
	return <-done, code
}

func assertOwnerRefusal(t *testing.T, f *ownerFixture, command, why, markerValue string) {
	t.Helper()
	before := ownerState(t, f.stateFiles)
	message, code := ownerStderr(t, func() int { return f.invoke(command) })
	if code != 2 || !strings.Contains(message, command) || !strings.Contains(message, why) {
		t.Errorf("%s: exit %d, refusal %q; want exit 2 with command and %s", command, code, message, why)
	}
	if markerValue != "" && strings.Contains(message, markerValue) {
		t.Error("refusal disclosed a marker value")
	}
	for path, body := range ownerState(t, f.stateFiles) {
		if body != before[path] {
			t.Errorf("refusal changed state: %s", path)
		}
	}
}

func TestOwnerOnlyEveryAgentMarker(t *testing.T) {
	fixtureOwnerTerminal(t, true)
	f := newOwnerFixture(t)
	for _, marker := range agentMarkerCases {
		t.Run(marker.name, func(t *testing.T) {
			clearAgentMarkers(t)
			const value = "private-marker-do-not-print"
			t.Setenv(marker.name, value)
			for _, command := range ownerCommands {
				t.Run(command, func(t *testing.T) { assertOwnerRefusal(t, f, command, marker.kind, value) })
			}
		})
	}
}

func TestOwnerOnlyNonInteractive(t *testing.T) {
	fixtureOwnerTerminal(t, false)
	f := newOwnerFixture(t)
	for _, command := range ownerCommands {
		t.Run(command, func(t *testing.T) {
			assertOwnerRefusal(t, f, command, "standard input is not an interactive terminal", "")
		})
	}
}

func TestOwnerOnlyOwnerSuccess(t *testing.T) {
	for _, command := range ownerCommands {
		t.Run(command, func(t *testing.T) {
			fixtureOwnerTerminal(t, true)
			f := newOwnerFixture(t)
			if message, code := ownerStderr(t, func() int { return f.invoke(command) }); code != 0 {
				t.Fatalf("owner command exit %d: %s", code, message)
			}
			switch command {
			case "rein run start --supervisor-plan":
				if m, err := run.Load(f.freshMarker); err != nil || m.Schema != run.SchemaSupervised {
					t.Fatalf("supervised run missing: %v", err)
				}
			case "rein run allow":
				if m, err := run.Load(f.marker); err != nil || !m.Allows("task", "t") {
					t.Fatalf("allowance missing: %v", err)
				}
			case "rein run end --abandon":
				if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
					t.Fatalf("abandoned marker remains: %v", err)
				}
			case "rein budget raise":
				body, err := os.ReadFile(f.raises)
				if err != nil || !bytes.Contains(body, []byte(`"amount":2`)) {
					t.Fatalf("raise missing: %v", err)
				}
			case "rein route reconcile":
				if holds, err := budget.Outstanding(); err != nil || len(holds) != 0 {
					t.Fatalf("hold remains: %v", err)
				}
			}
		})
	}
}

func TestOwnerOnlyOrdinaryRunCommandsRemainAvailable(t *testing.T) {
	fixtureOwnerTerminal(t, false)
	f := newOwnerFixture(t)
	for _, marker := range agentMarkerCases {
		t.Run(marker.name, func(t *testing.T) {
			clearAgentMarkers(t)
			t.Setenv(marker.name, "agent")
			for _, args := range [][]string{
				{"start", f.fresh, "--run", "ordinary", "--session", "s", "--pid", strconv.Itoa(os.Getpid())},
				{"audit", f.fresh},
				{"resume", f.fresh, "--session", "next", "--pid", strconv.Itoa(os.Getpid())},
				{"end", f.fresh},
			} {
				if code := cmdRun(args); code != 0 {
					t.Fatalf("ordinary %s under %s = %d", args[0], marker.kind, code)
				}
			}
		})
	}
}

func TestOwnerOnlyApproveAsDirectedProvenance(t *testing.T) {
	for _, marker := range []struct{ name, kind string }{
		{"CLAUDE_CODE_SESSION_ID", "claude-code"}, {"ZCODE_PROCESS_LABEL", "zcode"},
		{"CODEX_THREAD_ID", "codex"}, {"HERMES_SESSION_ID", "hermes"},
	} {
		t.Run(marker.kind, func(t *testing.T) {
			fixtureOwnerTerminal(t, false)
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			oldStdin := os.Stdin
			os.Stdin = r
			t.Cleanup(func() { os.Stdin = oldStdin; r.Close(); w.Close() })
			root, sha := approveTestRepo(t)
			t.Setenv(marker.name, "approval-session")
			args := []string{"--repo", root, "--mr", "34", "--sha", sha}
			if _, code := ownerStderr(t, func() int { return cmdApprove(args) }); code != 1 {
				t.Fatalf("undirected approval = %d, want 1", code)
			}
			if _, err := os.Stat(approvalsFile(root, 34)); !os.IsNotExist(err) {
				t.Fatalf("undirected approval changed state: %v", err)
			}
			args = append(args, "--as-directed", "--reason", "owner directed")
			if code := cmdApprove(args); code != 0 {
				t.Fatalf("directed approval = %d, want 0", code)
			}
			body, err := os.ReadFile(approvalsFile(root, 34))
			if err != nil {
				t.Fatal(err)
			}
			var records []verdict.ApprovalRecord
			if err := json.Unmarshal(body, &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Via != marker.kind || records[0].SessionID != "approval-session" {
				t.Fatalf("incorrect %s approval provenance", marker.kind)
			}
		})
	}
}
