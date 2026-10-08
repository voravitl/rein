package verdict

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voravitl/rein/internal/tier"
)

func TestRecordAndLoadVerdict(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123def456"

	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6")
	if err != nil {
		t.Fatalf("RecordVerdict failed: %v", err)
	}

	path := VerdictPath(storageDir, mr)
	records, err := loadVerdictRecords(path)
	if err != nil {
		t.Fatalf("loadVerdictRecords failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	r := records[0]
	if r.MR != mr {
		t.Errorf("MR = %d, want %d", r.MR, mr)
	}
	if r.SHA != sha {
		t.Errorf("SHA = %q, want %q", r.SHA, sha)
	}
	if r.Verdict != Approve {
		t.Errorf("Verdict = %v, want %v", r.Verdict, Approve)
	}
	if r.ReviewerModel != "claude-opus-4.8" {
		t.Errorf("ReviewerModel = %q, want %q", r.ReviewerModel, "claude-opus-4.8")
	}
	if r.WorkerModel != "gpt-5.6" {
		t.Errorf("WorkerModel = %q, want %q", r.WorkerModel, "gpt-5.6")
	}
}

func TestRecordAndLoadApproval(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123def456"
	patchID := "patch123"
	reason := "LGTM"

	err := RecordApproval(storageDir, mr, sha, patchID, reason)
	if err != nil {
		t.Fatalf("RecordApproval failed: %v", err)
	}

	path := ApprovalPath(storageDir, mr)
	records, err := loadApprovalRecords(path)
	if err != nil {
		t.Fatalf("loadApprovalRecords failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	r := records[0]
	if r.MR != mr {
		t.Errorf("MR = %d, want %d", r.MR, mr)
	}
	if r.SHA != sha {
		t.Errorf("SHA = %q, want %q", r.SHA, sha)
	}
	if r.PatchID != patchID {
		t.Errorf("PatchID = %q, want %q", r.PatchID, patchID)
	}
	if r.Reason != reason {
		t.Errorf("Reason = %q, want %q", r.Reason, reason)
	}
}

func TestCheckVerdicts_T1_OneApprover(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Record verdict from one maker (different from worker)
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6")
	if err != nil {
		t.Fatal(err)
	}

	// Record human approval
	err = RecordApproval(storageDir, mr, sha, patchID, "approved")
	if err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if !result.Passed {
		t.Errorf("CheckVerdicts should pass for T1 with 1 maker, got reasons: %v", result.MissingReasons)
	}
	if result.MakerCount != 1 {
		t.Errorf("MakerCount = %d, want 1", result.MakerCount)
	}
	if !result.HasApproval {
		t.Error("HasApproval should be true")
	}
}

func TestCheckVerdicts_T3_NeedsTwoMakers(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Record verdict from one maker only
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6")
	if err != nil {
		t.Fatal(err)
	}

	// Record human approval
	err = RecordApproval(storageDir, mr, sha, patchID, "approved")
	if err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T3, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if result.Passed {
		t.Error("CheckVerdicts should fail for T3 with only 1 maker")
	}
	if result.MakerCount != 1 {
		t.Errorf("MakerCount = %d, want 1", result.MakerCount)
	}
}

func TestCheckVerdicts_T3_TwoMakers(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Record verdicts from two different makers (both different from worker)
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "qwen3-coder-next")
	if err != nil {
		t.Fatal(err)
	}
	err = RecordVerdict(storageDir, mr, sha, Approve, "gpt-5.6", "qwen3-coder-next")
	if err != nil {
		t.Fatal(err)
	}

	// Record human approval
	err = RecordApproval(storageDir, mr, sha, patchID, "approved")
	if err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T3, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if !result.Passed {
		t.Errorf("CheckVerdicts should pass for T3 with 2 makers, got reasons: %v", result.MissingReasons)
	}
	if result.MakerCount != 2 {
		t.Errorf("MakerCount = %d, want 2", result.MakerCount)
	}
}

func TestCheckVerdicts_ReviewerSameAsWorker(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Record verdict where reviewer is same maker as worker (should be ignored)
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "claude-sonnet-4.5")
	if err != nil {
		t.Fatal(err)
	}

	// Record human approval
	err = RecordApproval(storageDir, mr, sha, patchID, "approved")
	if err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if result.Passed {
		t.Error("CheckVerdicts should fail when reviewer maker equals worker maker")
	}
	if result.MakerCount != 0 {
		t.Errorf("MakerCount = %d, want 0", result.MakerCount)
	}
}

func TestCheckVerdicts_NoApproval(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Record verdict
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6")
	if err != nil {
		t.Fatal(err)
	}

	// No human approval

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if result.Passed {
		t.Error("CheckVerdicts should fail without human approval")
	}
	if result.HasApproval {
		t.Error("HasApproval should be false")
	}
}

func TestCheckVerdicts_MissingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts should not error on missing files, got: %v", err)
	}

	if result.Passed {
		t.Error("CheckVerdicts should fail when files are missing")
	}
	if len(result.MissingReasons) == 0 {
		t.Error("expected missing reasons")
	}
}

func TestCheckVerdicts_CorruptFile(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Write corrupt verdict file
	if err := EnsureDir(storageDir); err != nil {
		t.Fatal(err)
	}
	corruptData := []byte("not json")
	if err := os.WriteFile(VerdictPath(storageDir, mr), corruptData, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err == nil {
		t.Error("CheckVerdicts should fail closed on corrupt file")
	}
}
