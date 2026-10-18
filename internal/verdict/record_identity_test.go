package verdict

import (
	"path/filepath"
	"testing"

	"github.com/voravitl/rein/internal/tier"
)

// TestRecordVerdict_BindsToRecordedSHAWhenHeadMoves covers the record-side half
// of F-01: a verdict written for one SHA while HEAD points at another must store
// the recorded revision's identity, not HEAD's. Otherwise a check of HEAD's
// patch accepts the foreign verdict through patch-ID equality.
func TestRecordVerdict_BindsToRecordedSHAWhenHeadMoves(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "verdicts")
	repoDir := initVerdictFeatureRepo(t)
	mr := 105

	// B: the revision being reviewed.
	writeFixtureFile(t, filepath.Join(repoDir, "b.txt"), "feature\n")
	gitRun(t, repoDir, "add", "b.txt")
	gitRun(t, repoDir, "commit", "-m", "feature B")
	bSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	bPatchID, err := ComputeRevisionID(repoDir, "origin/main", bSHA)
	if err != nil {
		t.Fatalf("ComputeRevisionID for B failed: %v", err)
	}

	// C: the worktree moves on before the verdict for B is recorded.
	writeFixtureFile(t, filepath.Join(repoDir, "c.txt"), "later\n")
	gitRun(t, repoDir, "add", "c.txt")
	gitRun(t, repoDir, "commit", "-m", "feature C")
	cSHA := gitRun(t, repoDir, "rev-parse", "HEAD")
	cPatchID, err := ComputeRevisionID(repoDir, "origin/main", cSHA)
	if err != nil {
		t.Fatalf("ComputeRevisionID for C failed: %v", err)
	}
	if bPatchID == cPatchID {
		t.Fatal("fixture error: B and C must have different patch IDs")
	}

	if err := RecordVerdict(storageDir, mr, bSHA, Approve, "claude-opus-4.8", "gpt-5.6", repoDir); err != nil {
		t.Fatal(err)
	}
	records, err := loadVerdictRecords(VerdictPath(storageDir, mr))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].PatchID != bPatchID {
		t.Fatalf("recorded patch IDs = %+v, want the recorded SHA's identity %s (HEAD is %s)", records, bPatchID, cPatchID)
	}
	if err := RecordApproval(storageDir, mr, bSHA, bPatchID, "approved B"); err != nil {
		t.Fatal(err)
	}

	result, err := CheckVerdicts(storageDir, mr, cSHA, cPatchID, tier.T1, repoDir)
	if err != nil {
		t.Fatalf("CheckVerdicts failed: %v", err)
	}
	if result.Passed || result.MakerCount != 0 || result.HasApproval {
		t.Errorf("a verdict for B must not bind to C (passed=%v makers=%d approval=%v)", result.Passed, result.MakerCount, result.HasApproval)
	}
}
