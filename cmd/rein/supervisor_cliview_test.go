package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

// RED stage for PR-D0 (DAG view inside agent CLIs). Every test compiles
// against base 515663b and reaches `rein supervisor status`; on base each one
// fails at runtime because `--view statusline` / `--view tui` are rejected
// (exit 2, `invalid --view "statusline": want table or graph`). Fixture sanity
// checks run before the first statusline/tui call so a RED result can only come
// from the missing views. Option-rejection tests exclude the base messages
// `invalid --view` and `flag provided but not defined`, so base rejections do
// not count as a pass.
//
// Intended statusline format (documented in the PR-D0 RED report):
//   line 1: header with run, status and a progress bar sized from the width;
//   lines 2..: one row per DAG layer, `<glyph> <task>` per node;
//   at most --lines rows in total (default 4); overflow ends with `+k more`.
// Glyphs: ✓ done, ✗ failed, ⊘ blocked, ◌ pending, ○ ready, and a Braille
// spinner frame (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏) for preparing/running/finalizing, chosen from
// REIN_NOW_UNIX_MS when set, else the wall clock.

const cliviewNow = int64(1700000000000)

var (
	cliviewSGR     = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	cliviewMore    = regexp.MustCompile(`\+\d+ more\b`)
	cliviewSpinner = regexp.MustCompile(`([⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]) task\b`)
)

func cliviewUnsetenv(t *testing.T, key string) {
	t.Helper()
	// Setenv registers restoration of the original value and its presence.
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// cliviewEnv pins the environment every statusline/tui test depends on.
func cliviewEnv(t *testing.T, columns string, color bool) {
	t.Helper()
	if columns == "" {
		cliviewUnsetenv(t, "COLUMNS")
	} else {
		t.Setenv("COLUMNS", columns)
	}
	if color {
		cliviewUnsetenv(t, "NO_COLOR")
	} else {
		t.Setenv("NO_COLOR", "1")
	}
	cliviewUnsetenv(t, "REIN_PLAN")
	t.Setenv("REIN_NOW_UNIX_MS", strconv.FormatInt(cliviewNow, 10))
}

// cliviewStatus runs the CLI with stdin replaced by a pipe carrying the given
// host payload (closed after writing), and checks the fixture stays read-only.
func cliviewStatus(t *testing.T, e *e2eEnv, stdin string, args ...string) (string, string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(w, stdin)
		_ = w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = old
		_ = r.Close()
	}()
	return supervisorGraphReadOnly(t, e, args...)
}

