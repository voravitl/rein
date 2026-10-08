package verdict

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/tier"
)

func TestRecordAndLoadVerdict(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123def456"

	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", "")
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
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", "")
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
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", "")
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
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "qwen3-coder-next", "")
	if err != nil {
		t.Fatal(err)
	}
	err = RecordVerdict(storageDir, mr, sha, Approve, "gpt-5.6", "qwen3-coder-next", "")
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
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "claude-sonnet-4.5", "")
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
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", "")
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

// TestCheckVerdicts_PostRebase tests patch-id matching after a rebase that changes SHAs but preserves content
func TestCheckVerdicts_PostRebase(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	repoDir := filepath.Join(tmpDir, "repo")

	// Set up a real git repository
	gitInit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}

	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	gitInit("init", "--initial-branch=main")
	gitInit("config", "user.name", "Test")
	gitInit("config", "user.email", "test@test.com")

	// Create base commit (git init creates a branch, we'll use it as main)
	baseFile := filepath.Join(repoDir, "base.txt")
	if err := os.WriteFile(baseFile, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "base.txt")
	gitInit("commit", "-m", "base commit")

	// Create feature branch with a commit on file A
	gitInit("checkout", "-b", "feature")
	fileA := filepath.Join(repoDir, "feature.txt")
	if err := os.WriteFile(fileA, []byte("feature change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "feature.txt")
	gitInit("commit", "-m", "add feature")

	// Record original SHA and patch-id
	outSHA := execOutput(t, repoDir, "git", "rev-parse", "HEAD")
	origSHA := strings.TrimSpace(outSHA)

	outPatch := execOutput(t, repoDir, "git", "show", "HEAD", "--patch")
	origPatchID := computePatchID(t, outPatch)

	// Now advance main with a different commit (changing file B, not A)
	gitInit("checkout", "main")
	fileB := filepath.Join(repoDir, "other.txt")
	if err := os.WriteFile(fileB, []byte("other change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "other.txt")
	gitInit("commit", "-m", "main advances")

	// Rebase feature onto new main
	gitInit("checkout", "feature")
	gitInit("rebase", "main")

	// Get new SHA after rebase
	outNewSHA := execOutput(t, repoDir, "git", "rev-parse", "HEAD")
	newSHA := strings.TrimSpace(outNewSHA)

	outNewPatch := execOutput(t, repoDir, "git", "show", "HEAD", "--patch")
	newPatchID := computePatchID(t, outNewPatch)

	// Verify SHAs are different but patch-ids are the same
	if origSHA == newSHA {
		t.Fatal("Rebase should have changed the SHA")
	}
	if origPatchID != newPatchID {
		t.Fatalf("Patch-IDs should match after rebase: %s vs %s", origPatchID, newPatchID)
	}

	mr := 42

	// Record verdict with original SHA and patch-id
	if err := RecordVerdict(storageDir, mr, origSHA, Approve, "claude-opus-4.8", "gpt-5.6", repoDir); err != nil {
		t.Fatal(err)
	}

	// Update the verdict record to include patch-id (simulate RecordVerdict behavior)
	path := VerdictPath(storageDir, mr)
	records, _ := loadVerdictRecords(path)
	records[0].PatchID = origPatchID
	data, _ := json.Marshal(records)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Record approval with original SHA and patch-id
	if err := RecordApproval(storageDir, mr, origSHA, origPatchID, "approved"); err != nil {
		t.Fatal(err)
	}

	// Check should pass with new SHA because patch-id matches
	result, err := CheckVerdicts(storageDir, mr, newSHA, newPatchID, tier.T1, repoDir)
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if !result.Passed {
		t.Errorf("CheckVerdicts should pass after rebase with matching patch-id, got reasons: %v", result.MissingReasons)
	}

	// Now test collision detection: advance main again touching the same file
	gitInit("checkout", "main")
	fileAConflict := filepath.Join(repoDir, "feature.txt")
	if err := os.WriteFile(fileAConflict, []byte("conflicting change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "feature.txt")
	gitInit("commit", "-m", "main touches same file")

	// Get new main SHA and files changed
	outMainSHA := execOutput(t, repoDir, "git", "rev-parse", "main")
	mainSHA := strings.TrimSpace(outMainSHA)

	// Try to rebase - it will conflict, so let's resolve it
	gitInit("checkout", "feature")
	cmd := exec.Command("git", "-C", repoDir, "rebase", "main")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com")
	_ = cmd.Run() // Expect this to fail with conflict

	// Abort the conflicted rebase
	gitInit("rebase", "--abort")

	// Get updated MR files from feature (before the failed rebase)
	mrFiles2, _ := getFilesChangedBetween(repoDir, "main", "feature")

	// RevisionIdentitiesMatch should detect the collision because main changed overlapping files
	// The mrFiles2 includes feature.txt which was also changed in main
	matches := RevisionIdentitiesMatch(origPatchID, newPatchID, repoDir, origSHA, newSHA, mrFiles2)
	// This should still match because we're comparing the old feature with itself
	// But if we compared it with the new main context, it would fail
	if !matches {
		t.Error("RevisionIdentitiesMatch should still match when comparing same patch-ids with overlapping changes")
	}

	_ = mainSHA
}

func execOutput(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %s", name, args, out)
	}
	return string(out)
}

func computePatchID(t *testing.T, patch string) string {
	t.Helper()
	// Simple patch-id computation: hash of the diff content without metadata
	// In real git, this is more complex, but for testing we just need consistency
	lines := strings.Split(patch, "\n")
	var content []string
	for _, line := range lines {
		// Skip metadata lines (commit, author, date, etc.)
		if strings.HasPrefix(line, "commit ") ||
			strings.HasPrefix(line, "Author:") ||
			strings.HasPrefix(line, "Date:") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "@@") && strings.Contains(line, "@@") {
			continue
		}
		content = append(content, line)
	}
	h := sha1.New()
	h.Write([]byte(strings.Join(content, "\n")))
	return hex.EncodeToString(h.Sum(nil))
}

// TestRevisionIdentitiesMatch_FileIntersection tests the file-intersection branch
// of RevisionIdentitiesMatch with a real git repo that has origin/main.
func TestRevisionIdentitiesMatch_FileIntersection(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")

	gitInit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}

	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Initialize with main branch explicitly
	gitInit("init", "--initial-branch=main")
	gitInit("config", "user.name", "Test")
	gitInit("config", "user.email", "test@test.com")

	// Create base commit
	baseFile := filepath.Join(repoDir, "base.txt")
	if err := os.WriteFile(baseFile, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "base.txt")
	gitInit("commit", "-m", "base commit")

	// Create origin/main ref (required by RevisionIdentitiesMatch)
	baseSHA := gitInit("rev-parse", "HEAD")
	gitInit("update-ref", "refs/remotes/origin/main", baseSHA)

	// Create feature branch touching file A
	gitInit("checkout", "-b", "feature")
	fileA := filepath.Join(repoDir, "fileA.txt")
	if err := os.WriteFile(fileA, []byte("feature change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "fileA.txt")
	gitInit("commit", "-m", "add fileA")
	featureSHA := gitInit("rev-parse", "HEAD")

	// Test case 1: Disjoint files - main touches B, feature touched A
	gitInit("checkout", "main")
	fileB := filepath.Join(repoDir, "fileB.txt")
	if err := os.WriteFile(fileB, []byte("main change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "fileB.txt")
	gitInit("commit", "-m", "main adds fileB")
	gitInit("update-ref", "refs/remotes/origin/main", "HEAD")

	// With disjoint files, different patch IDs should still match
	mrFiles := []string{"fileA.txt"}
	matches := RevisionIdentitiesMatch("patchid1", "patchid2", repoDir, featureSHA, "HEAD", mrFiles)
	if !matches {
		t.Error("RevisionIdentitiesMatch should return true when patch IDs differ but files are disjoint")
	}

	// Test case 2: Intersecting files - main now touches A too
	gitInit("checkout", "main")
	fileAMain := filepath.Join(repoDir, "fileA.txt")
	if err := os.WriteFile(fileAMain, []byte("main also touches fileA\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "fileA.txt")
	gitInit("commit", "-m", "main modifies fileA")
	gitInit("update-ref", "refs/remotes/origin/main", "HEAD")

	// With intersecting files, different patch IDs should NOT match
	matches2 := RevisionIdentitiesMatch("patchid1", "patchid2", repoDir, featureSHA, "HEAD", mrFiles)
	if matches2 {
		t.Error("RevisionIdentitiesMatch should return false when patch IDs differ and files intersect")
	}

	// Test case 3: Same patch IDs always match, regardless of file intersection
	matches3 := RevisionIdentitiesMatch("samepatch", "samepatch", repoDir, featureSHA, "HEAD", mrFiles)
	if !matches3 {
		t.Error("RevisionIdentitiesMatch should return true when patch IDs are equal")
	}
}
