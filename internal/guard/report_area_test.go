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

// A sibling of the report in the report's own directory is the worker's own file: the guard orders the worker to
// write its report there, so the whole directory stays writable without the coordinator having to list it in --allow.
func TestReportDirectoryIsWritable(t *testing.T) {
	wt, c := setup(t)
	report := filepath.Join(wt, "report", "task.md")
	c.ReportPath = report
	if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(wt, "report", "notes.md")
	if out := bashCase(t, wt, "echo notes > "+sibling); out != "" {
		t.Errorf("a sibling of the report was denied: %s", out)
	}
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "cwd": wt,
		"tool_input": map[string]string{"file_path": sibling}})
	var out bytes.Buffer
	Run(bytes.NewReader(in), &out)
	if out.String() != "" {
		t.Errorf("editing a sibling of the report was denied: %s", out.String())
	}
	// The report directory is the worker's own, but the rest of the worktree is not.
	if out := bashCase(t, wt, "echo x > "+filepath.Join(wt, "docs", "design", "other.md")); !strings.Contains(out, `"deny"`) {
		t.Errorf("a file outside the report directory was allowed: %q", out)
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
