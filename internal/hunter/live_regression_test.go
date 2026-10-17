package hunter

// Live-review regression tests (2026-10-14): four genuine bypasses found by
// live advisor runs (code-reviewer + test-engineer via claude-opus-5-5 and
// codex gpt-6.1-sol, run live-advise-20261014) and verified against the code
// by the coordinator before writing these tests.
//
// F-LIVE-1: Move closes to VERIFIED_FIXED directly, skipping every check
// VerifyFix makes (fixed revision, HEAD match, green structure).
// F-LIVE-2: Report accepts a finding that arrives already closed
// (State: VERIFIED_FIXED with attempts) — a closed claim with no gate.
// F-LIVE-3: Move(id, Duplicate, id) closes as its own duplicate
// (finding.go:416 matches self on f.ID == evidence).
// F-LIVE-4: Blocked detour skips triage AND approval
// (Candidate -> Blocked -> FixInProgress needs red proof since E1 but no
// triage or approved fix task — VerifyFix then assumes approval from state).
//
// Test-only. No production path changes.

import (
	"path/filepath"
	"testing"
)

func TestLive_MoveCannotCloseDirectlyToVerifiedFixed(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/a.go", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAttempt(id, ReproAttempt{
		Command: "x", Cwd: "/tmp", Exit: 1, Outcome: "reproduced", At: "2026-10-14T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		to State
		ev string
	}{
		{Reproduced, "r:red"},
		{Triaged, "c:triage"},
		{ApprovedForFX, "o:approve"},
		{FixInProgress, "w:fix"},
	} {
		if err := Move(id, step.to, step.ev); err != nil {
			t.Fatalf("Move to %s: %v", step.to, err)
		}
	}
	// F-LIVE-1: the direct close must be refused — only VerifyFix closes.
	if err := Move(id, VerifiedFixed, "w:claims-fixed"); err == nil {
		t.Fatal("Move closed to VERIFIED_FIXED directly, skipping VerifyFix (no fixed revision, no HEAD check, no green structure)")
	}
	got, _ := Get(id)
	if got.State != FixInProgress {
		t.Fatalf("state = %s after refused close, want FIX_IN_PROGRESS (unchanged)", got.State)
	}
}

func TestLive_ReportRefusesAlreadyClosedFinding(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	f := testFinding("t", "src/b.go", 2)
	f.State = VerifiedFixed
	f.Attempts = []ReproAttempt{{
		Command: "x", Cwd: "/tmp", Exit: 1, Outcome: "reproduced", At: "2026-10-14T00:00:00Z",
	}}
	// F-LIVE-2: a report arriving already closed must be refused.
	if id, dup, err := Report(f); err == nil {
		t.Fatalf("Report accepted an already-closed finding as %s (dup=%v): closed claims must go through the gates", id, dup)
	}
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("refused report left %d findings in the registry", len(all))
	}
}

func TestLive_MoveCannotDuplicateToSelf(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/c.go", 3))
	if err != nil {
		t.Fatal(err)
	}
	// F-LIVE-3: naming itself as the original must be refused.
	if err := Move(id, Duplicate, id); err == nil {
		t.Fatal("Move to DUPLICATE with itself as original accepted (DuplicateOf points at itself, no separate original)")
	}
	got, _ := Get(id)
	if got.State != Candidate {
		t.Fatalf("state = %s after refused self-duplicate, want CANDIDATE (unchanged)", got.State)
	}
	if got.DuplicateOf != "" {
		t.Fatalf("DuplicateOf = %q after refused move, want empty", got.DuplicateOf)
	}
}

func TestLive_BlockedDetourNeedsApproval(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/d.go", 4))
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAttempt(id, ReproAttempt{
		Command: "x", Cwd: "/tmp", Exit: 1, Outcome: "reproduced", At: "2026-10-14T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Blocked, "c:blocked-waiting"); err != nil {
		t.Fatal(err)
	}
	// F-LIVE-4: Blocked -> FixInProgress without triage or approval must be
	// refused — the fix task was never approved, so VerifyFix must not later
	// assume approval from the state alone.
	if err := Move(id, FixInProgress, "w:fix-without-approval"); err == nil {
		t.Fatal("Blocked -> FixInProgress accepted with no triage and no approved fix task (approval bypass)")
	}
	got, _ := Get(id)
	if got.State != Blocked {
		t.Fatalf("state = %s after refused detour, want BLOCKED (unchanged)", got.State)
	}
	// The legitimate path still works: Blocked resumes to where it came
	// from (CANDIDATE), then walks the full chain with approval.
	if err := Move(id, Candidate, "c:unblocked-resume"); err != nil {
		t.Fatalf("Blocked -> Candidate (resume): %v", err)
	}
	if err := RecordAttempt(id, ReproAttempt{
		Command: "x", Cwd: "/tmp", Exit: 1, Outcome: "reproduced", At: "2026-10-14T00:02:00Z",
	}); err != nil {
		t.Fatalf("re-record after resume: %v", err)
	}
	for _, step := range []struct {
		to State
		ev string
	}{
		{Reproduced, "r:red-2"},
		{Triaged, "c:triage"},
		{ApprovedForFX, "o:approve"},
		{FixInProgress, "w:fix"},
	} {
		if err := Move(id, step.to, step.ev); err != nil {
			t.Fatalf("Move to %s after resume: %v", step.to, err)
		}
	}
}
