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

func TestCmdVerdictTemplate(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	code := cmdVerdictTemplate([]string{
		"--mr", "42",
		"--sha", "abcdef123456",
		"--base", "main-sha-000",
		"--title", "feat: auth provider",
		"--reviewer", "codex:gpt-5",
		"--worker", "claude:sonnet-4.5",
		"--tier", "T2",
		"--round", "1",
	})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}

	requiredParts := []string{
		"# Review Report: MR !42 (Round 1)",
		"MR / PR**: !42 - feat: auth provider",
		"Base SHA**: `main-sha-000`",
		"Head SHA**: `abcdef123456`",
		"Reviewer**: codex:gpt-5",
		"Worker**: claude:sonnet-4.5",
		"Task Tier**: T2",
		"Acceptance Criteria Checklist",
		"Verification & Test Execution",
		"Findings & Defects",
		"Handoff & Remediation Plan",
		"Verdict",
		"rein verdict record --mr 42 --sha abcdef123456 --verdict <APPROVE|REQUEST_CHANGES> --reviewer codex:gpt-5 --worker claude:sonnet-4.5",
	}

	for _, req := range requiredParts {
		if !strings.Contains(output, req) {
			t.Errorf("missing expected string %q in template output:\n%s", req, output)
		}
	}
}
