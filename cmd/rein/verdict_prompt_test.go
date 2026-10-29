package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/verdict"
)

func TestApprovePromptTerminalContext(t *testing.T) {
	clearAgentMarkers(t)
	for _, mode := range []string{"absolute run", "relative run", "repo storage"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "repo ' $(touch injected) `touch injected` ; & space")
			gitIn(t, base, "init", "-q", "-b", "main", root)
			gitIn(t, root, "commit", "--allow-empty", "-qm", "fixture")
			gitIn(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			sha, err := verdict.ResolveCommit(root, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(base)
			repoArg, err := filepath.Rel(base, root)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--repo", repoArg, "--mr", "42", "--title", "fixture"}
			runDir := ""
			if mode != "repo storage" {
				runDir = filepath.Join(base, "run ' $(touch injected) `touch injected` ; & space")
				runArg := runDir
				if mode == "relative run" {
					runArg, err = filepath.Rel(base, runDir)
					if err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, "--run-dir", runArg)
			}
			storage := verdict.StorageDir(runDir, root)
			if err := verdict.RecordVerdict(storage, 42, sha, verdict.Approve, "fixture-reviewer", "fixture-worker", root); err != nil {
				t.Fatal(err)
			}
			out, code := capture(t, func() int { return cmdApprovePrompt(args) })
			if code != 0 {
				t.Fatalf("preview exit %d: %s", code, out)
			}
			var payload struct {
				Command  string `json:"terminal_command"`
				Question string `json:"question"`
			}
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload.Question, "1 verdict(s)") {
				t.Fatalf("preview did not read selected storage: %s", out)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			other := t.TempDir()
			gitIn(t, other, "init", "-q", "-b", "main")
			// Execute the generated shell command against the real CLI handler in a disposable fixture.
			cmd := exec.Command("sh", "-c", `rein() { "$REIN_PROMPT_TEST_BINARY" -test.run='^TestApprovePromptTerminalHelper$' -- "$@"; }; `+payload.Command)
			cmd.Dir = other
			cmd.Env = append(os.Environ(), "REIN_PROMPT_TEST_BINARY="+binary, "CLAUDE_PID=", "CLAUDE_CODE_SESSION_ID=", "ZCODE_APP_VERSION=", "ZCODE_PROCESS_LABEL=", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("terminal command failed: %v\n%s\n%s", err, payload.Command, out)
			}
			data, err := os.ReadFile(verdict.ApprovalPath(storage, 42))
			if err != nil || !strings.Contains(string(data), sha) {
				t.Fatalf("approval missing from preview storage: %s %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(other, ".rein")); !os.IsNotExist(err) {
				t.Fatalf("terminal command wrote to unrelated repository: %v", err)
			}
			for _, dir := range []string{base, other} {
				if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
					t.Fatalf("shell expanded path contents in %s: %v", dir, err)
				}
			}
		})
	}
}

func TestApprovePromptTerminalHelper(t *testing.T) {
	if os.Getenv("REIN_PROMPT_TEST_BINARY") == "" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) && os.Args[i+1] == "approve" {
			os.Exit(cmdApprove(os.Args[i+2:]))
		}
	}
	t.Fatal("missing approval arguments")
}
