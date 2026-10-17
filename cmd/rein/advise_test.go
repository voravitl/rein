package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdAdvise_ArgValidation(t *testing.T) {
	// Insufficient positional args
	if code := cmdAdvise([]string{}); code != 2 {
		t.Fatalf("expected code 2 on empty args, got %d", code)
	}
	if code := cmdAdvise([]string{"role", "dir", "task"}); code != 2 {
		t.Fatalf("expected code 2 on 3 positional args, got %d", code)
	}
	// Invalid flag
	if code := cmdAdvise([]string{"-invalid-flag"}); code != 2 {
		t.Fatalf("expected code 2 on invalid flag, got %d", code)
	}
}

func TestFindAdviseScript(t *testing.T) {
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts", "advise.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)
	found := findAdviseScript()
	if found != scriptPath {
		t.Fatalf("expected %s, got %s", scriptPath, found)
	}
}

func TestCmdAdvise_MockExecution(t *testing.T) {
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts", "advise.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Mock script records its arguments and env vars to a file
	logFile := filepath.Join(tmp, "args.log")
	scriptContent := `#!/bin/sh
echo "args: $@" > "` + logFile + `"
echo "provider: $ADVISE_PROVIDER" >> "` + logFile + `"
echo "timeout: $ADVISE_TIMEOUT" >> "` + logFile + `"
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)

	code := cmdAdvise([]string{
		"--provider", "kiro",
		"--model", "test-model",
		"--timeout", "10s",
		"critic", tmp, "task.md", "out.md",
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	str := string(logged)
	expectedArgs := "args: critic " + tmp + " task.md out.md test-model\n"
	if !containsStr(str, expectedArgs) {
		t.Errorf("expected %q in output, got %q", expectedArgs, str)
	}
	if !containsStr(str, "provider: kiro\n") {
		t.Errorf("expected provider kiro in output, got %q", str)
	}
	if !containsStr(str, "timeout: 10\n") {
		t.Errorf("expected timeout 10 in output, got %q", str)
	}
}

func containsStr(s, sub string) bool {
	return filepath.Clean(s) != "" && (s == sub || len(s) >= len(sub) && (s[:len(sub)] == sub || containsSubstr(s, sub)))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Phase C slice C1 (O2): an unknown advisor role fails closed and never
// reaches a model call. advise.sh resolves the role from
// templates/roles/ then OMC agents and exits 2 when neither has it
// (advise.sh:28-30); the route check (review receipt) runs only after the
// role file is found, so a missing role consumes no receipt and spends no
// model call. This test pins that ordering against the real script with a
// fake rein binary and fake provider CLIs, the same harness the python
// suite (test_advise_routing.py) uses.
func TestCmdAdvise_UnknownRoleFailsClosed(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts", "advise.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Layout the script expects next to itself: templates/roles/ with one
	// known role, an empty OMC agents dir, and advisor-rules.md.
	rolesDir := filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "critic.md"), []byte("# critic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	omcAgents := filepath.Join(tmp, "omc-agents")
	if err := os.MkdirAll(omcAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "advisor-rules.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Copy the real advise.sh but point its OMC lookup at the empty dir,
	// keeping the shebang on line 1 so the copy stays executable.
	real, err := os.ReadFile(filepath.Join(realRepoRoot(t), "skills", "worktree-pipeline", "scripts", "advise.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(real)
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	}
	pinned := "#!/bin/bash\n# test pin: OMC agents dir\nnpm() { echo " + omcAgents + "; }\n" + body
	if err := os.WriteFile(script, []byte(pinned), 0o755); err != nil {
		t.Fatal(err)
	}

	calls := filepath.Join(tmp, "calls.jsonl")
	writeFake := func(name, code string) {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+code+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Fake rein records any route/ledger call; fake codex records a model
	// call and writes the -o file like the python suite's fake does.
	writeFake("rein", `echo "$@" >> "`+calls+`"; exit "${ROUTE_EXIT:-0}"`)
	writeFake("codex", `echo model-call "$@" >> "`+calls+`"
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
[ -n "$out" ] && printf 'Verified answer\n' > "$out"
echo "tokens used"
echo "123"`)
	writeFake("timeout", `shift; exec "$@"`)
	task := filepath.Join(tmp, "task.md")
	if err := os.WriteFile(task, []byte("Review without writing files.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "answer.md")
	if err := os.WriteFile(out, []byte("Previous report\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)
	t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))
	t.Setenv("REIN", filepath.Join(tmp, "rein"))
	t.Setenv("ADVISE_NO_PACK", "1")

	code := cmdAdvise([]string{
		"--provider", "codex", "--model", "gpt-test",
		"--run", "run-r", "--task", "contract-task",
		"no-such-role", tmp, task, out,
	})
	if code != 2 {
		t.Fatalf("unknown role exit = %d, want 2 (fail closed)", code)
	}
	if b, err := os.ReadFile(calls); err == nil {
		t.Fatalf("unknown role reached route/model calls: %s", b)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "Previous report\n" {
		t.Fatalf("unknown role touched prior answer: %q err=%v", b, err)
	}

	// Control: the known role reaches route check with the same fakes.
	if err := os.WriteFile(out, []byte("Previous report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code = cmdAdvise([]string{
		"--provider", "codex", "--model", "gpt-test",
		"--run", "run-r", "--task", "contract-task",
		"critic", tmp, task, out,
	})
	if code != 0 {
		t.Fatalf("known role exit = %d, want 0", code)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("known role made no route call: %v", err)
	}
	if !strings.Contains(string(b), "route check") {
		t.Fatalf("known role skipped route check: %s", b)
	}
	// The fake rein answers ROUTE_EXIT=0, so the script proceeds to the
	// model; that keeps the control honest: an unknown role stops before
	// route check, a known role does not.
	if !strings.Contains(string(b), "model-call") {
		t.Fatalf("known role did not reach model call: %s", b)
	}
}

// realRepoRoot finds the checkout root (the dir holding skills/) by walking
// up from this test file; it never guesses a fixed path.
func realRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "skills", "worktree-pipeline", "scripts", "advise.sh")); err == nil && !fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("checkout root with skills/worktree-pipeline not found")
		}
		dir = parent
	}
}

// Phase C slice C9 (O4 — every advisor call is accounted, including failures).
//
// advise.sh:78-81 records every call in the model ledger: success writes a
// plain row, a failed or empty answer writes `--failed`. The python suite
// pins that a ledger WRITE failure is a failure (test_post_call_ledger_failure
// is_failure), but nothing pins that a failed ADVISOR call is itself recorded
// as failed — the case the supervisor's budget depends on, because an
// unrecorded failure is invisible spend ("unknown" must stay unknown, never
// zero, and a missing row is not even unknown).
//
// This test pins, against the REAL script with fake rein/codex/timeout:
//  1. a successful call records a ledger row WITHOUT --failed;
//  2. a failed provider call records a ledger row WITH --failed;
//  3. a ledger write failure fails the whole call (exit != 0), so the caller
//     retries the recording before another call (advise.sh:79-81).
//
// Test-only. No production path changes.
func TestCmdAdvise_FailedCallsAreRecorded(t *testing.T) {
	setup := func(t *testing.T, providerExit int, ledgerExit int) (tmp string, out string, calls string) {
		t.Helper()
		tmp = t.TempDir()
		scriptDir := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts")
		if err := os.MkdirAll(scriptDir, 0o755); err != nil {
			t.Fatal(err)
		}
		rolesDir := filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "roles")
		if err := os.MkdirAll(rolesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rolesDir, "critic.md"), []byte("# critic\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		omcAgents := filepath.Join(tmp, "omc-agents")
		if err := os.MkdirAll(omcAgents, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "advisor-rules.md"), []byte("rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		real, err := os.ReadFile(filepath.Join(realRepoRoot(t), "skills", "worktree-pipeline", "scripts", "advise.sh"))
		if err != nil {
			t.Fatal(err)
		}
		body := string(real)
		if i := strings.Index(body, "\n"); i >= 0 {
			body = body[i+1:]
		}
		if err := os.WriteFile(filepath.Join(scriptDir, "advise.sh"), []byte("#!/bin/bash\nnpm() { echo "+omcAgents+"; }\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		calls = filepath.Join(tmp, "calls.log")
		writeFake := func(name, code string) {
			if err := os.WriteFile(filepath.Join(tmp, name), []byte("#!/bin/sh\n"+code+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeFake("rein", `echo "rein $*" >> "`+calls+`"
if [ "$1" = route ]; then exit 0; fi
if [ "$1" = ledger ]; then exit ${LEDGER_EXIT:-0}; fi
exit 2`)
		if providerExit == 0 {
			writeFake("codex", `echo model-call "$@" >> "`+calls+`"
out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
[ -n "$out" ] && printf 'Verified answer\n' > "$out"
echo "tokens used"; echo "123"`)
		} else {
			writeFake("codex", `echo model-call "$@" >> "`+calls+`"; exit 3`)
		}
		writeFake("timeout", `shift; exec "$@"`)
		task := filepath.Join(tmp, "task.md")
		if err := os.WriteFile(task, []byte("Review without writing files.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out = filepath.Join(tmp, "answer.md")
		if err := os.WriteFile(out, []byte("Previous report\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)
		t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))
		t.Setenv("REIN", filepath.Join(tmp, "rein"))
		t.Setenv("ADVISE_NO_PACK", "1")
		t.Setenv("ADVISE_RUN", "run-r")
		t.Setenv("ADVISE_TASK", "contract-task")
		t.Setenv("ADVISE_PROVIDER", "codex")
		t.Setenv("ADVISE_MODEL", "gpt-test")
		t.Setenv("PIPELINE_LOGDIR", filepath.Join(tmp, "logs"))
		if ledgerExit == 0 {
			t.Setenv("LEDGER_EXIT", "0")
		} else {
			t.Setenv("LEDGER_EXIT", "1")
		}
		return tmp, out, calls
	}

	advise := func(tmp, out string) int {
		task := filepath.Join(tmp, "task.md")
		return cmdAdvise([]string{
			"--provider", "codex", "--model", "gpt-test",
			"--run", "run-r", "--task", "contract-task",
			"critic", tmp, task, out,
		})
	}
	ledgerLines := func(calls string) []string {
		t.Helper()
		b, err := os.ReadFile(calls)
		if err != nil {
			t.Fatalf("no rein call recorded: %v", err)
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if strings.HasPrefix(line, "rein ledger call") {
				out = append(out, line)
			}
		}
		return out
	}

	// 1. Success: one ledger row WITHOUT --failed, exit 0.
	tmp, out, calls := setup(t, 0, 0)
	if code := advise(tmp, out); code != 0 {
		t.Fatalf("success exit = %d, want 0", code)
	}
	rows := ledgerLines(calls)
	if len(rows) != 1 {
		t.Fatalf("success recorded %d ledger rows, want 1: %v", len(rows), rows)
	}
	for _, want := range []string{"--role critic", "--provider codex", "--model gpt-test", "--tokens 123"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("success ledger lacks %q: %s", want, rows[0])
		}
	}
	if strings.Contains(rows[0], "--failed") {
		t.Errorf("success ledger wrongly marked --failed: %s", rows[0])
	}

	// 2. Failed provider: the call is recorded WITH --failed, exit != 0, and
	// the failure text is kept for the human.
	tmp, out, calls = setup(t, 3, 0)
	if code := advise(tmp, out); code == 0 {
		t.Fatal("failed provider exit = 0, want non-zero")
	}
	rows = ledgerLines(calls)
	if len(rows) != 1 {
		t.Fatalf("failure recorded %d ledger rows, want exactly 1 --failed row: %v", len(rows), rows)
	}
	if !strings.Contains(rows[0], "--failed") {
		t.Errorf("failed advisor call not marked --failed: %s", rows[0])
	}
	for _, want := range []string{"--role critic", "--run run-r", "--task contract-task"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("failed ledger lacks attribution %q: %s", want, rows[0])
		}
	}

	// 3. Ledger write failure: the whole call fails (exit != 0), so the caller
	// retries the recording before another call — no silent unrecorded spend.
	tmp, out, _ = setup(t, 0, 1)
	if code := advise(tmp, out); code == 0 {
		t.Fatal("ledger-write failure exit = 0, want non-zero")
	}
}