func cliviewLines(out string) []string {
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// requireCliviewStatusline checks the host-safe statusline contract and
// returns the output with SGR color sequences removed.
func requireCliviewStatusline(t *testing.T, out, stderr string, code, width, maxLines int, color bool) string {
	t.Helper()
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("statusline must be accepted and non-empty: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	for _, seq := range []string{"\x1b[H", "\x1b[2J", "\x1b[?"} {
		if strings.Contains(out, seq) {
			t.Fatalf("statusline must not move the cursor or clear the screen (%q): %q", seq, out)
		}
	}
	plain := cliviewSGR.ReplaceAllString(out, "")
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("statusline may only emit SGR color escapes: %q", out)
	}
	if strings.ContainsAny(plain, "\r\x00\x07") {
		t.Fatalf("statusline leaked control bytes: %q", out)
	}
	if color && !strings.Contains(out, "\x1b[") {
		t.Fatalf("captured statusline must force color when NO_COLOR is unset: %q", out)
	}
	if !color && strings.Contains(out, "\x1b") {
		t.Fatalf("NO_COLOR statusline must contain no escapes: %q", out)
	}
	lines := cliviewLines(plain)
	if len(lines) > maxLines {
		t.Fatalf("statusline has %d lines, cap is %d: %q", len(lines), maxLines, plain)
	}
	for i, line := range lines {
		if n := utf8.RuneCountInString(line); n > width {
			t.Fatalf("statusline line %d has %d columns, exceeding width %d: %q", i+1, n, width, plain)
		}
	}
	if strings.HasPrefix(plain, "rein:") {
		t.Fatalf("valid snapshot must render the DAG, not a fallback line: %q", plain)
	}
	return plain
}

// requireCliviewFallback checks the failure-safe single `rein:` line.
func requireCliviewFallback(t *testing.T, out, stderr string, code int) {
	t.Helper()
	plain := strings.TrimSuffix(cliviewSGR.ReplaceAllString(out, ""), "\n")
	if code != 0 || !strings.HasPrefix(plain, "rein:") || strings.Contains(plain, "\n") {
		t.Fatalf("statusline failure must exit 0 with exactly one `rein:` line: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if strings.ContainsAny(plain, "\x1b\r\x00\x07") || utf8.RuneCountInString(plain) > 80 {
		t.Fatalf("statusline fallback must be short plain text: %q", out)
	}
}

func requireCliviewGlyph(t *testing.T, plain, glyph, task string) {
	t.Helper()
	if !regexp.MustCompile(regexp.QuoteMeta(glyph+" "+task) + `\b`).MatchString(plain) {
		t.Fatalf("statusline must show %q as `%s %s`: %q", task, glyph, task, plain)
	}
}

func requireCliviewNames(t *testing.T, plain string, p supervisor.Plan) {
	t.Helper()
	for _, task := range p.Tasks {
		if !strings.Contains(plain, task.Task) {
			t.Fatalf("statusline dropped node %q: %q", task.Task, plain)
		}
	}
}

func cliviewFanout() map[string][]string {
	return map[string][]string{"task": {}, "left": {"task"}, "right": {"task"}, "join": {"left", "right"}}
}

func cliviewFailureJournal(t *testing.T, e *e2eEnv, p supervisor.Plan, hash string) {
	t.Helper()
	opID := strings.Repeat("a", 64)
	supervisorStatusJournal(t, e, p, hash,
		`{"seq":2,"task":"task","op_id":"`+opID+`","phase":"intent","operation":"prepare"}`+"\n"+
			`{"seq":3,"task":"task","op_id":"`+opID+`","phase":"error","operation":"prepare","error":"fixture preparation failed"}`+"\n")
}

// cliviewRunningFixture binds a one-task plan whose journal prefix (header,
// invocation start, prepare intent/result) leaves the task running, mirroring
// TestSupervisorGraphSequenceIncludesHeaderAndLifecycle.
func cliviewRunningFixture(t *testing.T) (*e2eEnv, string, supervisor.Plan, string) {
	t.Helper()
	e, path, p, hash := supervisorGraphFixture(t, map[string][]string{"task": {}})
	loc, err := run.Locate(e.wt)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	event := supervisor.GraphInvocationEvent{GraphInvocationIdentity: supervisor.GraphInvocationIdentity{
		InvocationID: strings.Repeat("b", 64), Run: p.Run, PlanHash: hash, Repo: p.Repo,
		MarkerDigest: graphHash(marker), PID: os.Getpid(), PIDStart: 1,
	}}
	task := p.Tasks[0]
	a := supervisor.PreparedAttempt{
		Number: 1, Run: p.Run, PlanHash: hash, Task: task.Task, Kind: "action",
		Attempt: "graph-attempt", ReceiptID: "graph-receipt", ContractHash: task.ContractHash,
		Worktree: e.wt, SourceRevision: task.Base, SourceDigest: graphHash([]byte("source")),
		InputDigest: supervisor.InputDigest(nil), Argv: task.Action.Argv,
	}
	a.Action = &supervisor.ActionReceipt{
		DaemonID: "fixture-daemon", Version: 1, Run: a.Run, PlanHash: hash, Task: a.Task,
		Attempt: a.Attempt, ReceiptID: a.ReceiptID, ContractHash: a.ContractHash,
		SourceRevision: a.SourceRevision, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest,
		ArgvDigest: graphHash(dagEncode(task.Action.Argv)), SandboxDigest: graphHash(dagEncode(task.Action.Sandbox)),
	}
	entry := func(seq int, phase, operation, opID string, data any) string {
		row := map[string]any{"seq": seq, "phase": phase, "operation": operation}
		if opID != "" {
			row["task"], row["op_id"] = "task", opID
		}
		if data != nil {
			row["data"] = data
		}
		return string(dagEncode(row)) + "\n"
	}
	prepareID := strings.Repeat("c", 64)
	supervisorStatusJournal(t, e, p, hash,
		entry(2, "lifecycle", "invocation_start", "", event)+
			entry(3, "intent", "prepare", prepareID, nil)+
			entry(4, "result", "prepare", prepareID, a))
	strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
	if err != nil || len(strict.Nodes) != 1 || strict.Nodes[0].State != "running" {
		t.Fatalf("running fixture must be a strict running snapshot: %+v err=%v", strict, err)
	}
	return e, path, p, hash
}

// 1. Fan-out/join statusline at 80 columns: bounded, colored, no cursor control.
func TestSupervisorCLIViewStatuslineBasic(t *testing.T) {
	cliviewEnv(t, "80", true)
	e, path, p, hash := supervisorGraphFixture(t, cliviewFanout())
	if strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash); err != nil || strict.Nodes[0].State != "ready" || strict.Nodes[3].State != "pending" {
		t.Fatalf("fan-out fixture must be ready/pending: %+v err=%v", strict, err)
	}
	out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
	plain := requireCliviewStatusline(t, out, stderr, code, 80, 4, true)
	requireCliviewNames(t, plain, p)
	requireCliviewGlyph(t, plain, "○", "task")
	for _, name := range []string{"left", "right", "join"} {
		requireCliviewGlyph(t, plain, "◌", name)
	}
	again, againErr, againCode := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
	if againCode != 0 || again != out {
		t.Fatalf("unchanged snapshot and fixed time must render identical bytes: first=%q second=%q stderr=%q", out, again, againErr)
	}
	// REIN_PLAN selects the same plan when --plan is absent.
	t.Setenv("REIN_PLAN", path)
	env, envErr, envCode := cliviewStatus(t, e, "", "status", "--view", "statusline")
	if envCode != 0 || env != out {
		t.Fatalf("REIN_PLAN must select the plan like --plan: flag=%q env=%q exit=%d stderr=%q", out, env, envCode, envErr)
	}
}

// 2. NO_COLOR removes every escape and nothing else.
func TestSupervisorCLIViewStatuslineNoColor(t *testing.T) {
	cliviewEnv(t, "80", true)
	e, path, p, _ := supervisorGraphFixture(t, cliviewFanout())
	colored, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
	plainColored := requireCliviewStatusline(t, colored, stderr, code, 80, 4, true)
	t.Setenv("NO_COLOR", "1")
	out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
	plain := requireCliviewStatusline(t, out, stderr, code, 80, 4, false)
	requireCliviewNames(t, plain, p)
	if out != plainColored {
		t.Fatalf("NO_COLOR must only drop color: colored(stripped)=%q no_color=%q", plainColored, out)
	}
}

// 3. Width source: COLUMNS, then stdin JSON terminal_width, then 80.
func TestSupervisorCLIViewStatuslineWidthSource(t *testing.T) {
	cliviewEnv(t, "40", false)
	e, path, p, _ := supervisorGraphFixture(t, cliviewFanout())
	args := []string{"status", "--plan", path, "--view", "statusline"}
	render := func(columns, stdin string, width int) string {
		t.Helper()
		if columns == "" {
			cliviewUnsetenv(t, "COLUMNS")
		} else {
			t.Setenv("COLUMNS", columns)
		}
		out, stderr, code := cliviewStatus(t, e, stdin, args...)
		plain := requireCliviewStatusline(t, out, stderr, code, width, 4, false)
		requireCliviewNames(t, plain, p)
		return out
	}
	out40 := render("40", "", 40)
	if got := render("40", `{"terminal_width":120}`, 40); got != out40 {
		t.Fatalf("COLUMNS must take precedence over stdin terminal_width: columns=%q both=%q", out40, got)
	}
	out60 := render("60", "", 60)
	if got := render("", `{"terminal_width":60}`, 60); got != out60 {
		t.Fatalf("stdin terminal_width must set the width when COLUMNS is unset: columns=%q json=%q", out60, got)
	}
	if got := render("0", `{"terminal_width":60}`, 60); got != out60 {
		t.Fatalf("non-positive COLUMNS must fall through to stdin terminal_width: columns=%q json=%q", out60, got)
	}
	out80 := render("80", "", 80)
	if got := render("", "", 80); got != out80 {
		t.Fatalf("width must default to 80 without COLUMNS or terminal_width: default=%q columns80=%q", got, out80)
	}
	if got := render("", `{"terminal_width":0}`, 80); got != out80 {
		t.Fatalf("non-positive terminal_width must fall back to 80: default=%q got=%q", out80, got)
	}
	// The header progress bar is sized from the width, so each source is
	// observable: 40, 60 and 80 columns must render different bytes.
	if out40 == out60 || out60 == out80 || out40 == out80 {
		t.Fatalf("width must affect the statusline (progress bar): 40=%q 60=%q 80=%q", out40, out60, out80)
	}
}

// 4. Arbitrary host JSON on stdin never changes the exit code or the DAG.
func TestSupervisorCLIViewStatuslineTolerantStdin(t *testing.T) {
	cliviewEnv(t, "80", true)
	e, path, p, _ := supervisorGraphFixture(t, cliviewFanout())
	args := []string{"status", "--plan", path, "--view", "statusline"}
	baseline, stderr, code := cliviewStatus(t, e, "", args...)
	requireCliviewNames(t, requireCliviewStatusline(t, baseline, stderr, code, 80, 4, true), p)
	for _, tc := range []struct{ name, stdin string }{
		{"claude", `{"hook_event_name":"Status","session_id":"abc123","transcript_path":"/tmp/t.jsonl","cwd":"/tmp","model":{"id":"claude-opus-4","display_name":"Opus"},"workspace":{"current_dir":"/tmp","project_dir":"/tmp"},"version":"2.1.312","output_style":{"name":"default"},"cost":{"total_cost_usd":0.01,"total_duration_ms":1200}}`},
		{"agy", `{"terminal_width":100,"agent_state":"thinking","model":"gemini-3-pro","session":{"id":"s1"},"cwd":"/tmp"}`},
		{"agy_wrong_width_type", `{"terminal_width":"wide","agent_state":"idle"}`},
		{"invalid_json", `{not json`},
		{"non_object_json", `[1,2,3]`},
		{"trailing_garbage", `{"terminal_width":100} trailing`},
		{"control_bytes", "\x1b[2J\x00\x07\r\n"},
		{"oversized", `{"pad":"` + strings.Repeat("x", 2<<20) + `"}`},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := cliviewStatus(t, e, tc.stdin, args...)
			requireCliviewStatusline(t, out, stderr, code, 80, 4, true)
			if out != baseline {
				t.Fatalf("host stdin must not change the DAG when COLUMNS is set: baseline=%q got=%q", baseline, out)
			}
		})
	}
}

// 5. Failure safety: a host status line must never go blank on error.
func TestSupervisorCLIViewStatuslineFailureSafe(t *testing.T) {
	cliviewEnv(t, "80", true)
	e, path, p, hash := supervisorGraphFixture(t, cliviewFanout())
	// Control: a missing journal is a normal ready/pending snapshot.
	out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
	requireCliviewNames(t, requireCliviewStatusline(t, out, stderr, code, 80, 4, true), p)

	outside := t.TempDir()
	invalid := filepath.Join(outside, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{name: "missing_plan_flag", args: []string{"--plan", filepath.Join(outside, "absent.json")}},
		{name: "missing_plan_env", env: filepath.Join(outside, "absent.json")},
		{name: "no_plan_source"},
		{name: "plan_is_directory", args: []string{"--plan", outside}},
		{name: "plan_invalid_json", env: invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("REIN_PLAN", tc.env)
			}
			args := append([]string{"status", "--view", "statusline"}, tc.args...)
			out, stderr, code := cliviewStatus(t, e, "", args...)
			requireCliviewFallback(t, out, stderr, code)
		})
	}

	loc, err := run.Locate(e.wt)
	if err != nil {
		t.Fatal(err)
	}
	journal := loc.Marker + ".graph3-" + hash + ".jsonl"
	for _, tc := range []struct {
		name    string
		viaEnv  bool
		journal func(*testing.T)
	}{
		{name: "corrupt_complete_journal", journal: func(t *testing.T) { supervisorStatusJournal(t, e, p, hash, "not-json\n") }},
		{name: "plan_hash_mismatch", viaEnv: true, journal: func(t *testing.T) {
			header := map[string]any{
				"seq": 1, "phase": "header", "operation": "bind",
				"data": map[string]any{"journal_version": 1, "plan_schema": 3, "run": p.Run, "plan_hash": strings.Repeat("f", 64)},
			}
			if err := os.WriteFile(journal, append(dagEncode(header), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.journal(t)
			defer os.Remove(journal)
			// Control: the existing table reader rejects this journal.
			tableOut, tableErr, tableCode := cliviewStatus(t, e, "", "status", "--plan", path)
			if tableCode != 1 || tableOut != "" || tableErr == "" {
				t.Fatalf("fixture must be rejected by the base reader: exit=%d stdout=%q stderr=%q", tableCode, tableOut, tableErr)
			}
			args := []string{"status", "--view", "statusline"}
			if tc.viaEnv {
				t.Setenv("REIN_PLAN", path)
			} else {
				args = append(args, "--plan", path)
			}
			out, stderr, code := cliviewStatus(t, e, "", args...)
			requireCliviewFallback(t, out, stderr, code)
		})
	}
}

// 6. Height cap: default 4 rows, `--lines N` changes it, invalid N exits 2.
func TestSupervisorCLIViewStatuslineHeightCap(t *testing.T) {
	cliviewEnv(t, "80", false)
	e, path, p, _ := supervisorGraphFixture(t, map[string][]string{
		"task": {}, "left": {"task"}, "right": {"left"}, "join": {"right"}, "leaf": {"join"},
	})
	if len(p.Tasks) != 5 {
		t.Fatalf("chain fixture must have five layers: %+v", p.Tasks)
	}
	base := []string{"status", "--plan", path, "--view", "statusline"}
	out, stderr, code := cliviewStatus(t, e, "", base...)
	plain := requireCliviewStatusline(t, out, stderr, code, 80, 4, false)
	if !cliviewMore.MatchString(plain) {
		t.Fatalf("five layers exceed the default cap of 4 and must show `+N more`: %q", plain)
	}
	out, stderr, code = cliviewStatus(t, e, "", append(base, "--lines", "8")...)
	plain = requireCliviewStatusline(t, out, stderr, code, 80, 8, false)
	requireCliviewNames(t, plain, p)
	if cliviewMore.MatchString(plain) {
		t.Fatalf("--lines 8 fits header plus five layers and must not truncate: %q", plain)
	}
	out, stderr, code = cliviewStatus(t, e, "", append(base, "--lines=2")...)
	plain = requireCliviewStatusline(t, out, stderr, code, 80, 2, false)
	if !cliviewMore.MatchString(plain) {
		t.Fatalf("--lines 2 must truncate with `+N more`: %q", plain)
	}
	for _, value := range []string{"0", "-1", "x", "1.5", ""} {
		t.Run("invalid_"+value, func(t *testing.T) {
			// A missing plan proves validation precedes reading: the statusline
			// fallback would otherwise exit 0 with a `rein:` line.
			for _, plan := range []string{path, filepath.Join(t.TempDir(), "absent.json")} {
				out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", plan, "--view", "statusline", "--lines="+value)
				if code != 2 || out != "" || !strings.Contains(stderr, "lines") || strings.Contains(stderr, "flag provided but not defined") || strings.Contains(stderr, "invalid --view") {
					t.Fatalf("invalid --lines %q must be rejected with exit 2 before reading: exit=%d stdout=%q stderr=%q", value, code, out, stderr)
				}
			}
		})
	}
}

// 7. State glyphs for failed/blocked versus ready/pending.
func TestSupervisorCLIViewStatuslineStateGlyphs(t *testing.T) {
	cliviewEnv(t, "80", false)
	for _, tc := range []struct {
		name    string
		journal func(*testing.T, *e2eEnv, supervisor.Plan, string)
		glyphs  []string
	}{
		{name: "prepare_failure", journal: cliviewFailureJournal, glyphs: []string{"✗", "⊘", "⊘", "⊘"}},
		{name: "cancel", journal: func(t *testing.T, e *e2eEnv, p supervisor.Plan, hash string) {
			supervisorStatusJournal(t, e, p, hash, `{"seq":2,"phase":"control","operation":"cancel","error":"explicit cancellation \u001b[31mred\u001b[0m\u0007\r"}`+"\n")
		}, glyphs: []string{"⊘", "⊘", "⊘", "⊘"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, path, p, hash := supervisorGraphFixture(t, cliviewFanout())
			tc.journal(t, e, p, hash)
			strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
			if err != nil || !strict.Terminal() {
				t.Fatalf("glyph fixture must be terminal: %+v err=%v", strict, err)
			}
			want := map[string]string{"✗": "failed", "⊘": "blocked"}
			for i, glyph := range tc.glyphs {
				if strict.Nodes[i].State != want[glyph] {
					t.Fatalf("glyph fixture node states: %+v", strict.Nodes)
				}
			}
			out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
			plain := requireCliviewStatusline(t, out, stderr, code, 80, 4, false)
			for i, glyph := range tc.glyphs {
				requireCliviewGlyph(t, plain, glyph, p.Tasks[i].Task)
			}
			if strings.ContainsAny(plain, "✓○◌⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
				t.Fatalf("failed/blocked snapshot must not show done/ready/pending/spinner glyphs: %q", plain)
			}
		})
	}
}

// 8. Spinner frame is a function of REIN_NOW_UNIX_MS for active nodes.
func TestSupervisorCLIViewStatuslineSpinnerDeterminism(t *testing.T) {
	cliviewEnv(t, "80", false)
	frameAt := func(t *testing.T, e *e2eEnv, path string, now int64) (string, string) {
		t.Helper()
		t.Setenv("REIN_NOW_UNIX_MS", strconv.FormatInt(now, 10))
		out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
		plain := requireCliviewStatusline(t, out, stderr, code, 80, 4, false)
		m := cliviewSpinner.FindStringSubmatch(plain)
		if m == nil {
			t.Fatalf("active node must show a Braille spinner frame before its name: %q", plain)
		}
		return out, m[1]
	}
	t.Run("running", func(t *testing.T) {
		e, path, _, _ := cliviewRunningFixture(t)
		first, frame := frameAt(t, e, path, cliviewNow)
		again, againFrame := frameAt(t, e, path, cliviewNow)
		if again != first || againFrame != frame {
			t.Fatalf("identical time must render identical bytes: first=%q again=%q", first, again)
		}
		_, next := frameAt(t, e, path, cliviewNow+100)
		if next == frame {
			t.Fatalf("time+100ms must advance the spinner frame: both %q", frame)
		}
	})
	t.Run("preparing", func(t *testing.T) {
		e, path, p, hash := supervisorGraphFixture(t, map[string][]string{"task": {}})
		supervisorStatusJournal(t, e, p, hash, `{"seq":2,"task":"task","op_id":"`+strings.Repeat("a", 64)+`","phase":"intent","operation":"prepare"}`+"\n")
		strict, err := supervisor.ReadDAGStatus(context.Background(), p, hash)
		if err != nil || strict.Nodes[0].State != "preparing" {
			t.Fatalf("preparing fixture: %+v err=%v", strict, err)
		}
		frameAt(t, e, path, cliviewNow)
	})
}

// 9. tui on a non-TTY (captured pipe) falls back to the plain graph bytes.
func TestSupervisorCLIViewTUINonTTYFallsBackToGraph(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			cliviewEnv(t, strconv.Itoa(width), true)
			for _, tc := range []struct {
				name    string
				journal func(*testing.T, *e2eEnv, supervisor.Plan, string)
			}{
				{name: "missing_journal"},
				{name: "prepare_failure", journal: cliviewFailureJournal},
			} {
				t.Run(tc.name, func(t *testing.T) {
					e, path, p, hash := supervisorGraphFixture(t, cliviewFanout())
					if tc.journal != nil {
						tc.journal(t, e, p, hash)
					}
					graph, graphErr, graphCode := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "graph")
					requireSupervisorGraph(t, graph, graphErr, graphCode)
					out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "tui")
					requireSupervisorGraph(t, out, stderr, code)
					if out != graph || stderr != graphErr {
						t.Fatalf("non-TTY tui must print the plain graph bytes: graph=%q tui=%q stderr=%q", graph, out, stderr)
					}
				})
			}
		})
	}
}

