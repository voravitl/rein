package hunter

// Phase D slice D5 tests: hunter-draft workflow integration (H1 end-to-end,
// H2 full chain to VERIFIED_FIXED).
//
// agents/rein-bug-hunter.md was approved (report-only, supervised) on
// 2026-10-14 after passing this integration test plus live review and the
// F-LIVE fixes. This is that test: it runs the approved workflow end to end — SCAN contract, Report,
// REPRODUCE contract separation, recorded reproduction attempts, lifecycle
// moves, freshness gate, fix verification — with real contracts judged by the
// real guard, real shell reproduction commands (exit codes observed, never
// invented), and the real registry.
//
// What is real here: guard decisions (guard.CheckBash), registry
// (Report/RecordAttempt/Move/RequireFresh/VerifyFix), shell exit codes.
// What is fixture (labeled): the seeded defect, the worktree, the revision
// strings. No live Orca/OMC/provider calls; no real advisor run.
//
// Test-only. No production path changes.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/guard"
)

// runSh runs script with sh in dir and returns the observed exit code.
// The command string, cwd and exit code are exactly what the workflow
// records into the attempt — output is irrelevant, the exit code is the
// evidence.
func runSh(t *testing.T, dir, script string) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell repro commands need sh")
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		return 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	} else {
		t.Fatalf("sh failed: %v", err)
		return -1
	}
}

func TestWorkflow_CleanFixtureReportsNoConfirmedFindings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell repro commands need sh")
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	e := newReproEnv(t)
	// The fixture ships clean: the app file has no defect marker.
	// 1. SCAN sees only what the contract allows.
	scan := e.scanContract(t)
	if why := guard.CheckBash("echo x > src/auth.go", scan, e.wt, e.wt); why == "" {
		t.Fatal("SCAN contract allows writing application source")
	}
	// 2. The scan runs: grep for the defect marker, observed exit 1.
	exit := runSh(t, e.wt, "grep -q ALLOW_ALL src/auth.go")
	if exit != 1 {
		t.Fatalf("clean-fixture scan exit = %d, want 1 (marker absent)", exit)
	}
	// 3. Nothing reproduced, so nothing is reported: the registry stays empty.
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("clean fixture opened %d findings, want 0", len(all))
	}
	// 4. The report names what was covered and what was not — and never
	// claims BUG_FREE.
	report := NoConfirmedFindings("t", "abc123",
		[]string{"src/auth.go (SCAN, read-only; grep exit 1)"},
		[]string{"e2e scope (no URL/account authorization — blocked, not skipped silently)"})
	for _, want := range []string{"NO_CONFIRMED_FINDINGS", "abc123", "scope covered", "scope not covered"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	for _, forbidden := range []string{"BUG_FREE", "NO_BUGS", "CLEAN"} {
		if strings.Contains(report, forbidden) {
			t.Errorf("report claims %q:\n%s", forbidden, report)
		}
	}
}

