package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/verdict"
)

func TestCmdVerdictCheck_EvidenceOutput(t *testing.T) {
	root := contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	// Create branch feature
	gitIn(t, root, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "feature")

	// Get SHA
	out, err := execOutput(root, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))

	storageDir := filepath.Join(root, ".rein", "verdicts")

	// Record an escalated verdict
	if err := verdict.RecordVerdict(storageDir, 10, sha, verdict.Approve, "claude-opus-4.8", "claude-sonnet-4.5", root); err != nil {
		t.Fatal(err)
	}

	// Record approval
	if err := verdict.RecordApproval(storageDir, 10, sha, "", "Human approved in review"); err != nil {
		t.Fatal(err)
	}

	// Intercept stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	code := cmdVerdictCheck([]string{"--mr", "10", "--sha", sha, "--repo", root, "--tier", "T1"})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if code != 0 {
		t.Fatalf("cmdVerdictCheck exited %d, output:\n%s", code, output)
	}

	if !strings.Contains(output, "[APPROVAL & REVIEW EVIDENCE]") {
		t.Errorf("output missing evidence header:\n%s", output)
	}
	if !strings.Contains(output, "claude-opus-4.8 [escalated reviewer]") {
		t.Errorf("output missing escalated reviewer:\n%s", output)
	}
	if !strings.Contains(output, "Human approved in review") {
		t.Errorf("output missing approval reason:\n%s", output)
	}
}
