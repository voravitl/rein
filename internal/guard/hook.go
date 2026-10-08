// Package guard is the hook for pipeline workers (Claude Code, codex, agy, kiro, opencode). It is active only inside a git worktree whose
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

	"github.com/voravitl/rein/internal/approval"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/verdict"
)

// workerRoot walks up from dir past every directory that holds .git (a dir, or a file for linked worktrees and
// submodules) and returns the first one that is a contracted worktree: its contract's worktree is that directory.
// Walking past the innermost root matters: a submodule or nested clone inside a worker worktree has its own .git, and
// stopping there would look up a contract by the nested folder's name, find none, and leave the write unguarded.
// found is false when no ancestor is a worker (an ordinary session); err is a contract that exists but cannot be read.
func workerRoot(dir string) (top string, c *contract.Contract, found bool, name string, err error) {
	d := contract.Real(dir)
	for {
		if _, serr := os.Stat(filepath.Join(d, ".git")); serr == nil {
			name = filepath.Base(d)
			c, err = contract.Load(name)
			switch {
			case err == nil:
				if contract.Real(c.Worktree) == d {
					return d, c, true, name, nil
				} // same directory name, different tree: keep walking
			case !errors.Is(err, contract.ErrNone):
				return d, nil, false, name, err // a worker whose contract is broken: the caller fails closed
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", nil, false, "", nil
		}
		d = parent
	}
}

func appendLine(path, line string) {
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func stamp() string { return time.Now().Format("2006-01-02T15:04:05") }

func logDenial(name, reason string) {
	appendLine(filepath.Join(contract.LogDir(), "guard.log"),
		fmt.Sprintf("%s %s DENY %s", stamp(), name, strings.ReplaceAll(reason, "\n", " ")))
}

// logSeen records that a hook call reached the guard inside a contracted worktree (proof the guard is wired for
// that vendor); drift's GUARD_INACTIVE reads it. Tool names are squeezed to one token so a line stays parseable.
func logSeen(vendor string, c *contract.Contract, a Action) {
	tool := strings.Join(strings.Fields(a.Tool), "_")
	if tool == "" {
		tool = "-"
	}
	line := fmt.Sprintf("%s %s %s %s", stamp(), vendor, a.Event, tool)
	if c.HooksGeneration != "" { // ties the line to the install it belongs to (drift ignores older generations)
		line += " gen=" + c.HooksGeneration
	}
	appendLine(contract.SeenPath(c.Name), line)
}

// emit writes the denial in the way the vendor's hook runner understands and returns the process exit code.
func emit(vendor string, w, stderr io.Writer, event, name, reason string) int {
	logDenial(name, reason)
	switch vendor {
	case "kiro", "opencode": // exit 2 + reason on stderr is their block signal
		fmt.Fprintln(stderr, "[rein] "+reason)
		return 2
	case "agy": // always exit 0 with JSON; Stop "continue" re-enters the agent loop with the reason
		out := map[string]string{"decision": "deny", "reason": "[rein] " + reason}
		if event == "stop" {
			out = map[string]string{"decision": "continue", "reason": reason}
		}
		_ = json.NewEncoder(w).Encode(out)
		return 0
	}
	var out any
	if event == "stop" {
		out = map[string]string{"decision": "block", "reason": reason}
	} else {
		out = map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": "deny",
			"permissionDecisionReason": "[rein] " + reason}}
	}
	_ = json.NewEncoder(w).Encode(out)
	return 0
}

// Run is the Claude Code hook: it reads one event from r and writes a decision to w (or nothing).
func Run(r io.Reader, w io.Writer) { RunVendor("claude", r, w, io.Discard) }

// RunVendor is RunTask without a bound task: the worker is found from the event's directory.
func RunVendor(vendor string, r io.Reader, w, stderr io.Writer) int {
	return RunTask(vendor, "", r, w, stderr)
}

// RunTask reads one hook event of the given vendor from r, judges it with the task contract, and writes the
// decision the way that vendor needs. The return value is the process exit code. It never returns an error to the
// caller: a hook that crashes would be worse than a hook that stays silent outside workers.
//
// With task == "" (the global Claude plugin hook) the contract is found from the event's directory and anything
// that is not a worker is ignored. With a task (hooks installed by `rein hooks install` pass --task) the hook IS
// that worker's guard: it judges against that contract's worktree and denies when the contract is missing or
// broken or the event cannot be understood, because silence there would be a bypass.
func RunTask(vendor, task string, r io.Reader, w, stderr io.Writer) int {
	var raw json.RawMessage
	derr := json.NewDecoder(r).Decode(&raw) // the first JSON value, like the original Claude hook
	var a Action
	ok := false
	if derr == nil {
		a, ok = parse(vendor, raw)
	}
	// The coordinator-subagent deny is unconditional (every session, bound or not) and must not need a contract:
	// judge it on the parsed action before any directory walk or contract lookup (ADR 0002 B0).
	if a.DenyCoord {
		return emit(vendor, w, stderr, a.Event, "agent", coordAgentReject)
	}
	if task != "" {
		return runBound(vendor, task, a, ok, w, stderr)
	}
	if !ok {
		return 0
	}
	cwd := a.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	anchor := a.Anchor
	if anchor == "" {
		anchor = cwd
	}
	top, c, found, name, err := workerRoot(anchor)
	if err != nil { // a worker whose contract is broken: fail closed
		return emit(vendor, w, stderr, a.Event, name, fmt.Sprintf("task contract unreadable (%v); ask the coordinator", err))
	}
	if !found {
		return coordinate(vendor, a, cwd, w, stderr) // not a worker: silent unless it is a coordinator session of an active run
	}
	return judge(vendor, a, c, top, cwd, w, stderr)
}

