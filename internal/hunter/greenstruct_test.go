package hunter

// Green-evidence structure (migration §4 step 2): VerifyFix must validate
// the STRUCTURE of the green evidence, not just that it names the fixed
// revision. `strings.Contains(greenEvidence, fixedRevision)` accepts the
// bare string "def456" — a claim with no failing baseline, no passing run of
// the same check, and no regression result.
//
// The green evidence must carry all four, bound to the finding's own record:
//  1. `fixed=<fixedRevision>` — the revision the green run names;
//  2. `FAIL@<baseline>` — the SAME check failed on the finding's baseline
//     (the red side, tied to this finding, not free text);
//  3. `PASS@<fixedRevision>` — the SAME check passed at the fixed revision;
//  4. `existing-pass` — the existing suite still passes (no regression by fix).
//
// Test-only. No production path changes.

import (
	"path/filepath"
	"testing"
)

func TestVerifyFixNeedsStructuredGreenEvidence(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id := toFixInProgress(t) // baseline abc123, red proof on record

	// Even complete textual structure is not independently verified evidence.
	full := "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@def456 existing-pass"
	if err := VerifyFix(id, "def456", "def456", full); err == nil {
		t.Fatal("text-only verification closed finding")
	}

	cases := []struct {
		name  string
		green string
	}{
		// Bare revision: names the fix but proves nothing ran.
		{"bare-revision", "def456"},
		// Fixed marker without the red side: no FAIL on the buggy baseline.
		{"no-fail", "runner:green-after-1 fixed=def456 PASS@def456 existing-pass"},
		// FAIL on the wrong baseline: not THIS finding's red.
		{"fail-wrong-baseline", "runner:green-after-1 fixed=def456 FAIL@zzz999 PASS@def456 existing-pass"},
		// Fixed marker without the green side: no PASS at the fixed revision.
		{"no-pass", "runner:green-after-1 fixed=def456 FAIL@abc123 existing-pass"},
		// PASS on the wrong revision: not the revision being closed.
		{"pass-wrong-revision", "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@zzz999 existing-pass"},
		// No regression result: the fix may have broken everything else.
		{"no-regression", "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@def456"},
		// Vague all-green: the old substring check's blind spot in reverse
		// (names no revision at all).
		{"vague", "runner:vague-all-green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
			fresh := toFixInProgress(t)
			if err := VerifyFix(fresh, "def456", "def456", tc.green); err == nil {
				t.Fatalf("unstructured green accepted: %q", tc.green)
			}
			got, _ := Get(fresh)
			if got.State != FixInProgress {
				t.Fatalf("state = %s after refused close, want FIX_IN_PROGRESS (unchanged)", got.State)
			}
		})
	}
}
