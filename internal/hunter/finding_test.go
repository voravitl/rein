package hunter

// Phase D slice D1 tests: finding schema, fingerprint dedupe, the lifecycle
// with its evidence gates, and the NO_CONFIRMED_FINDINGS rule.
//
// Test-only. The registry stores JSONL under the contract index dir
// (PIPELINE_CONTRACTS), so every test redirects it into t.TempDir().

import (
	"path/filepath"
	"strings"
	"testing"
)

func testFinding(task, file string, line int) Finding {
	return Finding{
		Task: task, Run: "r", Baseline: "abc123",
		File: file, Line: line, Symbol: "Handle",
		Expected: "returns 401 for a bad token", Actual: "returns 200 and serves data",
		Invariant: "auth decisions must deny unauthenticated requests",
		Trigger:   "GET /data with Authorization: Bearer bad",
		Impact:    "unauthenticated data access",
		Severity:  "high: unauthenticated access to protected data", Confidence: "high",
		Strength:    StaticSupported,
		Evidence:    []string{"sha256:static-analysis-report"},
		Limitations: "static analysis only, no runtime reproduction yet",
		NextAction:  "isolated reproduction in a test-only worktree",
	}
}

func TestReportAssignsStableIDAndFingerprint(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, dup, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil || dup || id != "H-0001" {
		t.Fatalf("Report = %s dup=%v err=%v", id, dup, err)
	}
	got, err := Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Candidate {
		t.Fatalf("new finding state = %s, want CANDIDATE", got.State)
	}
	want := FingerprintOf("abc123", "src/auth.go", 42, "Handle", "auth decisions must deny unauthenticated requests")
	if got.Fingerprint != want {
		t.Fatalf("fingerprint = %s, want %s", got.Fingerprint, want)
	}
}

func TestReportRefusesIncompleteFindings(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	for name, mutate := range map[string]func(*Finding){
		"no task":      func(f *Finding) { f.Task = "" },
		"no baseline":  func(f *Finding) { f.Baseline = "" },
		"no file":      func(f *Finding) { f.File = "" },
		"no line":      func(f *Finding) { f.Line = 0 },
		"no expected":  func(f *Finding) { f.Expected = "" },
		"no invariant": func(f *Finding) { f.Invariant = "" },
		"bad task":     func(f *Finding) { f.Task = "../x" },
	} {
		t.Run(name, func(t *testing.T) {
			f := testFinding("t", "src/auth.go", 42)
			mutate(&f)
			if _, _, err := Report(f); err == nil {
				t.Fatalf("Report accepted %+v", f)
			}
		})
	}
}

func TestDuplicateKeepsOriginalEvidence(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id1, dup, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil || dup {
		t.Fatal(id1, dup, err)
	}
	// Same defect, different words: same fingerprint, refused as duplicate.
	f2 := testFinding("t", "src/auth.go", 42)
	f2.Expected = "a completely different description of the same defect"
	id2, dup, err := Report(f2)
	if err != nil || !dup || id2 != id1 {
		t.Fatalf("duplicate = %s dup=%v err=%v, want original id + dup", id2, dup, err)
	}
	// A different line is a different finding.
	f3 := testFinding("t", "src/auth.go", 43)
	if _, dup, err := Report(f3); err != nil || dup {
		t.Fatalf("distinct finding refused: dup=%v err=%v", dup, err)
	}
	all, err := List()
	if err != nil || len(all) != 2 {
		t.Fatalf("registry holds %d findings, want 2: %v", len(all), err)
	}
	if all[0].Evidence[0] != "sha256:static-analysis-report" {
		t.Fatalf("original evidence rewritten: %+v", all[0])
	}
}

func TestLifecycleNeedsEvidenceAndCannotSkip(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	// Cannot skip CANDIDATE -> TRIAGED.
	if err := Move(id, Triaged, "ev:1"); err == nil {
		t.Fatal("skipped REPRODUCED accepted")
	}
	// REPRODUCED without evidence is refused.
	if err := Move(id, Reproduced, ""); err == nil {
		t.Fatal("evidence-less REPRODUCED accepted")
	}
	// REPRODUCED with evidence but no recorded attempt is refused (H2): the
	// string alone is not a reproduction.
	if err := Move(id, Reproduced, "runner:repro-0"); err == nil {
		t.Fatal("REPRODUCED accepted with no recorded attempt")
	}
	// One recorded "reproduced" run opens the gate.
	if err := RecordAttempt(id, ReproAttempt{Command: "go test ./internal/auth/ -count=1", Cwd: "/tmp/wt", Exit: 1, Outcome: "reproduced"}); err != nil {
		t.Fatal(err)
	}
	// The forward path works with evidence at each gate; the close itself
	// goes through VerifyFix (Move cannot reach VERIFIED_FIXED — F-LIVE-1).
	for _, step := range []struct {
		to State
		ev string
	}{
		{Reproduced, "runner:repro-1"},
		{Triaged, "controller:triage-1"},
		{ApprovedForFX, "owner:approve-1"},
		{FixInProgress, "worker:fix-1"},
	} {
		if err := Move(id, step.to, step.ev); err != nil {
			t.Fatalf("Move to %s: %v", step.to, err)
		}
	}
	if err := VerifyFix(id, "def456", "def456", "runner:green-after-1 fixed=def456 FAIL@abc123 PASS@def456 existing-pass"); err == nil {
		t.Fatal("text-only closure accepted")
	}
	got, _ := Get(id)
	if got.State != FixInProgress {
		t.Fatalf("state = %s, want FIX_IN_PROGRESS", got.State)
	}
	if len(got.Evidence) < 4 {
		t.Fatalf("evidence trail lost: %+v", got.Evidence)
	}
	// VERIFIED_FIXED is terminal.
	if err := Move(id, FixInProgress, "ev:x"); err == nil {
		t.Fatal("reopening VERIFIED_FIXED accepted")
	}
}