func runBound(vendor, task string, a Action, ok bool, w, stderr io.Writer) int {
	c, err := contract.Load(task)
	if err != nil {
		return emit(vendor, w, stderr, a.Event, task, fmt.Sprintf("the guard for task %s cannot load its contract (%v); ask the coordinator", task, err))
	}
	top := contract.Real(c.Worktree)
	if fi, err := os.Stat(top); err != nil || !fi.IsDir() {
		return emit(vendor, w, stderr, a.Event, task, fmt.Sprintf("worktree %s of task %s is missing; ask the coordinator", c.Worktree, task))
	}
	if !ok || a.Event == "" {
		return emit(vendor, w, stderr, a.Event, task, fmt.Sprintf("the guard for task %s cannot read this %s hook event; ask the coordinator", task, vendor))
	}
	cwd := a.Cwd
	if cwd == "" {
		cwd = top
	}
	return judge(vendor, a, c, top, cwd, w, stderr)
}

func judge(vendor string, a Action, c *contract.Contract, top, cwd string, w, stderr io.Writer) int {
	if a.Event != "" {
		logSeen(vendor, c, a)
	}

	// Handle PostToolUse for approval recording
	if a.Event == "posttool" && vendor == "claude" && a.Tool == "AskUserQuestion" {
		return judgePost(vendor, a, c, top, w, stderr)
	}

	if reason := decide(a, c, top, cwd); reason != "" {
		return emit(vendor, w, stderr, a.Event, c.Name, reason)
	}
	return 0
}

// judgePost handles PostToolUse events for approval recording.
func judgePost(vendor string, a Action, c *contract.Contract, top string, w, stderr io.Writer) int {
	// Get run marker to extract owner session
	loc, found := run.Find(top)
	if !found {
		return 0 // No run context, skip approval recording
	}
	_, err := run.Load(loc.Marker)
	if err != nil {
		return 0 // Cannot load run, skip approval recording
	}

	// Check if approval was recorded
	storageDir := verdict.StorageDir("", top)
	approved, err := approval.ProcessPostToolUse("", a.Response, storageDir)
	if err != nil || !approved {
		return 0 // Not an approval or error, just continue
	}

	// Extract MR and SHA from metadata in tool input
	var payload approval.AskUserQuestionPayload
	if err := json.Unmarshal([]byte(a.ToolInput), &payload); err != nil {
		return 0
	}

	mr, _ := payload.Metadata["mr"].(float64)
	sha, _ := payload.Metadata["sha"].(string)

	if mr > 0 && sha != "" {
		// Compute patch ID
		patchID, _ := verdict.ComputeRevisionID(top, "origin/main", "HEAD")

		// Record approval
		_ = verdict.RecordApproval(storageDir, int(mr), sha, patchID, "Human approval via AskUserQuestion")
	}

	return 0
}

func decide(a Action, c *contract.Contract, top, cwd string) string {
	switch a.Event {
	case "stop":
		if a.StopActive {
			return "" // second stop: let it end; the coordinator's drift check still runs
		}
		if fi, err := os.Stat(c.ReportPath); err != nil || fi.Size() == 0 {
			return fmt.Sprintf("write your report to %s (one heading per scope id %s with status, gate numbers, red checks), then send worker_done",
				c.ReportPath, strings.Join(c.Scope, ", "))
		}
	case "posttool":
		// PostToolUse is logged but not denied (approval recording happens in judgePost)
		return ""
	case "pretool":
		// Check AskUserQuestion approval guard (ADR B2.4)
		if a.Kind == "ask" && a.Tool == "AskUserQuestion" && a.ToolInput != "" {
			// Get owner session from run marker
			ownerSessionID := ""
			if loc, found := run.Find(top); found {
				if m, err := run.Load(loc.Marker); err == nil {
					ownerSessionID = m.SessionID
				}
			}

			// Check approval rules
			if reason := approval.CheckPreToolUse(a.ToolInput, a.AgentID, a.SessionID, ownerSessionID); reason != "" {
				return reason
			}
		}
		switch a.Kind {
		case "bash":
			if strings.Contains(a.Command, patchMarker) && invokesApplyPatch.MatchString(a.Command) { // apply_patch run through the shell writes files too
				paths := patchPaths(a.Command)
				if len(paths) == 0 {
					return "a patch inside a shell command names no file the guard can read; use the apply_patch tool"
				}
				for _, p := range paths {
					if reason := checkEdit(p, c, top, cwd); reason != "" {
						return reason
					}
				}
			}
			return CheckBash(a.Command, c, top, cwd)
		case "write":
			if len(a.Paths) == 0 { // a known write tool whose target we cannot read must not slip through
				return fmt.Sprintf("cannot tell which file %s writes; use a tool call that names the file", a.Tool)
			}
			for _, p := range a.Paths {
				if reason := checkEdit(p, c, top, cwd); reason != "" {
					return reason
				}
			}
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
	if protectedHook(top, rel) {
		return fmt.Sprintf("%s belongs to the rein guard installed for task %s; workers never change it", rel, c.Name)
	}
	if !glob.Match(rel, c.Allow) {
		return fmt.Sprintf("%s is outside the ownership of task %s (%s); if the task really needs it, ask the coordinator with the preamble's ask command",
			rel, c.Name, strings.Join(c.Allow, ", "))
	}
	return ""
}
