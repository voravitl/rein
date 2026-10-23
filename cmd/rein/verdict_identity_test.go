package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
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
	if err := verdict.RecordApproval(storageDir, 77, bSHA, patchB, "approved B"); err != nil {
		t.Fatal(err)
	}

	_, code := capture(t, func() int {
		return cmdVerdictCheck([]string{"--mr", "77", "--sha", sSHA, "--repo", root, "--tier", "T1"})
	})
	if code != 1 {
		t.Errorf("check for S must not accept a verdict recorded for B (exit %d; a HEAD-derived identity would bind B's patch to S)", code)
	}
}

func TestMutableRefsAreFrozenAndCannotApproveNextCommit(t *testing.T) {
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