// 10. statusline and tui are read-only and need no controller lock.
func TestSupervisorCLIViewReadOnlyWithHeldController(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{name: "missing_journal"},
		{name: "header_only", tail: ""},
		{name: "partial_tail", tail: `{"seq":2,"phase":"intent"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cliviewEnv(t, "80", true)
			e, path, p, hash := supervisorGraphFixture(t, cliviewFanout())
			if tc.name != "missing_journal" {
				supervisorStatusJournal(t, e, p, hash, tc.tail)
			}
			controller, err := run.AcquireController(e.wt, hash)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			// cliviewStatus fails the test if any fixture path changes.
			out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
			requireCliviewNames(t, requireCliviewStatusline(t, out, stderr, code, 80, 4, true), p)
			out, stderr, code = cliviewStatus(t, e, "", "status", "--plan", path, "--view", "tui")
			requireSupervisorGraph(t, out, stderr, code)
			requireSupervisorGraphNodes(t, out, p)
			if err := controller.Check(); err != nil {
				t.Fatalf("statusline/tui invalidated the held controller: %v", err)
			}
		})
	}
}

// 11. Invalid statusline/tui option combinations exit 2 before any read/write.
func TestSupervisorCLIViewRejectsInvalidOptions(t *testing.T) {
	cliviewEnv(t, "80", false)
	e, path, p, hash := supervisorGraphFixture(t, cliviewFanout())
	supervisorStatusJournal(t, e, p, hash, "")
	// Positive controls: the new views are accepted and table/graph are unchanged.
	out, stderr, code := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "statusline")
	requireCliviewStatusline(t, out, stderr, code, 80, 4, false)
	out, stderr, code = cliviewStatus(t, e, "", "status", "--plan", path, "--view", "graph")
	requireSupervisorGraph(t, out, stderr, code)
	table, tableErr, tableCode := cliviewStatus(t, e, "", "status", "--plan", path)
	explicit, explicitErr, explicitCode := cliviewStatus(t, e, "", "status", "--plan", path, "--view", "table")
	if tableCode != 0 || table == "" || explicitCode != 0 || explicit != table || explicitErr != tableErr {
		t.Fatalf("table default must be unchanged: default=(%d,%q,%q) explicit=(%d,%q,%q)", tableCode, table, tableErr, explicitCode, explicit, explicitErr)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "statusline_json", args: []string{"--view", "statusline", "--json"}, want: "cannot be combined with --json"},
		{name: "tui_json", args: []string{"--view", "tui", "--json"}, want: "cannot be combined with --json"},
		{name: "statusline_watch", args: []string{"--view", "statusline", "--watch"}, want: "cannot be combined with --watch"},
		{name: "statusline_watch_interval", args: []string{"--view", "statusline", "--watch=1s"}, want: "cannot be combined with --watch"},
		{name: "statusline_seq", args: []string{"--view", "statusline", "--seq", "1"}, want: "--seq requires --view graph"},
		{name: "tui_seq", args: []string{"--view", "tui", "--seq", "1"}, want: "--seq requires --view graph"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A missing plan proves rejection precedes reading: statusline would
			// otherwise fall back to a `rein:` line with exit 0.
			for _, plan := range []string{path, filepath.Join(t.TempDir(), "absent.json")} {
				args := append([]string{"status", "--plan", plan}, tc.args...)
				out, stderr, code := cliviewStatus(t, e, "", args...)
				if code != 2 || out != "" || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "invalid --view") || strings.Contains(stderr, "flag provided but not defined") {
					t.Fatalf("invalid statusline/tui options must exit 2 with %q before reading: exit=%d stdout=%q stderr=%q", tc.want, code, out, stderr)
				}
			}
		})
	}
}
