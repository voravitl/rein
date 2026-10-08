// Package guard is the Claude Code hook for pipeline workers. It is active only inside a git worktree whose
// directory name has a task contract whose worktree path is that same directory; everywhere else it returns
// without output, so ordinary sessions are unaffected.
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
)

type event struct {
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	Cwd            string          `json:"cwd"`
	ToolInput      json.RawMessage `json:"tool_input"`
	StopHookActive bool            `json:"stop_hook_active"`
}

var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// toplevel walks up from dir to the first directory that holds .git (a dir, or a file for linked worktrees).
func toplevel(dir string) string {
	d := contract.Real(dir)
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func logDenial(name, reason string) {
	dir := os.Getenv("PIPELINE_LOGDIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache", "worktree-pipeline", "logs")
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "guard.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s DENY %s\n", time.Now().Format("2006-01-02T15:04:05"), name, strings.ReplaceAll(reason, "\n", " "))
}

func emit(w io.Writer, ev, name, reason string) {
	logDenial(name, reason)
	var out any
	if ev == "Stop" {
		out = map[string]string{"decision": "block", "reason": reason}
	} else {
		out = map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": "deny",
			"permissionDecisionReason": "[rein] " + reason}}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// Run reads one hook event from r and writes a decision to w (or nothing). It never returns an error to the
// caller: a hook that crashes would be worse than a hook that stays silent outside workers.
func Run(r io.Reader, w io.Writer) {
	var ev event
	if err := json.NewDecoder(r).Decode(&ev); err != nil {
		return
	}
	cwd := ev.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	top := toplevel(cwd)
	if top == "" {
		return
	}
	name := filepath.Base(top)
	c, err := contract.Load(name)
	if errors.Is(err, contract.ErrNone) {
		return // not a pipeline worker
	}
	if err != nil { // a worker whose contract is broken: fail closed
		emit(w, ev.HookEventName, name, fmt.Sprintf("task contract unreadable (%v); ask the coordinator", err))
		return
	}
	if contract.Real(c.Worktree) != top {
		return // same directory name, different tree
	}
	if reason := decide(ev, c, top, cwd); reason != "" {
		emit(w, ev.HookEventName, c.Name, reason)
	}
}

func decide(ev event, c *contract.Contract, top, cwd string) string {
	switch ev.HookEventName {
	case "Stop":
		if ev.StopHookActive {
			return "" // second stop: let it end; the coordinator's drift check still runs
		}
		if fi, err := os.Stat(c.ReportPath); err != nil || fi.Size() == 0 {
			return fmt.Sprintf("write your report to %s (one heading per scope id %s with status, gate numbers, red checks), then send worker_done",
				c.ReportPath, strings.Join(c.Scope, ", "))
		}
	case "PreToolUse":
		if ev.ToolName == "Bash" {
			var in struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(ev.ToolInput, &in)
			return CheckBash(in.Command, c, top, cwd)
		}
		if editTools[ev.ToolName] {
			var in struct {
				FilePath     string `json:"file_path"`
				NotebookPath string `json:"notebook_path"`
			}
			_ = json.Unmarshal(ev.ToolInput, &in)
			fp := in.FilePath
			if fp == "" {
				fp = in.NotebookPath
			}
			if fp == "" {
				return ""
			}
			return checkEdit(fp, c, top, cwd)
		}
	}
	return ""
}

func checkEdit(fp string, c *contract.Contract, top, cwd string) string {
	x := &ctx{c: c, top: top, cwd: cwd}
	p := x.abs(fp)
	if !inside(p, top) {
		return outsideWrite(p, fp, c, top)
	}
	rel := filepath.ToSlash(mustRel(top, p))
	if glob.Match(rel, c.Deny) {
		return fmt.Sprintf("%s is on the never-edit list of task %s", rel, c.Name)
	}
	if !glob.Match(rel, c.Allow) {
		return fmt.Sprintf("%s is outside the ownership of task %s (%s); if the task really needs it, ask the coordinator with the preamble's ask command",
			rel, c.Name, strings.Join(c.Allow, ", "))
	}
	return ""
}
