package verdict

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	// Record verdict where reviewer is same model as worker (should be ignored)
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-sonnet-4.5", "claude-sonnet-4.5", "")
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
		t.Error("CheckVerdicts should fail when reviewer model equals worker model")
	}
	if result.MakerCount != 0 {
		t.Errorf("MakerCount = %d, want 0", result.MakerCount)
	}
}

func TestCheckVerdicts_OpusReviewingSonnet(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 42
	sha := "abc123"
	patchID := "patch123"

	// Record verdict where reviewer is Opus and worker is Sonnet (escalated review: allowed)
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

	if !result.Passed {
		t.Errorf("CheckVerdicts should pass when Opus reviews Sonnet, got missing reasons: %v", result.MissingReasons)
	}
	if result.MakerCount != 1 {
		t.Errorf("MakerCount = %d, want 1", result.MakerCount)
	}
}

func TestCheckVerdicts_OpusReviewingHaiku(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 43
	sha := "def456"
	patchID := "patch456"

	// Record verdict where reviewer is Opus and worker is Haiku (escalated review: allowed)
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-3.7", "claude-haiku", "")
	if err != nil {
		t.Fatal(err)
	}

	err = RecordApproval(storageDir, mr, sha, patchID, "approved")
	if err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if !result.Passed {
		t.Errorf("CheckVerdicts should pass when Opus reviews Haiku, got missing reasons: %v", result.MissingReasons)
	}
}

