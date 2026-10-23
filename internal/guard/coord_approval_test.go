package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/verdict"
)

// approvalSHA is a full commit id: the canonical prompt shows its 8-character prefix while the metadata carries
// the whole value (cmd/rein/verdict.go, `approve prompt`).
const approvalSHA = "141b55db07abb71795bc22d7d661e9dbc0219cfd"

// approvalInput is what `rein approve prompt --mr N` prints; prefill adds the answers field a model sets when it
// answers the question itself instead of leaving the click to the user.
func approvalInput(mr int, prefill bool) map[string]any {
	p := map[string]any{
		"question": fmt.Sprintf("Approve merge for MR %d?\n\nSHA: %s\nTier: T1\n\nVerdicts: No verdicts recorded yet\n\nThis approval gates the merge. Review the verdicts and changes before approving.", mr, approvalSHA[:8]),
		"options":  []string{"Approve"},
		"metadata": map[string]any{"mr": mr, "sha": approvalSHA, "tier": "T1"},
	}
	if prefill {
		p["answers"] = []string{"Approve"}
	}
	return p
}

// runHook drives the production hook the way `rein hook` is driven: one JSON event on stdin, the decision on
// stdout, the exit code cmdHook returns.
func runHook(t *testing.T, ev map[string]any) (string, int) {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := RunTask("claude", "", bytes.NewReader(b), &out, &errb)
	return out.String() + errb.String(), code
}

func approvalEvent(event, cwd string, toolInput map[string]any, extra ...map[string]any) map[string]any {
	ev := map[string]any{"hook_event_name": event, "tool_name": "AskUserQuestion", "cwd": cwd,
		"session_id": coordSession, "tool_input": toolInput}
	for _, x := range extra {
		for k, v := range x {
			ev[k] = v
		}
	}
	return ev
}

// F-03: the run owner's approval prompt must go through the same prompt-integrity rules as a worker's, not just
// the coordinator allowlist.
func TestCoordApprovalPromptIsValidated(t *testing.T) {
	e := newCoordEnv(t)
	mr := 42
	for _, c := range []struct {
		name string
		ev   map[string]any
	}{
		{"canonical prompt with the forbidden prefilled answer", approvalEvent("PreToolUse", e.root, approvalInput(mr, true))},
		{"approval prompt asked by a subagent", approvalEvent("PreToolUse", e.root, approvalInput(mr, false), map[string]any{"agent_id": "ag-1"})},
	} {
		out, code := runHook(t, c.ev)
		if !isDeny(out) || !strings.Contains(out, "APPROVAL_DENIED") {
			t.Errorf("%s: want an APPROVAL_DENIED decision, got code %d output %q", c.name, code, out)
		}
	}
	// Even a well-formed owner prompt requires the terminal fallback.
	out, code := runHook(t, approvalEvent("PreToolUse", e.root, approvalInput(mr, false)))
	if !isDeny(out) {
		t.Errorf("owner prompt requires terminal fallback, got code %d output %q", code, out)
	}
}

// An unpaired answer must leave no approval; the decision trail remains advisory.
func TestCoordUnpairedApprovalResponseIsNotRecorded(t *testing.T) {
	e := newCoordEnv(t)
	e.m.RunDir = filepath.Join(e.outside, "run")
	e.save()
	mr := 42
	path := verdict.ApprovalPath(verdict.StorageDir("", e.root), mr)

	// an answer that is not the fixed label is not an approval
	out, code := runHook(t, approvalEvent("PostToolUse", e.root, approvalInput(mr, false),
		map[string]any{"tool_response": map[string]any{"answers": []string{"Decline"}}}))
	if code != 0 || out != "" {
		t.Fatalf("PostToolUse must stay silent, got code %d output %q", code, out)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("a declined question must not record an approval (%s)", path)
	}

	out, code = runHook(t, approvalEvent("PostToolUse", e.root, approvalInput(mr, false),
		map[string]any{"tool_response": map[string]any{"answers": []string{"Approve"}}}))
	if code != 0 || out != "" {
		t.Fatalf("PostToolUse must stay silent, got code %d output %q", code, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unpaired PostToolUse must not create approval: %v", err)
	}
	// the run's decision trail is still appended (ADR 0002 B4.1)
	if b, err := os.ReadFile(filepath.Join(e.m.RunDir, "decisions.tsv")); err != nil || !strings.Contains(string(b), "Approve merge for MR 42") {
		t.Errorf("decisions.tsv missing the question: %v %q", err, b)
	}
}

// Workers also cannot turn unpaired tool responses into merge authority.
func TestWorkerUnpairedApprovalIsNotRecorded(t *testing.T) {
	e := newCoordEnv(t)
	wt := filepath.Join(contract.Real(filepath.Dir(e.root)), "wts", "task-a")
	linkWorktree(t, e.root, wt, "task-a")
	writeFile(t, filepath.Join(wt, "src", "w.go"), "x\n")
	if _, err := contract.New("task-a", 1, filepath.Join(e.outside, "run"), "src/**", "", "S1", filepath.Dir(wt), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	mr := 9

	out, _ := runHook(t, approvalEvent("PreToolUse", wt, approvalInput(mr, true)))
	if !isDeny(out) || !strings.Contains(out, "APPROVAL_DENIED") {
		t.Errorf("worker prefilled prompt must stay denied, got %q", out)
	}
	out, code := runHook(t, approvalEvent("PreToolUse", wt, approvalInput(mr, false)))
	if !isDeny(out) {
		t.Errorf("worker prompt requires terminal fallback, got code %d output %q", code, out)
	}
	out, code = runHook(t, approvalEvent("PostToolUse", wt, approvalInput(mr, false),
		map[string]any{"tool_response": map[string]any{"answers": []string{"Approve"}}}))
	if code != 0 || out != "" {
		t.Fatalf("worker PostToolUse must stay silent, got code %d output %q", code, out)
	}
	path := verdict.ApprovalPath(verdict.StorageDir("", wt), mr)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worker must not create unpaired approval at %s: %v", path, err)
	}
}
