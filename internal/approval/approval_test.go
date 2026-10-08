package approval

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGoldenCase_A_RealClick tests case (a): real click -> should pass and record approval
func TestGoldenCase_A_RealClick(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Approve merge for MR 42?\n\nSHA: abc123\nTier: T1\n\nReview the changes before approving.",
		Options:  []string{"Approve"},
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason != "" {
		t.Errorf("Real click should pass PreToolUse, got denial: %s", reason)
	}

	// Simulate PostToolUse with Approve answer
	response := AskUserQuestionResponse{
		Answers: []string{"Approve"},
	}
	respJSON, _ := json.Marshal(response)

	recorded, err := ProcessPostToolUse("tool-1", string(respJSON), "/tmp/verdicts")
	if err != nil {
		t.Errorf("ProcessPostToolUse failed: %v", err)
	}
	if !recorded {
		t.Error("Real approve click should trigger recording")
	}
}

// TestGoldenCase_B_PrefilledAnswers tests case (b): answers pre-filled by model -> denied
func TestGoldenCase_B_PrefilledAnswers(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Approve merge for MR 42?",
		Options:  []string{"Approve"},
		Answers:  []string{"Approve"},
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason == "" {
		t.Error("Pre-filled answers should be denied")
	}
	if !strings.Contains(reason, "pre-filled") {
		t.Errorf("Denial should mention pre-filled, got: %s", reason)
	}
	if !strings.Contains(reason, "Remove the 'answers' field") {
		t.Errorf("Denial should explain the fix, got: %s", reason)
	}
}

// TestGoldenCase_C_DeclineOrOther tests case (c): decline or other answer -> no approval
func TestGoldenCase_C_DeclineOrOther(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Approve merge for MR 42?",
		Options:  []string{"Approve", "Decline"},
	}
	input, _ := json.Marshal(payload)

	// Should pass PreToolUse
	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason != "" {
		t.Errorf("Valid question should pass, got: %s", reason)
	}

	// User chose "Decline"
	response := AskUserQuestionResponse{
		Answers: []string{"Decline"},
	}
	respJSON, _ := json.Marshal(response)

	recorded, _ := ProcessPostToolUse("tool-1", string(respJSON), "/tmp/verdicts")
	if recorded {
		t.Error("Decline should not trigger approval recording")
	}
}

// TestGoldenCase_D_MultiSelectAcrossMRs tests case (d): multiSelect across MRs -> denied
func TestGoldenCase_D_MultiSelectAcrossMRs(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question:    "Approve these merges: MR 42, MR 43?",
		Options:     []string{"Approve"},
		MultiSelect: true,
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason == "" {
		t.Error("MultiSelect should be denied for approvals")
	}
	if !strings.Contains(reason, "multiSelect") {
		t.Errorf("Denial should mention multiSelect, got: %s", reason)
	}
	if !strings.Contains(reason, "one MR per question") {
		t.Errorf("Denial should explain the fix, got: %s", reason)
	}
}

// TestGoldenCase_E_BypassPermissions tests case (e): bypassPermissions -> denied
func TestGoldenCase_E_BypassPermissions(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question:          "Approve merge for MR 42?",
		Options:           []string{"Approve"},
		BypassPermissions: true,
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason == "" {
		t.Error("bypassPermissions should be denied")
	}
	if !strings.Contains(reason, "bypassPermissions") {
		t.Errorf("Denial should mention bypassPermissions, got: %s", reason)
	}
	if !strings.Contains(reason, "Remove this field") {
		t.Errorf("Denial should explain the fix, got: %s", reason)
	}
}

// TestGoldenCase_F_SubagentAsking tests case (f): subagent asking -> denied
func TestGoldenCase_F_SubagentAsking(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Approve merge for MR 42?",
		Options:  []string{"Approve"},
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "subagent-456", "session-123", "session-123")
	if reason == "" {
		t.Error("Subagent asking should be denied")
	}
	if !strings.Contains(reason, "subagent") {
		t.Errorf("Denial should mention subagent, got: %s", reason)
	}
	if !strings.Contains(reason, "main coordinator session") {
		t.Errorf("Denial should explain who can ask, got: %s", reason)
	}
}