func TestAuxiliaryStates(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, _ := Report(testFinding("t", "src/auth.go", 42))
	// BLOCKED and back.
	if err := Move(id, Blocked, ""); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Candidate, ""); err != nil {
		t.Fatal(err)
	}
	// REGRESSED returns to FIX_IN_PROGRESS, never straight to VERIFIED_FIXED.
	// A recorded reproduced attempt opens the REPRODUCED gate (H2).
	if err := RecordAttempt(id, ReproAttempt{Command: "go test ./internal/auth/ -count=1", Cwd: "/tmp/wt", Exit: 1, Outcome: "reproduced"}); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Reproduced, "runner:r"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Triaged, "controller:t"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, ApprovedForFX, "owner:a"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, FixInProgress, "worker:f"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Regressed, "runner:still-broken"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, VerifiedFixed, "runner:green"); err == nil {
		t.Fatal("REGRESSED -> VERIFIED_FIXED accepted")
	}
	if err := Move(id, FixInProgress, "worker:f2"); err != nil {
		t.Fatal(err)
	}
	// DUPLICATE needs the original.
	id2, _, _ := Report(testFinding("t", "src/other.go", 7))
	if err := Move(id2, Duplicate, "no-such-id"); err == nil {
		t.Fatal("DUPLICATE without an original accepted")
	}
	if err := Move(id2, Duplicate, id); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(id2)
	if got.State != Duplicate || got.DuplicateOf != id {
		t.Fatalf("duplicate not linked: %+v", got)
	}
	// FLAKY keeps the finding alive for another reproduction.
	id3, _, _ := Report(testFinding("t", "src/flaky.go", 1))
	if err := RecordAttempt(id3, ReproAttempt{Command: "go test ./flaky/ -count=1", Cwd: "/tmp/wt", Exit: 1, Outcome: "reproduced"}); err != nil {
		t.Fatal(err)
	}
	if err := Move(id3, Reproduced, "runner:r1"); err != nil {
		t.Fatal(err)
	}
	if err := Move(id3, Flaky, ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordAttempt(id3, ReproAttempt{Command: "go test ./flaky/ -count=2", Cwd: "/tmp/wt", Exit: 1, Outcome: "reproduced"}); err != nil {
		t.Fatal(err)
	}
	if err := Move(id3, Reproduced, "runner:r2"); err != nil {
		t.Fatal(err)
	}
	// Unknown IDs and corrupt lines fail closed.
	if err := Move("H-9999", Triaged, "ev"); err == nil {
		t.Fatal("unknown finding moved")
	}
}

func TestStaticStrengthNeverClaimsRuntimeReproduction(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "src/auth.go", 42))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Get(id)
	if got.Strength != StaticSupported {
		t.Fatalf("strength = %s, want STATIC_SUPPORTED", got.Strength)
	}
	if len(got.Attempts) != 0 {
		t.Fatalf("static finding carries runtime attempts: %+v", got.Attempts)
	}
	// The schema keeps the two apart: a runtime claim needs a recorded
	// attempt, and the move to REPRODUCED needs runner evidence naming it.
	if err := RecordAttempt(id, ReproAttempt{Command: "go test ./internal/auth/ -count=1", Cwd: "/tmp/wt", Exit: 1, Outcome: "reproduced"}); err != nil {
		t.Fatal(err)
	}
	if err := Move(id, Reproduced, "runner:repro-with-attempts-1"); err != nil {
		t.Fatal(err)
	}
	got, _ = Get(id)
	if !strings.Contains(strings.Join(got.Evidence, " "), "runner:repro-with-attempts-1") {
		t.Fatalf("evidence trail missing the runner reference: %+v", got.Evidence)
	}
}
