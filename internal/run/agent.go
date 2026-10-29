package run

import "os"

// AgentSession identifies a known AI session. Order is stable when markers overlap.
// Values are returned only for approval provenance; refusal messages use kind alone.
func AgentSession() (kind, sessionID string) {
	groups := []struct {
		kind, session string
		markers       []string
	}{
		{"claude-code", "", []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "CLAUDECODE"}},
		{"zcode", "ZCODE_PROCESS_LABEL", []string{"ZCODE_APP_VERSION", "ZCODE_PROCESS_LABEL"}},
		{"codex", "CODEX_THREAD_ID", []string{"CODEX_THREAD_ID"}},
		{"hermes", "HERMES_SESSION_ID", []string{"HERMES_SESSION_ID"}},
		{"antigravity", "ANTIGRAVITY_CONVERSATION_ID", []string{"ANTIGRAVITY_AGENT", "ANTIGRAVITY_CONVERSATION_ID", "GEMINI_CLI"}},
		{"opencode", "", []string{"OPENCODE", "OPENCODE_PID"}},
	}
	for _, group := range groups {
		for _, marker := range group.markers {
			if value := os.Getenv(marker); value != "" {
				if group.session != "" {
					return group.kind, os.Getenv(group.session)
				}
				return group.kind, value
			}
		}
	}
	return "", ""
}
