package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/verdict"
)

func approveTestRepo(t *testing.T) (root, sha string) {
	t.Helper()
	clearAgentMarkers(t)
	root = contract.Real(t.TempDir())
	gitIn(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	out, err := execOutput(root, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(out))
}

func approvalsFile(root string, mr int) string {
	return filepath.Join(root, ".rein", "verdicts", "mr-"+strconv.Itoa(mr)+"-approvals.json")
}

func TestApproveRefusedInsideClaudeCodeWithoutAsDirected(t *testing.T) {
	root, sha := approveTestRepo(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-refuse")
	t.Setenv("CLAUDE_PID", "")

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := cmdApprove([]string{"--mr", "30", "--sha", sha, "--repo", root})

	w.Close()
	os.Stderr = oldStderr
	var stderr bytes.Buffer
	_, _ = io.Copy(&stderr, r)

	if code != 1 {
		t.Fatalf("cmdApprove inside Claude Code without --as-directed = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--as-directed") {
		t.Errorf("refusal must mention --as-directed, got: %s", stderr.String())
	}
	if _, err := os.Stat(approvalsFile(root, 30)); !os.IsNotExist(err) {
		t.Error("no approval record may be written without --as-directed")
	}
}

func TestApproveAsDirectedRecordsProvenance(t *testing.T) {
	root, sha := approveTestRepo(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-42")
	t.Setenv("CLAUDE_PID", "")

	code := cmdApprove([]string{"--mr", "31", "--sha", sha, "--repo", root, "--as-directed", "--reason", "owner approved in chat"})
	if code != 0 {
		t.Fatalf("cmdApprove --as-directed = %d, want 0", code)
	}

	data, err := os.ReadFile(approvalsFile(root, 31))
	if err != nil {
		t.Fatalf("approval record missing: %v", err)
	}
	var records []verdict.ApprovalRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("approval record corrupt: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	if records[0].Via != "claude-code" {
		t.Errorf("Via = %q, want claude-code", records[0].Via)
	}
	if records[0].SessionID != "session-42" {
		t.Errorf("SessionID = %q, want session-42", records[0].SessionID)
	}
}

func TestApproveOutsideSessionHasNoProvenance(t *testing.T) {
	root, sha := approveTestRepo(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("ZCODE_APP_VERSION", "")
	t.Setenv("ZCODE_PROCESS_LABEL", "")

	code := cmdApprove([]string{"--mr", "32", "--sha", sha, "--repo", root, "--reason", "typed in terminal"})
	if code != 0 {
		t.Fatalf("cmdApprove outside an AI session = %d, want 0", code)
	}

	data, err := os.ReadFile(approvalsFile(root, 32))
	if err != nil {
		t.Fatalf("approval record missing: %v", err)
	}
	var records []verdict.ApprovalRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("approval record corrupt: %v", err)
	}
	if len(records) != 1 || records[0].Via != "" || records[0].SessionID != "" {
		t.Fatalf("direct terminal approval must carry no provenance, got %+v", records[0])
	}
}

func TestApproveAsDirectedRecordsZcodeProvenance(t *testing.T) {
	root, sha := approveTestRepo(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("ZCODE_APP_VERSION", "3.16.3")
	t.Setenv("ZCODE_PROCESS_LABEL", "local-1")

	code := cmdApprove([]string{"--mr", "33", "--sha", sha, "--repo", root, "--as-directed", "--reason", "owner approved in a zcode chat"})
	if code != 0 {
		t.Fatalf("cmdApprove --as-directed in a zcode session = %d, want 0", code)
	}

	data, err := os.ReadFile(approvalsFile(root, 33))
	if err != nil {
		t.Fatalf("approval record missing: %v", err)
	}
	var records []verdict.ApprovalRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("approval record corrupt: %v", err)
	}
	if len(records) != 1 || records[0].Via != "zcode" || records[0].SessionID != "local-1" {
		t.Fatalf("zcode provenance = %+v, want via zcode session local-1", records[0])
	}
}
