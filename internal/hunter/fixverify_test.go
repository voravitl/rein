package hunter

// Phase E slice E1 tests: fix verification red-before/green-after (H11).
//
// A finding closes as VERIFIED_FIXED only through VerifyFix, which requires
// the full chain on record:
//  1. state is FIX_IN_PROGRESS (an approved fix task exists);
//  2. at least one recorded attempt with outcome "reproduced" (red proof);
//  3. green evidence naming the fixed revision (FAIL on the buggy baseline,
//     PASS on the fixed revision, existing tests still pass);
//  4. checkout HEAD matches the fixed revision (no stale close).
//
// Test-only. The registry stores JSONL under the contract index dir
// (PIPELINE_CONTRACTS), so every test redirects it into t.TempDir().

import (
	"path/filepath"
	"strings"
	"testing"
)

// toFixInProgress walks a fresh finding to FIX_IN_PROGRESS with a recorded
// red proof on file, returning its ID.
func toFixInProgress(t *testing.T) string {
	t.Helper()
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordAttempt(id, ReproAttempt{
		Command: "go test ./internal/auth/ -run TestBadToken -count=1",
		Cwd:     "/tmp/wt-hunt-repro", Exit: 1, Outcome: "reproduced",
	}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		to State
		ev string
	}{
		{Reproduced, "runner:red-1"},
		{Triaged, "controller:triage-1"},
		{ApprovedForFX, "owner:approve-1"},
		{FixInProgress, "worker:fix-1"},
	} {
		if err := Move(id, step.to, step.ev); err != nil {
			t.Fatalf("Move to %s: %v", step.to, err)
		}
	}
	return id
}

func TestVerifyFixNeedsFullChain(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id := toFixInProgress(t)
	green := "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@def456 existing-pass"

	// Claims that look like a complete chain still contain no observations or reviewer authority.
	if err := VerifyFix(id, "def456", "def456", green); err == nil {
		t.Fatal("text-only closure accepted")
	}
	got, _ := Get(id)
	if got.State != FixInProgress {
		t.Fatalf("state changed: %s", got.State)
	}

}

func TestVerifyFixRefusals(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	green := "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@def456 existing-pass"

	// Unknown finding fails closed.
	if err := VerifyFix("H-9999", "def456", "def456", green); err == nil {
		t.Fatal("unknown finding closed")
	}
	// Empty fixed revision or evidence fails closed.
	id := toFixInProgress(t)
	if err := VerifyFix(id, "", "def456", green); err == nil {
		t.Fatal("empty fixed revision accepted")
	}
	if err := VerifyFix(id, "def456", "def456", ""); err == nil {
		t.Fatal("empty green evidence accepted")
	}
	// Wrong state fails closed: only FIX_IN_PROGRESS may close.
	id2, _, _ := Report(testFinding("t", "src/other.go", 7))
	if err := VerifyFix(id2, "def456", "def456", green); err == nil {
		t.Fatal("close from CANDIDATE accepted")
	}
	// Green evidence that does not name the fixed revision fails closed.
	if err := VerifyFix(id, "def456", "def456", "runner:vague-all-green"); err == nil {
		t.Fatal("green evidence without the fixed revision accepted")
	}
	// Stale HEAD fails closed: the checkout moved past the green run.
	if err := VerifyFix(id, "def456", "ghi789", green); err == nil {
		t.Fatal("stale HEAD accepted")
	} else if !strings.Contains(err.Error(), "ghi789") || !strings.Contains(err.Error(), "def456") {
		t.Fatalf("stale error does not name both revisions: %v", err)
	}
	// Empty HEAD fails closed: unknown is never fresh.
	if err := VerifyFix(id, "def456", "", green); err == nil {
		t.Fatal("empty HEAD accepted")
	}
}

func TestVerifyFixNeedsRedProof(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	// A finding walked to FIX_IN_PROGRESS can only get there with a red proof
	// under the current gates — prove the close itself also reads the record:
	// if history were trimmed, the close must still refuse.
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	// CANDIDATE cannot reach FIX_IN_PROGRESS at all, so there is no path to
	// close without ever reproducing.
	if err := VerifyFix(id, "def456", "def456", "runner:green fixed=def456"); err == nil {
		t.Fatal("close without any reproduction accepted")
	}
	// The bypass path is now closed at entry: CANDIDATE -> Blocked ->
	// FixInProgress is refused at the last step because no red proof is on
	// record. VerifyFix would also refuse at close time; this gate stops it
	// earlier, before a fix task starts on an unreproduced bug.
	idB, _, err := Report(testFinding("t", "src/bypass.go", 9))
	if err != nil {
		t.Fatal(err)
	}
	if err := Move(idB, Blocked, ""); err != nil {
		t.Fatal(err)
	}
	if err := Move(idB, FixInProgress, "worker:claims-fixed"); err == nil {
		t.Fatal("Blocked -> FixInProgress accepted with no red proof on record")
	} else if !strings.Contains(err.Error(), "red proof") {
		t.Fatalf("wrong refusal reason: %v", err)
	}
}