// TestNonOwnerSession tests approval from non-owner session -> denied
func TestNonOwnerSession(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Approve merge for MR 42?",
		Options:  []string{"Approve"},
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-456", "session-123")
	if reason == "" {
		t.Error("Non-owner session should be denied")
	}
	if !strings.Contains(reason, "owner session") {
		t.Errorf("Denial should mention owner session, got: %s", reason)
	}
}

// TestMissingApproveOption tests question without "Approve" option -> denied
func TestMissingApproveOption(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Approve merge for MR 42?",
		Options:  []string{"Yes", "No"},
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason == "" {
		t.Error("Missing 'Approve' option should be denied")
	}
	if !strings.Contains(reason, "'Approve' as an option") {
		t.Errorf("Denial should mention the missing option, got: %s", reason)
	}
	if !strings.Contains(reason, "rein approve prompt") {
		t.Errorf("Denial should point to the template command, got: %s", reason)
	}
}

// TestNonApprovalQuestion tests that non-approval questions pass through
func TestNonApprovalQuestion(t *testing.T) {
	payload := AskUserQuestionPayload{
		Question: "Which file should I edit?",
		Options:  []string{"file1.go", "file2.go"},
	}
	input, _ := json.Marshal(payload)

	reason := CheckPreToolUse(string(input), "", "session-123", "session-123")
	if reason != "" {
		t.Errorf("Non-approval question should pass, got denial: %s", reason)
	}
}

// TestAllDenialsHaveClearReasonsAndNextSteps ensures every denial is actionable
func TestAllDenialsHaveClearReasonsAndNextSteps(t *testing.T) {
	tests := []struct {
		name    string
		payload AskUserQuestionPayload
		agentID string
		session string
		owner   string
	}{
		{"pre-filled", AskUserQuestionPayload{Question: "Approve merge for MR 42?", Options: []string{"Approve"}, Answers: []string{"Approve"}}, "", "s1", "s1"},
		{"subagent", AskUserQuestionPayload{Question: "Approve merge for MR 42?", Options: []string{"Approve"}}, "agent-1", "s1", "s1"},
		{"non-owner", AskUserQuestionPayload{Question: "Approve merge for MR 42?", Options: []string{"Approve"}}, "", "s2", "s1"},
		{"no-approve-option", AskUserQuestionPayload{Question: "Approve merge for MR 42?", Options: []string{"Yes"}}, "", "s1", "s1"},
		{"bypass", AskUserQuestionPayload{Question: "Approve merge for MR 42?", Options: []string{"Approve"}, BypassPermissions: true}, "", "s1", "s1"},
		{"multiselect", AskUserQuestionPayload{Question: "Approve merge for MR 42?", Options: []string{"Approve"}, MultiSelect: true}, "", "s1", "s1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := json.Marshal(tt.payload)
			reason := CheckPreToolUse(string(input), tt.agentID, tt.session, tt.owner)
			if reason == "" {
				t.Errorf("%s: expected denial, got none", tt.name)
				return
			}

			if !strings.Contains(reason, "APPROVAL_DENIED") {
				t.Errorf("%s: denial should start with APPROVAL_DENIED, got: %s", tt.name, reason)
			}

			lower := strings.ToLower(reason)
			hasProblem := strings.Contains(lower, "pre-filled") ||
				strings.Contains(lower, "subagent") ||
				strings.Contains(lower, "owner") ||
				strings.Contains(lower, "option") ||
				strings.Contains(lower, "bypass") ||
				strings.Contains(lower, "multiselect")

			if !hasProblem {
				t.Errorf("%s: denial should clearly state the problem, got: %s", tt.name, reason)
			}

			hasNextStep := strings.Contains(lower, "remove") ||
				strings.Contains(lower, "use 'rein") ||
				strings.Contains(lower, "use single-select") ||
				strings.Contains(lower, "escalate") ||
				strings.Contains(lower, "ensure")

			if !hasNextStep {
				t.Errorf("%s: denial should state the next step, got: %s", tt.name, reason)
			}
		})
	}
}
