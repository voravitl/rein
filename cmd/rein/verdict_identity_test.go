package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/tier"
	"github.com/voravitl/rein/internal/verdict"
)

// TestCmdVerdictCheck_UsesCheckedSHAForRevisionIdentity covers the CLI half of
// F-01: `verdict check --sha S` must derive the revision identity from S, not
// from whatever commit HEAD points at. A verdict for B (same patch as HEAD's C)
// must not satisfy a check of the unrelated S.
func TestCmdVerdictCheck_UsesCheckedSHAForRevisionIdentity(t *testing.T) {
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	revParse := func(ref string) string {
		t.Helper()
		out, err := execOutput(root, "git", "rev-parse", ref)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	gitIn(t, root, "checkout", "-b", "feature")
	write("b.md", "same\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "feature B")
	bSHA := revParse("HEAD")

	// twin C applies the same patch as B, so both share a patch ID while the
	// commits differ.
	gitIn(t, root, "checkout", "-b", "twin", "main")
	write("b.md", "same\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "twin C")

	gitIn(t, root, "checkout", "-b", "other", "main")
	write("s.md", "different\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "other S")
	sSHA := revParse("HEAD")

	gitIn(t, root, "checkout", "twin")

	patchB, err := verdict.ComputeRevisionID(root, "origin/main", bSHA)
	if err != nil {
		t.Fatal(err)
	}
	patchS, err := verdict.ComputeRevisionID(root, "origin/main", sSHA)
	if err != nil {
		t.Fatal(err)
	}
	patchHead, err := verdict.ComputeRevisionID(root, "origin/main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if patchB != patchHead {
		t.Fatalf("fixture error: twin commits must share a patch ID: %s vs %s", patchB, patchHead)
	}
	if patchB == patchS {
		t.Fatal("fixture error: S must have a different patch ID")
	}

	storageDir := verdict.StorageDir("", root)
	if err := verdict.RecordVerdict(storageDir, 77, bSHA, verdict.Approve, "claude-opus-4.8", "gpt-5.6", root); err != nil {
		t.Fatal(err)
	}
	if err := verdict.RecordApproval(storageDir, 77, bSHA, patchB, "approved B", "", ""); err != nil {
		t.Fatal(err)
	}

	_, code := capture(t, func() int {
		return cmdVerdictCheck([]string{"--mr", "77", "--sha", sSHA, "--repo", root, "--tier", "T1"})
	})
	if code != 1 {
		t.Errorf("check for S must not accept a verdict recorded for B (exit %d; a HEAD-derived identity would bind B's patch to S)", code)
	}
}

// TestGetVerdictSummary_MatchesCheckAfterUnchangedRebase pins the summary to
// the same revision binding `verdict check` uses: a verdict recorded for A must
// still be counted when the head is rebased to B with an unchanged patch body,
// even when the stored and current patch IDs differ (e.g. one computed by git's
// default patch-id mode instead of --verbatim). A SHA-or-patchID matcher would
// miss that record; only the unchanged-patch-body proof binds it.
func TestGetVerdictSummary_MatchesCheckAfterUnchangedRebase(t *testing.T) {
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	base := filepath.Join(root, "base.txt")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(base, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	revParse := func(ref string) string {
		t.Helper()
		out, err := execOutput(root, "git", "rev-parse", ref)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	// stablePatchID computes the revision's patch ID the way `git patch-id`
	// does without --verbatim, i.e. what a record from a different patch-id
	// mode carries for the very same patch.
	stablePatchID := func(sha string) string {
		t.Helper()
		mbOut, err := execOutput(root, "git", "merge-base", "origin/main", sha)
		if err != nil {
			t.Fatal(err)
		}
		mb := strings.TrimSpace(string(mbOut))
		diffOut, err := execOutput(root, "git", "diff",
			"--no-ext-diff", "--no-textconv", "--no-renames", "--no-color", mb+"..."+sha)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "patch-id", "--stable")
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(diffOut)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 || fields[0] == "" {
			t.Fatal("fixture error: no stable patch ID")
		}
		return fields[0]
	}

	write("line1\nline2\nline3\nline4\nline5\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "base")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	gitIn(t, root, "checkout", "-q", "-b", "feature")
	write("line1\nline2\nline3\nline4\nline5 v1\n")
	gitIn(t, root, "commit", "-qam", "feature A")
	aSHA := revParse("HEAD")

	storageDir := verdict.StorageDir("", root)
	if err := verdict.RecordVerdict(storageDir, 78, aSHA, verdict.Approve, "claude-opus-4.8", "gpt-5.6", root); err != nil {
		t.Fatal(err)
	}

	// Rebase A onto an advanced main: B rewrites the commit with an unchanged
	// patch body (the insertion sits outside the change's context window).
	gitIn(t, root, "checkout", "-q", "main")
	write("inserted\nline1\nline2\nline3\nline4\nline5\n")
	gitIn(t, root, "commit", "-qam", "main advances")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, root, "checkout", "-q", "feature")
	gitIn(t, root, "rebase", "main")
	bSHA := revParse("HEAD")

	patchB, err := verdict.ComputeRevisionID(root, "origin/main", bSHA)
	if err != nil || patchB == "" {
		t.Fatalf("ComputeRevisionID for B failed: %v (id %q)", err, patchB)
	}
	if aSHA == bSHA {
		t.Fatal("fixture error: the rebase should rewrite the commit")
	}

	// Rewrite the stored record's patch ID to the stable-mode value: same
	// patch, different ID, so neither the SHA nor the patch ID can bind it.
	storedID := stablePatchID(aSHA)
	if storedID == patchB {
		t.Fatal("fixture error: stored and current patch IDs must differ")
	}
	verdictPath := verdict.VerdictPath(storageDir, 78)
	data, err := os.ReadFile(verdictPath)
	if err != nil {
		t.Fatal(err)
	}
	var records []verdict.VerdictRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	records[0].PatchID = storedID
	data, err = json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verdictPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if !verdict.RevisionIdentitiesMatch(storedID, patchB, root, aSHA, bSHA) {
		t.Fatal("fixture error: the unchanged patch rebase must be accepted by the identity proof")
	}

	summary := getVerdictSummary(storageDir, 78, bSHA, root)
	if !strings.Contains(summary, "APPROVE: anthropic") {
		t.Errorf("summary must count the rebased-but-unchanged approval the way verdict check does, got %q", summary)
	}

	result, err := verdict.CheckVerdicts(storageDir, 78, bSHA, patchB, tier.T1, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MatchingVerdicts) != 1 || result.ReviewerCount != 1 {
		t.Errorf("verdict check matched %d verdict(s) and %d maker(s); want 1 and 1", len(result.MatchingVerdicts), result.ReviewerCount)
	}
}

func TestMutableRefsAreFrozenAndCannotApproveNextCommit(t *testing.T) {
	clearAgentMarkers(t)
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	path := filepath.Join(root, "a.txt")
	os.WriteFile(path, []byte("base\n"), 0600)
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "base")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	os.WriteFile(path, []byte("reviewed\n"), 0600)
	gitIn(t, root, "commit", "-qam", "reviewed")
	sha, err := verdict.ResolveCommit(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	_, code := capture(t, func() int {
		return cmdVerdictRecord([]string{"--repo", root, "--mr", "42", "--sha", "HEAD", "--verdict", "APPROVE", "--reviewer", "claude-opus-4.8", "--worker", "gpt-5.6"})
	})
	if code != 0 {
		t.Fatalf("record exit %d", code)
	}
	// The terminal entry point must freeze explicit refs as well as its HEAD default.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("ZCODE_APP_VERSION", "")
	t.Setenv("ZCODE_PROCESS_LABEL", "")
	_, code = capture(t, func() int { return cmdApprove([]string{"--repo", root, "--mr", "42", "--sha", "HEAD"}) })
	if code != 0 {
		t.Fatalf("approve exit %d", code)
	}
	b, err := os.ReadFile(verdict.ApprovalPath(verdict.StorageDir("", root), 42))
	if err != nil || !strings.Contains(string(b), sha) || strings.Contains(string(b), `"HEAD"`) {
		t.Fatalf("approval not immutable: %s %v", b, err)
	}
	_, code = capture(t, func() int { return cmdVerdictCheck([]string{"--repo", root, "--mr", "42", "--sha", "HEAD"}) })
	if code != 0 {
		t.Fatalf("reviewed HEAD rejected: %d", code)
	}
	os.WriteFile(path, []byte("unreviewed\n"), 0600)
	gitIn(t, root, "commit", "-qam", "next")
	_, code = capture(t, func() int { return cmdVerdictCheck([]string{"--repo", root, "--mr", "42", "--sha", "HEAD"}) })
	if code == 0 {
		t.Fatal("old HEAD authority approved new commit")
	}
	if _, err := verdict.ResolveCommit(root, "--all"); err == nil {
		t.Fatal("option accepted as commit")
	}
	if _, err := verdict.ResolveCommit(root, "does-not-exist"); err == nil {
		t.Fatal("missing ref accepted")
	}
}
