package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

// Only the exact report is implicitly writable; siblings need explicit ownership.
func TestReportDirectoryDoesNotGrantSiblingOwnership(t *testing.T) {
	wt, c := setup(t)
	report := filepath.Join(wt, "report", "task.md")
	c.ReportPath = report
	if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if out := bashCase(t, wt, "echo notes > "+report); out != "" {
		t.Fatal(out)
	}
	sibling := filepath.Join(wt, "report", "notes.md")
	if out := bashCase(t, wt, "echo notes > "+sibling); !strings.Contains(out, `"deny"`) {
		t.Errorf("sibling allowed: %s", out)
	}
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "cwd": wt, "tool_input": map[string]string{"file_path": sibling}})
	var out bytes.Buffer
	Run(bytes.NewReader(in), &out)
	if !strings.Contains(out.String(), `"deny"`) {
		t.Errorf("sibling edit allowed: %s", out.String())
	}
}

// The report area must not weaken the never-edit list, the guard's own files, or the coordinator's run dir.
func TestReportAreaDoesNotWeakenProtection(t *testing.T) {
	wt, c := setup(t)
	report := filepath.Join(wt, "report", "task.md")
	c.ReportPath = report
	c.Deny = append(c.Deny, "report/VERSION")
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(wt, "report", "VERSION")
	if out := bashCase(t, wt, "echo x > "+denied); !strings.Contains(out, `"deny"`) {
		t.Errorf("a denied path in the report area was allowed: %q", out)
	}
	hook := filepath.Join(wt, ".opencode", "plugin", "rein.js")
	if out := bashCase(t, wt, "echo x > "+hook); !strings.Contains(out, `"deny"`) {
		t.Errorf("a guard file in the report area was allowed: %q", out)
	}
	runFile := filepath.Join(filepath.Dir(filepath.Dir(c.RunCopy)), "other.md")
	if out := bashCase(t, wt, "echo x > "+runFile); !strings.Contains(out, `"deny"`) {
		t.Errorf("a run-dir write was allowed: %q", out)
	}
}

// A relative --writable path belongs to the task's worktree, exactly like a relative report path.
func TestRelativeWritablePathBecomesWorktreePath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "idx"))
	c, err := contract.New("t", 0, filepath.Join(root, "run"), "src/**", "", "S1", filepath.Join(root, "wts"),
		"report/task-1.md", "notes/scratch.md", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(c.Worktree, "notes/scratch.md")
	if len(c.WorkerWritable) != 1 || c.WorkerWritable[0] != want {
		t.Fatalf("WorkerWritable = %v, want [%s]", c.WorkerWritable, want)
	}
	loaded, err := contract.Load("t")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.WorkerWritable) != 1 || loaded.WorkerWritable[0] != want {
		t.Fatalf("loaded WorkerWritable = %v, want [%s]", loaded.WorkerWritable, want)
	}
	if !loaded.Writable(want) {
		t.Error("the relative writable path is not writable")
	}
}

func TestReviewIPCPlusUnreadShellMustRemainSandboxed(t *testing.T) {
	wt, c := setup(t)
	for _, cmd := range []string{
		"orca orchestration send --from term_1; sh /tmp/worker.sh",
		"orca orchestration send --from term_1; bash /tmp/worker.sh",
		`env FOO=1 bash /tmp/worker.sh; orca orchestration check`,
		`timeout 5 sh /tmp/worker.sh; orca orchestration check`,
		`bash -lc 'orca orchestration send --from term_1'`,
		`bash -ic 'orca orchestration send --from term_1'`,
		`zsh -c 'orca orchestration send --from term_1'`,
		`sudo -i bash -c 'orca orchestration send --from term_1'`,
		`BASH_ENV=/tmp/worker.sh bash -c 'orca orchestration send --from term_1'`,
		`export BASH_ENV=/tmp/worker.sh; bash -c 'orca orchestration send --from term_1'`,
		`env BASH_ENV=/tmp/worker.sh bash -c 'orca orchestration send --from term_1'`,
		"bash --rcfile /tmp/worker.sh -ic 'orca orchestration send --from term_1'",
	} {
		reason, bypass := checkBash(cmd, c, wt, wt)
		if reason == "" && bypass {
			t.Errorf("unread script incorrectly given sandbox bypass: %s", cmd)
		}
	}
}

func TestReviewExecStartupWrappersRemainSandboxed(t *testing.T) {
	wt, c := setup(t)
	for _, cmd := range []string{
		`exec -l bash -c 'orca orchestration send --from term_1'`,
		`exec -a -bash bash -c 'orca orchestration send --from term_1'`,
		`exec -a=-bash bash -c 'orca orchestration send --from term_1'`,
		`exec orca orchestration send --from term_1`,
	} {
		if reason, bypass := checkBash(cmd, c, wt, wt); reason == "" && bypass {
			t.Errorf("exec startup wrapper received sandbox bypass: %s", cmd)
		}
	}
}

func TestReviewInheritedShellStartupEnvRemainsSandboxed(t *testing.T) {
	wt, c := setup(t)
	for _, key := range []string{"BASH_ENV", "ENV"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "/tmp/worker.sh")
			if reason, bypass := checkBash(`bash -c 'orca orchestration send --from term_1'`, c, wt, wt); reason == "" && bypass {
				t.Fatal("inherited startup env received sandbox bypass")
			}
			if reason, bypass := checkBash(`orca orchestration send --from term_1`, c, wt, wt); reason != "" || !bypass {
				t.Fatalf("direct IPC changed: reason=%q bypass=%v", reason, bypass)
			}
		})
	}
}

func TestReviewPathQualifiedOrcaImpostorsStaySandboxed(t *testing.T) {
	wt, c := setup(t)
	for _, exe := range []string{"./orca", "src/orca", "/tmp/x/orca", "orca-dev", "orca-ide"} {
		cmd := exe + " orchestration send --from term_1"
		if reason, bypass := checkBash(cmd, c, wt, wt); reason == "" && bypass {
			t.Errorf("impostor received bypass: %s", cmd)
		}
	}
}

func TestReviewReportBesideCodeDoesNotOwnCode(t *testing.T) {
	wt, c := setup(t)
	c.ReportPath = filepath.Join(wt, "backend", "REPORT.md")
	if c.Writable(filepath.Join(wt, "backend", "Auth", "Login.cs")) {
		t.Fatal("report parent grants unrelated code")
	}
}
