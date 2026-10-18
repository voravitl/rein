package main

// Kiro-parser gap (live finding, run live-advise-20261014): kiro-cli's
// current output no longer echoes the prompt with a `> ` prefix — the live
// log (110 lines) starts with `[tool] ...` lines and answers in markdown
// directly. The parser in advise.sh:68 (`awk '/^> /{p=1} p'`) therefore
// keeps nothing: OUT is 0 lines, the ledger row is `--failed`, and the spent
// quota is invisible. A failed parse must stay fail-closed (ledger --failed),
// but a successful answer in the current format must be kept.
//
// This test pins, against the REAL script with a fake kiro-cli emitting the
// CURRENT format (no `> ` lines, tool-status preamble, markdown answer):
// the answer reaches OUT non-empty and the ledger row has no --failed.
//
// Test-only. No production path changes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Current kiro-cli format (observed 2026-10-14, live log
// advise-kiro-tracer-124442.log): tool-status preamble, no `> ` echo, the
// markdown answer directly. Representative head, not the whole 110 lines.
const kiroCurrentFormatLog = `[tool] Finding **/* in hunter-live-codex-work
[tool] status: Completed
[tool] Reading finding.go:1
[tool] status: Completed
## Trace Report

### Observation
The gates are enforced per entry point instead of on every way into a state.

### Current Best Explanation
` + "`Move`" + ` and ` + "`Report`" + ` both go around the fix-verification chain.
`

func TestCmdAdvise_KiroCurrentFormatKeepsAnswer(t *testing.T) {
	tmp := t.TempDir()
	scriptDir := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rolesDir := filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "tracer.md"), []byte("# tracer\n"), 0o644); err != nil {
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
	calls := filepath.Join(tmp, "calls.log")
	writeFake := func(name, code string) {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("#!/bin/sh\n"+code+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFake("rein", `echo "rein $*" >> "`+calls+`"; exit 0`)
	// Fake kiro-cli emits the CURRENT format: preamble + markdown, no `> `.
	writeFake("kiro-cli", `printf '%s' "`+strings.ReplaceAll(kiroCurrentFormatLog, "`", "'")+`"`)
	writeFake("timeout", `shift; exec "$@"`)
	task := filepath.Join(tmp, "task.md")
	if err := os.WriteFile(task, []byte("Review without writing files.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "answer.md")

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)
	t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))
	t.Setenv("REIN", filepath.Join(tmp, "rein"))
	t.Setenv("ADVISE_NO_PACK", "1")

	code := cmdAdvise([]string{
		"--provider", "kiro", "--model", "claude-opus-5.5",
		"--run", "run-r", "--task", "contract-task",
		"tracer", tmp, task, out,
	})
	if code != 0 {
		t.Fatalf("kiro current-format exit = %d, want 0 (the answer is in the log)", code)
	}
	b, err := os.ReadFile(out)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		t.Fatalf("kiro current-format answer lost: OUT empty (err=%v) — the live log's 110 lines parsed to nothing", err)
	}
	if !strings.Contains(string(b), "Current Best Explanation") {
		t.Fatalf("OUT lacks the answer body:\n%s", b)
	}
	if strings.Contains(string(b), "[tool]") {
		t.Fatalf("OUT leaks tool-status preamble:\n%s", b)
	}
	callsB, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("no rein call recorded: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(callsB)), "\n") {
		if strings.HasPrefix(line, "rein ledger call") && strings.Contains(line, "--failed") {
			t.Fatalf("kept answer wrongly marked --failed: %s", line)
		}
	}
}