func TestCheckVerdicts_SonnetReviewingOpus(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 44
	sha := "ghi789"
	patchID := "patch789"

	// Record verdict where reviewer is Sonnet and worker is Opus (lower tier reviewing higher: disallowed)
	err := RecordVerdict(storageDir, mr, sha, Approve, "claude-sonnet-4.5", "claude-opus-4.8", "")
	if err != nil {
		t.Fatal(err)
	}

	err = RecordApproval(storageDir, mr, sha, patchID, "approved")
	if err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if result.Passed {
		t.Error("CheckVerdicts should fail when Sonnet reviews Opus")
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
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
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

	// F-01: binding comes from the revision identity, not from which files main
	// touched. The rebased revision is proven unchanged against its approved
	// predecessor even though main moved, and a further feature change breaks
	// that proof while origin/main holds still.
	gitInit("update-ref", "refs/remotes/origin/main", "main")
	rebaseID, err := ComputeRevisionID(repoDir, "origin/main", newSHA)
	if err != nil || rebaseID == "" {
		t.Fatalf("ComputeRevisionID for the rebased revision failed: %v (id %q)", err, rebaseID)
	}
	if !RevisionIdentitiesMatch(origPatchID, rebaseID, repoDir, origSHA, newSHA) {
		t.Error("RevisionIdentitiesMatch must accept the unchanged patch rebase")
	}

	featureFile := filepath.Join(repoDir, "feature.txt")
	if err := os.WriteFile(featureFile, []byte("changed after approval\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInit("add", "feature.txt")
	gitInit("commit", "-m", "unapproved change")
	changedSHA := strings.TrimSpace(execOutput(t, repoDir, "git", "rev-parse", "HEAD"))
	changedID, err := ComputeRevisionID(repoDir, "origin/main", "HEAD")
	if err != nil || changedID == "" {
		t.Fatalf("ComputeRevisionID for the changed revision failed: %v (id %q)", err, changedID)
	}
	if changedID == rebaseID {
		t.Fatal("fixture error: changed content must produce a different revision ID")
	}
	if RevisionIdentitiesMatch(rebaseID, changedID, repoDir, newSHA, changedSHA) {
		t.Error("changed feature content must not match the approved revision")
	}
}

func execOutput(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
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

// TestRevisionIdentitiesMatch_ChangedContentAndRebase covers the semantics that
// replaced the file-intersection rule: changed feature content is rejected even
// with origin/main unchanged, while an unchanged patch rebase is accepted.
func TestRevisionIdentitiesMatch_ChangedContentAndRebase(t *testing.T) {
	repoDir := initVerdictFeatureRepo(t)
	baseFile := filepath.Join(repoDir, "base.txt")

	// Feature commit B: edit the last line.
	writeFixtureFile(t, baseFile, "line1\nline2\nline3\nline4\nline5 v1\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "feature B")
	bSHA := gitRun(t, repoDir, "rev-parse", "HEAD")

	// Feature commit C: changes the same file again; origin/main is unchanged.
	writeFixtureFile(t, baseFile, "line1\nline2\nline3\nline4\nline5 v2\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "feature C")
	cSHA := gitRun(t, repoDir, "rev-parse", "HEAD")

	if RevisionIdentitiesMatch("id-b", "id-c", repoDir, bSHA, cSHA) {
		t.Error("changed feature content must not match, even with origin/main unchanged")
	}

	// Unchanged patch rebase: advance origin/main above the edited line and rebase.
	gitRun(t, repoDir, "checkout", "main")
	writeFixtureFile(t, baseFile, "inserted\nline1\nline2\nline3\nline4\nline5\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "main advances")
	gitRun(t, repoDir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, repoDir, "checkout", "feature")
	gitRun(t, repoDir, "rebase", "main")

	rebasedSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	if rebasedSHA == cSHA {
		t.Fatal("fixture error: the rebase should rewrite the commit")
	}
	if !RevisionIdentitiesMatch("id-c", "id-rebased", repoDir, cSHA, rebasedSHA) {
		t.Error("an unchanged patch rebase must match")
	}

	// Equal patch IDs always match; empty and unresolvable revisions fail closed.
	if !RevisionIdentitiesMatch("same-id", "same-id", repoDir, bSHA, cSHA) {
		t.Error("equal patch IDs must match")
	}
	if RevisionIdentitiesMatch("", "id-c", repoDir, bSHA, cSHA) {
		t.Error("an empty patch ID must never match")
	}
	if RevisionIdentitiesMatch("id-b", "id-c", repoDir, "no-such-revision", cSHA) {
		t.Error("an unresolvable revision must fail closed")
	}
}

// gitRun runs git inside a disposable fixture. Host hooks and global
// configuration are disabled so the fixture cannot be perturbed by the caller's
// Git setup.
func gitRun(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// initVerdictFeatureRepo builds the F-01 fixture: a base commit on main, an
// origin/main ref pinned to it, and a checked-out feature branch.
func initVerdictFeatureRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	gitRun(t, repoDir, "init", "--initial-branch=main")
	gitRun(t, repoDir, "config", "user.name", "Test")
	gitRun(t, repoDir, "config", "user.email", "test@test.com")
	writeFixtureFile(t, filepath.Join(repoDir, "base.txt"), "line1\nline2\nline3\nline4\nline5\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "base commit")
	gitRun(t, repoDir, "update-ref", "refs/remotes/origin/main", gitRun(t, repoDir, "rev-parse", "HEAD"))
	gitRun(t, repoDir, "checkout", "-b", "feature")
	return repoDir
}

// TestCheckVerdicts_ChangedFeatureCommitRejected is the F-01 regression: a
// commit that changes the feature content must not inherit a verdict and human
// approval recorded for an earlier commit, even when origin/main has not moved
// since that approval.
func TestCheckVerdicts_ChangedFeatureCommitRejected(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	repoDir := initVerdictFeatureRepo(t)
	mr := 101
	baseFile := filepath.Join(repoDir, "base.txt")

	// Commit B: the approved revision.
	writeFixtureFile(t, baseFile, "line1\nline2\nline3\nline4\nline5 v1\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "feature B")
	bSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	bPatchID, err := ComputeRevisionID(repoDir, "origin/main", "HEAD")
	if err != nil {
		t.Fatalf("ComputeRevisionID for B failed: %v", err)
	}
	if err := RecordVerdict(storageDir, mr, bSHA, Approve, "claude-opus-4.8", "gpt-5.6", repoDir); err != nil {
		t.Fatal(err)
	}
	if err := RecordApproval(storageDir, mr, bSHA, bPatchID, "approved B"); err != nil {
		t.Fatal(err)
	}

	// Commit C: changes the same file again; origin/main stays where it was.
	writeFixtureFile(t, baseFile, "line1\nline2\nline3\nline4\nline5 v2\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "feature C")
	cSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	cPatchID, err := ComputeRevisionID(repoDir, "origin/main", "HEAD")
	if err != nil {
		t.Fatalf("ComputeRevisionID for C failed: %v", err)
	}
	if bSHA == cSHA {
		t.Fatal("fixture error: expected a new commit")
	}
	if bPatchID == cPatchID {
		t.Fatalf("fixture error: changed content must produce a different revision ID: %s", bPatchID)
	}

	result, err := CheckVerdicts(storageDir, mr, cSHA, cPatchID, tier.T1, repoDir)
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if result.Passed {
		t.Errorf("changed feature commit must not inherit an earlier verdict/approval; missing reasons: %v", result.MissingReasons)
	}
	if result.HasApproval {
		t.Error("HasApproval must be false for a changed feature commit")
	}
	if result.MakerCount != 0 {
		t.Errorf("MakerCount = %d, want 0", result.MakerCount)
	}
}

// TestCheckVerdicts_UnchangedRebaseKeepsVerdict is the F-01 accepted case: a
// rebase that leaves the feature diff identical still matches. The stored patch
// IDs are forced to differ (as they can when a record's ID was computed against
// a different origin/main tip), so acceptance must come from the
// unchanged-patch proof rather than patch-ID equality.
func TestCheckVerdicts_UnchangedRebaseKeepsVerdict(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	repoDir := initVerdictFeatureRepo(t)
	mr := 102
	baseFile := filepath.Join(repoDir, "base.txt")

	// Commit B: edit the last line of base.txt.
	writeFixtureFile(t, baseFile, "line1\nline2\nline3\nline4\nline5 feature\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "feature B")
	bSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	if err := RecordVerdict(storageDir, mr, bSHA, Approve, "claude-opus-4.8", "gpt-5.6", repoDir); err != nil {
		t.Fatal(err)
	}
	stalePatchID := "recorded-against-another-base"
	rewriteVerdictPatchID(t, storageDir, mr, stalePatchID)
	if err := RecordApproval(storageDir, mr, bSHA, stalePatchID, "approved B"); err != nil {
		t.Fatal(err)
	}

	// Advance origin/main by inserting a line above the edited line, then rebase.
	gitRun(t, repoDir, "checkout", "main")
	writeFixtureFile(t, baseFile, "inserted\nline1\nline2\nline3\nline4\nline5\n")
	gitRun(t, repoDir, "add", "base.txt")
	gitRun(t, repoDir, "commit", "-m", "main advances")
	gitRun(t, repoDir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, repoDir, "checkout", "feature")
	gitRun(t, repoDir, "rebase", "main")

	cSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	if cSHA == bSHA {
		t.Fatal("fixture error: the rebase should rewrite the commit")
	}
	cPatchID, err := ComputeRevisionID(repoDir, "origin/main", "HEAD")
	if err != nil || cPatchID == "" {
		t.Fatalf("ComputeRevisionID after rebase failed: %v (id %q)", err, cPatchID)
	}
	if cPatchID == stalePatchID {
		t.Fatal("fixture error: patch IDs must differ so the proof path is exercised")
	}

	result, err := CheckVerdicts(storageDir, mr, cSHA, cPatchID, tier.T1, repoDir)
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if !result.Passed {
		t.Errorf("an unchanged patch rebase must stay approved; missing reasons: %v", result.MissingReasons)
	}
	if !result.HasApproval {
		t.Error("HasApproval should remain true for a proven unchanged rebase")
	}
}

func rewriteVerdictPatchID(t *testing.T, storageDir string, mr int, patchID string) {
	t.Helper()
	path := VerdictPath(storageDir, mr)
	records, err := loadVerdictRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range records {
		records[i].PatchID = patchID
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckVerdicts_RequestChangesRevokesApprove covers F-02: a later
// REQUEST_CHANGES from the same reviewer on the same revision retracts the
// earlier APPROVE.
func TestCheckVerdicts_RequestChangesRevokesApprove(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	mr, sha, patchID := 103, "abc123", "patch123"

	if err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordVerdict(storageDir, mr, sha, RequestChanges, "claude-opus-4.8", "gpt-5.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordApproval(storageDir, mr, sha, patchID, "approved"); err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if result.Passed {
		t.Error("CheckVerdicts should fail after the same reviewer requested changes")
	}
	if result.MakerCount != 0 {
		t.Errorf("MakerCount = %d, want 0", result.MakerCount)
	}
	if len(result.MatchingVerdicts) != 0 {
		t.Errorf("MatchingVerdicts len = %d, want 0 (the retracted APPROVE must not be evidence)", len(result.MatchingVerdicts))
	}
}

// TestCheckVerdicts_ApproveAfterRequestChanges covers F-02: a fresh APPROVE
// after a REQUEST_CHANGES is the reviewer's effective verdict again.
func TestCheckVerdicts_ApproveAfterRequestChanges(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	mr, sha, patchID := 104, "abc123", "patch123"

	if err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordVerdict(storageDir, mr, sha, RequestChanges, "claude-opus-4.8", "gpt-5.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordApproval(storageDir, mr, sha, patchID, "approved"); err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if !result.Passed {
		t.Errorf("CheckVerdicts should pass on the reviewer's latest APPROVE; missing reasons: %v", result.MissingReasons)
	}
	if result.MakerCount != 1 {
		t.Errorf("MakerCount = %d, want 1", result.MakerCount)
	}
}

// TestCheckVerdicts_StaleRequestChangesDoesNotRevoke covers F-02: the effective
// verdict reduction runs after revision matching, so a REQUEST_CHANGES bound to
// a different revision cannot retract a current APPROVE.
func TestCheckVerdicts_StaleRequestChangesDoesNotRevoke(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	nonRepoDir := t.TempDir() // makes the recorded patch ID empty
	mr, sha, patchID := 105, "abc123", "patch123"

	if err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "gpt-5.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordVerdict(storageDir, mr, "stale-sha", RequestChanges, "claude-opus-4.8", "gpt-5.6", nonRepoDir); err != nil {
		t.Fatal(err)
	}
	if err := RecordApproval(storageDir, mr, sha, patchID, "approved"); err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if !result.Passed {
		t.Errorf("a stale REQUEST_CHANGES must not revoke the current APPROVE; missing reasons: %v", result.MissingReasons)
	}
	if result.MakerCount != 1 {
		t.Errorf("MakerCount = %d, want 1", result.MakerCount)
	}
}

// TestCheckVerdicts_StaleApproveDoesNotCount covers F-02: an APPROVE bound to
// another revision alone does not satisfy the maker requirement.
func TestCheckVerdicts_StaleApproveDoesNotCount(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")
	nonRepoDir := t.TempDir() // makes the recorded patch ID empty
	mr, sha, patchID := 106, "abc123", "patch123"

	if err := RecordVerdict(storageDir, mr, "stale-sha", Approve, "claude-opus-4.8", "gpt-5.6", nonRepoDir); err != nil {
		t.Fatal(err)
	}
	if err := RecordApproval(storageDir, mr, sha, patchID, "approved"); err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if result.Passed {
		t.Error("CheckVerdicts should fail when the only APPROVE is bound to another revision")
	}
	if result.MakerCount != 0 {
		t.Errorf("MakerCount = %d, want 0", result.MakerCount)
	}
}

func TestCheckVerdicts_Evidence(t *testing.T) {
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "verdicts")

	mr := 99
	sha := "commit999"
	patchID := "patch999"

	// 1. Record an escalated reviewer verdict (Opus reviewing Sonnet)
	if err := RecordVerdict(storageDir, mr, sha, Approve, "claude-opus-4.8", "claude-sonnet-4.5", ""); err != nil {
		t.Fatal(err)
	}

	// 2. Record human approval
	if err := RecordApproval(storageDir, mr, sha, patchID, "Approved by Security Lead"); err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, sha, patchID, tier.T1, "")
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}

	if !result.Passed {
		t.Fatalf("CheckVerdicts should pass, got missing reasons: %v", result.MissingReasons)
	}

	if len(result.MatchingVerdicts) != 1 {
		t.Fatalf("MatchingVerdicts len = %d, want 1", len(result.MatchingVerdicts))
	}
	if result.MatchingVerdicts[0].ReviewerModel != "claude-opus-4.8" {
		t.Errorf("ReviewerModel = %q, want claude-opus-4.8", result.MatchingVerdicts[0].ReviewerModel)
	}

	if len(result.MatchingApprovals) != 1 {
		t.Fatalf("MatchingApprovals len = %d, want 1", len(result.MatchingApprovals))
	}
	if result.MatchingApprovals[0].Reason != "Approved by Security Lead" {
		t.Errorf("Reason = %q, want 'Approved by Security Lead'", result.MatchingApprovals[0].Reason)
	}

	evidence := result.FormatEvidence()
	if !strings.Contains(evidence, "[APPROVAL & REVIEW EVIDENCE]") {
		t.Errorf("evidence missing header: %s", evidence)
	}
	if !strings.Contains(evidence, "claude-opus-4.8 [escalated reviewer]") {
		t.Errorf("evidence missing escalated reviewer: %s", evidence)
	}
	if !strings.Contains(evidence, "Approved by Security Lead") {
		t.Errorf("evidence missing human approval reason: %s", evidence)
	}
	if !strings.Contains(evidence, sha) {
		t.Errorf("evidence missing sha: %s", evidence)
	}
}

func TestConcurrentVerdictRecordsAreNotLost(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "verdicts")
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := RecordVerdict(storageDir, 9, "abc123", Approve, fmt.Sprintf("reviewer-%d", i), "worker", ""); err != nil {
				t.Errorf("RecordVerdict %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	records, err := loadVerdictRecords(VerdictPath(storageDir, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != n {
		t.Fatalf("lost verdicts: got %d, want %d", len(records), n)
	}
}

func TestConcurrentApprovalRecordsAreNotLost(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "verdicts")
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := RecordApproval(storageDir, 9, "abc123", "patch", fmt.Sprintf("approval-%d", i)); err != nil {
				t.Errorf("RecordApproval %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	records, err := loadApprovalRecords(ApprovalPath(storageDir, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != n {
		t.Fatalf("lost approvals: got %d, want %d", len(records), n)
	}
}
