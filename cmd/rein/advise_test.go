package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCmdAdvise_ArgValidation(t *testing.T) {
	// Insufficient positional args
	if code := cmdAdvise([]string{}); code != 2 {
		t.Fatalf("expected code 2 on empty args, got %d", code)
	}
	if code := cmdAdvise([]string{"role", "dir", "task"}); code != 2 {
		t.Fatalf("expected code 2 on 3 positional args, got %d", code)
	}
	// Invalid flag
	if code := cmdAdvise([]string{"-invalid-flag"}); code != 2 {
		t.Fatalf("expected code 2 on invalid flag, got %d", code)
	}
}

func TestFindAdviseScript(t *testing.T) {
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts", "advise.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)
	found := findAdviseScript()
	if found != scriptPath {
		t.Fatalf("expected %s, got %s", scriptPath, found)
	}
}

func TestCmdAdvise_MockExecution(t *testing.T) {
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts", "advise.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Mock script records its arguments and env vars to a file
	logFile := filepath.Join(tmp, "args.log")
	scriptContent := `#!/bin/sh
echo "args: $@" > "` + logFile + `"
echo "provider: $ADVISE_PROVIDER" >> "` + logFile + `"
echo "timeout: $ADVISE_TIMEOUT" >> "` + logFile + `"
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)

	code := cmdAdvise([]string{
		"--provider", "kiro",
		"--model", "test-model",
		"--timeout", "10s",
		"critic", tmp, "task.md", "out.md",
	})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	str := string(logged)
	expectedArgs := "args: critic " + tmp + " task.md out.md test-model\n"
	if !containsStr(str, expectedArgs) {
		t.Errorf("expected %q in output, got %q", expectedArgs, str)
	}
	if !containsStr(str, "provider: kiro\n") {
		t.Errorf("expected provider kiro in output, got %q", str)
	}
	if !containsStr(str, "timeout: 10\n") {
		t.Errorf("expected timeout 10 in output, got %q", str)
	}
}

func containsStr(s, sub string) bool {
	return filepath.Clean(s) != "" && (s == sub || len(s) >= len(sub) && (s[:len(sub)] == sub || containsSubstr(s, sub)))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
