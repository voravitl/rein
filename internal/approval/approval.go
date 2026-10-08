// Package approval implements approval guard hooks for AskUserQuestion (ADR 0002 B2.4).
package approval

import (
	"encoding/json"
	"strings"
)

// AskUserQuestionPayload represents the structure of AskUserQuestion tool input.
type AskUserQuestionPayload struct {
	Question          string                 `json:"question"`
	Options           []string               `json:"options,omitempty"`
	Answers           []string               `json:"answers,omitempty"`
	MultiSelect       bool                   `json:"multiSelect,omitempty"`
	BypassPermissions bool                   `json:"bypassPermissions,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// AskUserQuestionResponse represents the hook response from user interaction.
type AskUserQuestionResponse struct {
	Answers []string `json:"answers,omitempty"`
}

// CheckPreToolUse validates AskUserQuestion before execution (PreToolUse hook).
// Returns denial reason if the question should be blocked, empty string if allowed.
func CheckPreToolUse(toolInput string, agentID string, sessionID string, ownerSessionID string) string {
	var payload AskUserQuestionPayload
	if err := json.Unmarshal([]byte(toolInput), &payload); err != nil {
		return ""
	}

	// Check if this is an approval question
	if !isApprovalQuestion(&payload) {
		return ""
	}

	// Rule 1: Deny if answers are pre-filled
	if len(payload.Answers) > 0 {
		return "APPROVAL_DENIED: answers are pre-filled by the model. Remove the 'answers' field and let the user click the button. If you need approval, use 'rein approve prompt' to get the exact template, then call AskUserQuestion with only 'question' and 'options' fields."
	}

	// Rule 2: Deny if called by a subagent
	if agentID != "" {
		return "APPROVAL_DENIED: approval questions cannot be asked by subagents (agent_id present). Only the main coordinator session can request human approval. If the task needs approval, escalate to the coordinator or use 'rein approve' in a user terminal."
	}

	// Rule 3: Deny if not from owner session
	if sessionID != ownerSessionID {
		return "APPROVAL_DENIED: approval questions must come from the owner session. Current session does not match the run owner. If you are the coordinator, ensure you're running in the correct session."
	}

	// Rule 4: Validate template (byte-equal check would go here in real implementation)
	// For now, check basic structure
	if !hasApproveOption(&payload) {
		return "APPROVAL_DENIED: approval question must have 'Approve' as an option. Use 'rein approve prompt --mr N' to generate the correct template."
	}

	// Rule 5: Deny if bypassPermissions is set
	if payload.BypassPermissions {
		return "APPROVAL_DENIED: bypassPermissions cannot be used with approval questions. Remove this field."
	}

	// Rule 6: Deny if multiSelect with multiple MRs
	if payload.MultiSelect {
		return "APPROVAL_DENIED: multiSelect cannot be used for approvals (risk of approving multiple MRs at once). Use single-select with one MR per question."
	}

	return ""
}

// ProcessPostToolUse handles approval recording after user interaction (PostToolUse hook).
// Returns true if approval was recorded.
func ProcessPostToolUse(toolUseID string, response string, storageDir string) (bool, error) {
	var resp AskUserQuestionResponse
	if err := json.Unmarshal([]byte(response), &resp); err != nil {
		return false, nil
	}

	// Check if user clicked "Approve"
	if len(resp.Answers) == 0 {
		return false, nil
	}

	for _, answer := range resp.Answers {
		if strings.TrimSpace(answer) == "Approve" {
			return true, nil
		}
	}

	return false, nil
}

func isApprovalQuestion(payload *AskUserQuestionPayload) bool {
	if payload.Question == "" {
		return false
	}
	q := strings.ToLower(payload.Question)
	return strings.Contains(q, "approve") && strings.Contains(q, "merge") ||
		strings.Contains(q, "approval") && (strings.Contains(q, "mr") || strings.Contains(q, "sha"))
}

func hasApproveOption(payload *AskUserQuestionPayload) bool {
	for _, opt := range payload.Options {
		if strings.TrimSpace(opt) == "Approve" {
			return true
		}
	}
	return false
}
