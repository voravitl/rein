package run

import (
	"os"
	"testing"
)

var agentMarkers = []struct{ name, kind, session string }{
	{"CLAUDE_CODE_SESSION_ID", "claude-code", "fixture"}, {"CLAUDE_PID", "claude-code", "fixture"}, {"CLAUDECODE", "claude-code", "fixture"},
	{"ZCODE_APP_VERSION", "zcode", ""}, {"ZCODE_PROCESS_LABEL", "zcode", "fixture"},
	{"CODEX_THREAD_ID", "codex", "fixture"}, {"HERMES_SESSION_ID", "hermes", "fixture"},
	{"ANTIGRAVITY_AGENT", "antigravity", ""}, {"ANTIGRAVITY_CONVERSATION_ID", "antigravity", "fixture"}, {"GEMINI_CLI", "antigravity", ""},
	{"OPENCODE", "opencode", "fixture"}, {"OPENCODE_PID", "opencode", "fixture"},
}

func clearAgentEnvironment(t *testing.T) {
	t.Helper()
	for _, marker := range agentMarkers {
		t.Setenv(marker.name, "")
	}
}

func TestAgentSessionEveryMarker(t *testing.T) {
	for _, marker := range agentMarkers {
		t.Run(marker.name, func(t *testing.T) {
			clearAgentEnvironment(t)
			t.Setenv(marker.name, "fixture")
			kind, session := AgentSession()
			if kind != marker.kind || session != marker.session {
				t.Fatalf("%s: wrong kind or provenance", marker.name)
			}
		})
	}
}

func TestAgentSessionPrecedenceAndEmptyValues(t *testing.T) {
	clearAgentEnvironment(t)
	t.Setenv("AGENT", "generic")
	t.Setenv("AI_AGENT", "generic")
	if kind, session := AgentSession(); kind != "" || session != "" {
		t.Fatal("empty or generic markers matched")
	}
	for i := len(agentMarkers) - 1; i >= 0; i-- {
		marker := agentMarkers[i]
		t.Setenv(marker.name, "fixture")
		if kind, _ := AgentSession(); kind != marker.kind {
			t.Fatalf("precedence at %s: got %s, want %s", marker.name, kind, marker.kind)
		}
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session")
	t.Setenv("CLAUDE_PID", "pid")
	if kind, session := AgentSession(); kind != "claude-code" || session != "session" {
		t.Fatal("Claude provenance precedence changed")
	}
	clearAgentEnvironment(t)
	t.Setenv("ZCODE_APP_VERSION", "version")
	t.Setenv("ZCODE_PROCESS_LABEL", "label")
	if kind, session := AgentSession(); kind != "zcode" || session != "label" {
		t.Fatal("ZCode provenance changed")
	}
}

func TestAgentSessionClaudeCompatibility(t *testing.T) {
	clearAgentEnvironment(t)
	t.Setenv("CODEX_THREAD_ID", "fixture")
	if UnderClaude() {
		t.Fatal("legacy Claude predicate matched Codex")
	}
	t.Setenv("CLAUDECODE", "1")
	if !UnderClaude() {
		t.Fatal("legacy Claude predicate missed CLAUDECODE")
	}
}

func TestOwnerOnlyInteractiveStdinRejectsNonTerminals(t *testing.T) {
	old := os.Stdin
	t.Cleanup(func() { os.Stdin = old })
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { null.Close() })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		file *os.File
	}{{"file", file}, {"null", null}, {"pipe", r}, {"closed", closed}, {"nil", nil}} {
		t.Run(test.name, func(t *testing.T) {
			os.Stdin = test.file
			if InteractiveStdin() {
				t.Fatal("nonterminal accepted as interactive stdin")
			}
		})
	}
}
