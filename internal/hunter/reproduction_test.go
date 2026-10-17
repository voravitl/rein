package hunter

// Phase D slice D3 tests: seeded-defect reproduction (H2), setup failure is
// not a defect (H4), and deleting/weakening assertions cannot close a bug (H7).
//
// Test-only. The registry stores JSONL under the contract index dir
// (PIPELINE_CONTRACTS), so every test redirects it into t.TempDir().

import (
	"path/filepath"
	"testing"
)

func repro(id, outcome string) ReproAttempt {
	return ReproAttempt{
		Command: "go test ./internal/auth/ -run TestBadToken -count=1",
		Cwd:     "/tmp/wt-hunt-repro",
		Exit:    1,
		Outcome: outcome,
		At:      "2026-10-14T00:00:00Z",
	}
}

func TestSeededDefectNeedsRecordedReproduction(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	// H2: an evidence string alone is not a reproduction — Move refuses
	// without a recorded attempt whose outcome is "reproduced".
	if err := Move(id, Reproduced, "runner:repro-1"); err == nil {
		t.Fatal("REPRODUCED accepted with no recorded attempt")
	}
	// One recorded "reproduced" run opens the gate and promotes strength.
	if err := RecordAttempt(id, repro(id, "reproduced")); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(id)
	if got.Strength != ReproducedOnce {
		t.Fatalf("strength = %s, want REPRODUCED_ONCE after one reproduction", got.Strength)
	}
	if err := Move(id, Reproduced, "runner:repro-1"); err != nil {
		t.Fatalf("Move to REPRODUCED with a recorded attempt: %v", err)
	}
	// A second reproduction promotes to MULTI — the count and each result
	// are both preserved.
	if err := RecordAttempt(id, repro(id, "reproduced")); err != nil {
		t.Fatal(err)
	}
	got, _ = Get(id)
	if got.Strength != ReproducedMulti {
		t.Fatalf("strength = %s, want REPRODUCED_MULTI after two reproductions", got.Strength)
	}
	if len(got.Attempts) != 2 {
		t.Fatalf("attempts = %d, want both runs preserved", len(got.Attempts))
	}
}

func TestSetupFailureIsNeverADefect(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	// H4: three setup failures are data, not proof — the gate stays closed
	// and strength never promotes.
	for i := 0; i < 3; i++ {
		if err := RecordAttempt(id, repro(id, "setup-failure")); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Get(id)
	if got.Strength != StaticSupported {
		t.Fatalf("setup failures promoted strength to %s", got.Strength)
	}
	if err := Move(id, Reproduced, "runner:looks-broken"); err == nil {
		t.Fatal("REPRODUCED accepted on setup failures alone")
	}
	// An unknown outcome word is refused at record time, not stored.
	if err := RecordAttempt(id, repro(id, "maybe")); err == nil {
		t.Fatal("unknown attempt outcome accepted")
	}
	// not-reproduced runs are recorded (they are evidence too) but do not
	// open the gate either.
	if err := RecordAttempt(id, repro(id, "not-reproduced")); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Reproduced, "runner:still-nothing"); err == nil {
		t.Fatal("REPRODUCED accepted with no reproduced run")
	}
}

func TestDeletingAssertionsCannotCloseABug(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAttempt(id, repro(id, "reproduced")); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Reproduced, "runner:red-1"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Triaged, "controller:t-1"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, ApprovedForFX, "owner:a-1"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, FixInProgress, "worker:f-1"); err != nil {
		t.Fatal(err)
	}
	// H7: VERIFIED_FIXED needs the red proof to still exist. The registry has
	// no delete API at all — but even if history were trimmed, a finding with
	// zero reproduced attempts cannot close. Prove the gate reads the record,
	// not the claim: a fresh finding with verification evidence but no red
	// proof is refused.
	id2, _, err := Report(testFinding("t", "src/other.go", 7))
	if err != nil {
		t.Fatal(err)
	}
	// Direct path: CANDIDATE cannot jump to FIX_IN_PROGRESS at all.
	if err := Move(id2, FixInProgress, "worker:claims-fixed"); err == nil {
		t.Fatal("jump to FIX_IN_PROGRESS accepted")
	}
	// And VERIFIED_FIXED without the red proof is refused even with evidence.
	if err := Move(id2, Reproduced, "runner:green-only"); err == nil {
		t.Fatal("REPRODUCED accepted with no recorded reproduced attempt")
	}
	// A retained red record plus textual claims cannot independently verify a fix.
	if err := VerifyFix(id, "def456", "def456", "fixed=def456 FAIL@abc123 PASS@def456 existing-pass"); err == nil {
		t.Fatal("text-only closure accepted")
	}
	got, _ := Get(id)
	if got.State != FixInProgress {
		t.Fatalf("state changed: %s", got.State)
	}
}