func TestWorkflow_SeededDefectToVerifiedFixed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell repro commands need sh")
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	e := newReproEnv(t)
	// Seed the defect (labeled fixture): the auth check allows everything.
	defect := "package auth\n// ALLOW_ALL=true -- seeded defect, test fixture only\n"
	if err := os.WriteFile(e.appFile, []byte(defect), 0o644); err != nil {
		t.Fatal(err)
	}
	// 1. SCAN: application source stays read-only under the scan contract.
	scan := e.scanContract(t)
	if why := guard.CheckBash("echo x > src/auth.go", scan, e.wt, e.wt); why == "" {
		t.Fatal("SCAN contract allows writing application source")
	}
	// 2. The scan finds the marker; the candidate is reported as STATIC_SUPPORTED.
	exit := runSh(t, e.wt, "grep -q ALLOW_ALL src/auth.go")
	if exit != 0 {
		t.Fatalf("seeded-defect scan exit = %d, want 0 (marker present)", exit)
	}
	f := testFinding("t", "src/auth.go", 2)
	f.Symbol = ""
	f.Expected = "auth check denies unauthenticated requests"
	f.Actual = "ALLOW_ALL=true present: auth check allows everything"
	f.Invariant = "auth decisions must deny unauthenticated requests"
	f.Trigger = "grep -q ALLOW_ALL src/auth.go (exit 0)"
	f.Impact = "unauthenticated access"
	id, dup, err := Report(f)
	if err != nil || dup {
		t.Fatalf("Report = %s dup=%v err=%v", id, dup, err)
	}
	// 3. REPRODUCE: a worker contract is refused as reproducer; the separate
	// test-only contract is accepted.
	if ok, _ := IsReproducerContract(e.workerContract(t), []string{"src/**"}, []string{"hunter-tests/**"}); ok {
		t.Fatal("worker contract passes as a reproducer contract")
	}
	if ok, why := IsReproducerContract(e.reproContract(t), []string{"src/**"}, []string{"hunter-tests/**"}); !ok {
		t.Fatalf("REPRODUCE contract rejected: %s", why)
	}
	// 4. Reproduction runs the SAME check the scan found, records the exact
	// command/cwd/exit — then the gate opens on the recorded attempt, not on
	// the evidence string alone.
	second, _, err := Report(testFinding("t", "src/other.go", 7))
	if err != nil {
		t.Fatal(err)
	}
	if err := Move(second, Reproduced, "runner:claims-repro"); err == nil {
		t.Fatal("REPRODUCED accepted with no recorded attempt (evidence string alone)")
	}
	cmd := "grep -q ALLOW_ALL src/auth.go"
	red := runSh(t, e.wt, cmd)
	if red != 0 {
		t.Fatalf("red run exit = %d, want 0", red)
	}
	if err := RecordAttempt(id, ReproAttempt{
		Command: cmd, Cwd: e.wt, Exit: red, Env: "hunter=repro",
		Outcome: "reproduced", At: "2026-10-14T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Reproduced, "runner:red-1"); err != nil {
		t.Fatalf("Move to REPRODUCED with a recorded attempt: %v", err)
	}
	for _, step := range []struct {
		to State
		ev string
	}{
		{Triaged, "controller:triage-1"},
		{ApprovedForFX, "owner:approve-1"},
		{FixInProgress, "worker:fix-1"},
	} {
		if err := Move(id, step.to, step.ev); err != nil {
			t.Fatalf("Move to %s: %v", step.to, err)
		}
	}
	// 5. The fix removes the marker; the SAME check now exits 1. The green
	// run is recorded too — count and per-run results both survive (H2).
	if err := os.WriteFile(e.appFile, []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	green := runSh(t, e.wt, cmd)
	if green != 1 {
		t.Fatalf("green run exit = %d, want 1 (marker gone)", green)
	}
	if err := RecordAttempt(id, ReproAttempt{
		Command: cmd, Cwd: e.wt, Exit: green, Env: "hunter=repro",
		Outcome: "not-reproduced", At: "2026-10-14T00:01:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(id)
	if len(got.Attempts) != 2 {
		t.Fatalf("attempts = %d, want red + green both preserved", len(got.Attempts))
	}
	if got.Strength != ReproducedOnce {
		t.Fatalf("strength = %s, want REPRODUCED_ONCE (one red run)", got.Strength)
	}
	// 6. Stale proof cannot close: the baseline is abc123, HEAD moved on.
	if err := RequireFresh(id, "def456"); err == nil {
		t.Fatal("stale evidence accepted as fresh")
	}
	ev := "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@def456 existing-pass"
	if err := VerifyFix(id, "def456", "zzz999", ev); err == nil {
		t.Fatal("stale HEAD accepted at close")
	}
	// 7. Text claims cannot replace a runtime verifier and independent review.
	if err := VerifyFix(id, "def456", "def456", ev); err == nil {
		t.Fatal("text-only closure accepted")
	}
	got, _ = Get(id)
	if got.State != FixInProgress {
		t.Fatalf("state changed: %s", got.State)
	}
	if !strings.Contains(strings.Join(got.Evidence, " "), "runner:red-1") {
		t.Fatal("red evidence lost")
	}
}
