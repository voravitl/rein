package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/ledger"
)

// runAdviseWithRole runs the REAL advise.sh against fakes, with the given
// OMC role prompt body and version, and returns the arguments every `rein`
// invocation received. The selected role can come from the pipeline or OMC.
func runAdviseWithRole(t *testing.T, role string, roleBody, omcVersion string, pipeline bool) (string, int) {
	t.Helper()
	tmp := t.TempDir()
	rolesDir := filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if pipeline {
		if err := os.WriteFile(filepath.Join(rolesDir, role+".md"), []byte(roleBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "skills", "worktree-pipeline", "templates", "advisor-rules.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(tmp, "omc-agents")
	packageDir := filepath.Join(agents, "oh-my-claude-sisyphus")
	if err := os.MkdirAll(filepath.Join(packageDir, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "agents", role+".md"), []byte(roleBody), 0644); err != nil {
		t.Fatal(err)
	}
	if omcVersion != "" {
		pkg, _ := json.Marshal(map[string]string{"version": omcVersion})
		if err := os.WriteFile(filepath.Join(packageDir, "package.json"), pkg, 0644); err != nil {
			t.Fatal(err)
		}
	}

	calls := filepath.Join(tmp, "calls.log")
	writeFake := func(name, code string) {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("#!/bin/sh\n"+code+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFake("rein", `echo "rein $*" >> "`+calls+`"; exit 0`)
	writeFake("omc", `echo wrong-global-version`)
	writeFake("codex", `echo provider-called >> "`+calls+`"
out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
[ -n "$out" ] && printf 'Answer\n' > "$out"
echo "tokens used"; echo "7"`)
	writeFake("timeout", `shift; exec "$@"`)

	// Run the REAL script, with `npm root -g` pinned to the fixture dir.
	real, err := os.ReadFile(filepath.Join(realRepoRoot(t), "skills", "worktree-pipeline", "scripts", "advise.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(real)
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	}
	script := filepath.Join(tmp, "skills", "worktree-pipeline", "scripts", "advise.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/bash\nnpm() { echo "+agents+"; }\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	task := filepath.Join(tmp, "task.md")
	if err := os.WriteFile(task, []byte("Review this diff.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "answer.md")

	t.Setenv("CLAUDE_PLUGIN_ROOT", tmp)
	t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))
	t.Setenv("REIN", filepath.Join(tmp, "rein"))
	t.Setenv("ADVISE_NO_PACK", "1")
	t.Setenv("ADVISE_RUN", "run-r")
	t.Setenv("ADVISE_TASK", "contract-task")
	t.Setenv("ADVISE_PROVIDER", "codex")
	t.Setenv("ADVISE_MODEL", "gpt-test")
	t.Setenv("PIPELINE_LOGDIR", filepath.Join(tmp, "logs"))

	code := cmdAdvise([]string{
		"--provider", "codex", "--model", "gpt-test",
		"--run", "run-r", "--task", "contract-task",
		role, tmp, task, out,
	})
	b, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b), code
}

func TestAdvisorLedgerCallPinsRole(t *testing.T) {
	for _, pipeline := range []bool{false, true} {
		t.Run(fmt.Sprint(pipeline), func(t *testing.T) {
			body := "---\nname: code-reviewer\n---\nReview the diff.\n"
			recorded, code := runAdviseWithRole(t, "code-reviewer", body, "5.6.2", pipeline)
			if code != 0 {
				t.Fatalf("exit=%d calls=%s", code, recorded)
			}
			source, version := "omc", "5.6.2"
			if pipeline {
				source, version = "pipeline", "unknown"
			}
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("Review the diff.")))
			for _, want := range []string{"rein ledger call", "--role code-reviewer", "--provider codex", "--model gpt-test", "--tokens 7", "--run run-r", "--task contract-task", "--role-source " + source, "--omc-version " + version, "--role-digest " + digest} {
				if !strings.Contains(recorded, want) {
					t.Errorf("missing %q: %s", want, recorded)
				}
			}
			if strings.Contains(recorded, "wrong-global-version") {
				t.Fatal("used unrelated global omc")
			}
			edited, code := runAdviseWithRole(t, "code-reviewer", "Different prompt.\n", "5.6.2", pipeline)
			editedDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("Different prompt.")))
			if code != 0 || !strings.Contains(edited, "--role-digest "+editedDigest) || strings.Contains(edited, "--role-digest "+digest) {
				t.Fatalf("changed prompt digest incorrect: %s", edited)
			}
		})
	}
}

func TestAdvisorOMCMissingVersionFailsBeforeProvider(t *testing.T) {
	for _, version := range []string{"", " ", "5.6.2\nother"} {
		recorded, code := runAdviseWithRole(t, "code-reviewer", "Review this.\n", version, false)
		if code != 2 || recorded != "" {
			t.Fatalf("version=%q exit=%d calls=%s", version, code, recorded)
		}
	}
	// A pipeline role has no OMC installation prerequisite.
	recorded, code := runAdviseWithRole(t, "code-reviewer", "Review this.\n", "", true)
	if code != 0 || !strings.Contains(recorded, "--role-source pipeline --omc-version unknown") {
		t.Fatalf("exit=%d calls=%s", code, recorded)
	}
}

func TestLedgerCallPersistsRolePin(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("prompt")))
	args := []string{"call", "--role", "critic", "--model", "m", "--role-source", "omc", "--omc-version", "5.6.2", "--role-digest", digest}
	if code := cmdLedger(args); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	rows, err := ledger.LoadStrict("")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if row := rows[0]; row.RoleSource != "omc" || row.OMCVersion != "5.6.2" || row.RoleDigest != digest {
		t.Fatalf("metadata=%+v", row)
	}
	if code := cmdLedger([]string{"call", "--role", "critic", "--model", "legacy"}); code != 0 {
		t.Fatalf("legacy call exit=%d", code)
	}
}
