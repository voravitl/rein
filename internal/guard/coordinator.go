package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
	"github.com/voravitl/rein/internal/run"
)

// The coordinator guard (ADR 0002 B0). While a run is active (`rein run start`), the OWNER session's hook judges
// the coordinator itself in every worktree of the repo that has no task contract: it may read, orchestrate through
// Orca and write docs, but it may not edit code, call tools outside its allowlist, authorize itself or start
// writing subagents. Contracted worker worktrees keep their own rule (workerRoot in hook.go runs first).
//
// Like the worker guard this is a seatbelt: interpreter writes (python -c, node -e) and scripts it runs are not
// seen; `rein run audit` is the backstop for what lands on main.

const coordHow = "the coordinator does not edit code or implement through subagents: write the task spec, then " +
	"`rein contract new` -> `orca worktree create` -> `rein hooks install` -> `orca orchestration worker-start --worktree path:<worktree>`"

// coordinate judges one Claude Code event from a directory that is not a worker. It stays silent (0, no output)
// unless the directory is in a repo with a run marker AND the event comes from that run's live owner session.
func coordinate(vendor string, a Action, cwd string, w, stderr io.Writer) int {
	if vendor != "claude" {
		return 0
	}
	switch a.Hook {
	case "PreToolUse", "SessionStart", "SubagentStart":
	default:
		return 0
	}
	loc, found := findRun(a, cwd)
	if !found {
		return 0
	}
	m, err := run.Load(loc.Marker)
	switch {
	case errors.Is(err, run.ErrNoRun):
		return 0
	case err != nil: // corrupt or unknown schema: nobody can tell whose run it is, so every call in the repo is denied
		if a.Hook == "PreToolUse" {
			return emit(vendor, w, stderr, a.Event, "run", err.Error())
		}
		return 0
	}
	if !loc.Belongs(m) {
		return 0
	}
	if a.Hook == "SessionStart" {
		return coordSessionStart(a, loc, m, w)
	}
	if a.SessionID == "" || a.SessionID != m.SessionID || !m.OwnerAlive() {
		return 0 // another session, or a dead owner: the hook stays silent
	}
	if a.Hook == "SubagentStart" {
		if a.AgentID != "" {
			_, _, _ = run.BindStart(loc.Marker, a.AgentID) // cannot block; an unbound subagent is judged by the coordinator rules
		}
		return 0
	}
	if loc.EnvRedirect {
		return emit(vendor, w, stderr, a.Event, "run", "GIT_DIR / GIT_COMMON_DIR is set while run "+m.Run+" is active: the guard cannot tell which repository a command touches; unset them")
	}
	if reason := coordDecide(a, cwd, loc, m); reason != "" {
		return emit(vendor, w, stderr, a.Event, "run:"+m.Run, reason)
	}
	return 0
}

// findRun looks for a run marker from the event's directory, and for a write tool also from the file's directory,
// so a coordinator whose cwd is outside the repo cannot edit the repo's code by absolute path.
func findRun(a Action, cwd string) (run.Loc, bool) {
	if cwd != "" {
		if loc, ok := run.Find(cwd); ok {
			return loc, true
		}
	}
	if editTools[a.Tool] {
		for _, p := range a.Paths {
			if !filepath.IsAbs(p) {
				p = filepath.Join(cwd, p)
			}
			if loc, ok := run.Find(filepath.Dir(p)); ok {
				return loc, true
			}
		}
	}
	return run.Loc{}, false
}

func coordDecide(a Action, cwd string, loc run.Loc, m *run.Marker) string {
	if a.AgentID != "" { // a subagent bound to an allowed task is judged by that task's contract
		if task, ok := run.BoundTask(loc.Marker, a.AgentID); ok && task != "" {
			return boundDecide(a, task, cwd)
		}
	}
	p := &coordPolicy{m: m, loc: loc}
	switch {
	case a.Tool == "Bash":
		return p.bash(a.Command, cwd)
	case editTools[a.Tool]:
		if len(a.Paths) == 0 {
			return fmt.Sprintf("cannot tell which file %s writes; use a tool call that names the file", a.Tool)
		}
		for _, f := range a.Paths {
			if !filepath.IsAbs(f) {
				f = filepath.Join(cwd, f)
			}
			if r := p.judgePath(f, f); r != "" {
				return r
			}
		}
		return ""
	case a.Tool == "Agent" || a.Tool == "Task":
		return p.agent(a)
	case glob.Match(a.Tool, m.CoordinatorTools):
		return ""
	}
	return fmt.Sprintf("tool %s is not on the coordinator allowlist (%s); MCP tools are denied by default. The user can add it to coordinator_tools in the profile and start a new run",
		a.Tool, strings.Join(m.CoordinatorTools, ", "))
}

// boundDecide judges a subagent that the user allowed for a task with that task's worker contract.
func boundDecide(a Action, task, cwd string) string {
	c, err := contract.Load(task)
	if err != nil {
		return fmt.Sprintf("the subagent is bound to task %s but its contract cannot be loaded (%v); run `rein contract new` first", task, err)
	}
	return decide(a, c, contract.Real(c.Worktree), cwd)
}

var taskRx = regexp.MustCompile(`rein-task:[ \t]*([A-Za-z0-9][A-Za-z0-9._-]*)`)

// agent judges an Agent call and records the pending binding SubagentStart will pair with.
func (p *coordPolicy) agent(a Action) string {
	task := ""
	switch {
	case a.SubType != "" && contains(p.m.ReadonlyAgents, a.SubType):
	default:
		if mt := taskRx.FindStringSubmatch(a.Prompt); mt != nil && p.m.Allows("task", mt[1]) {
			task = mt[1]
		} else {
			return fmt.Sprintf("the Agent tool is not how the coordinator works (subagent %q): %s. A subagent for a task needs the user to run "+
				"`rein run allow --task <t> --reason <text>` in their own terminal and `rein-task: <t>` in the prompt; read-only agent types are listed in readonly_agents",
				a.SubType, coordHow)
		}
	}
	if err := run.RecordPending(p.loc.Marker, p.m.SessionID, task); err != nil {
		if errors.Is(err, run.ErrAmbiguous) {
			return "another subagent is still starting and its task binding would be ambiguous; wait until it has started, then retry"
		}
		return fmt.Sprintf("cannot record the subagent binding (%v)", err)
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// coordPolicy is the coordinator's write policy for one event.
type coordPolicy struct {
	m     *run.Marker
	loc   run.Loc
	trees []string
	got   bool
}

// allTrees lists every worktree of the repo: the main checkout, the one the event runs in and the linked ones.
func (p *coordPolicy) allTrees() []string {
	if p.got {
		return p.trees
	}
	p.got = true
	seen := map[string]bool{}
	add := func(d string) {
		if d = contract.Real(d); d != "" && !seen[d] {
			seen[d] = true
			p.trees = append(p.trees, d)
		}
	}
	add(p.m.Root)
	add(p.loc.Top)
	wts := filepath.Join(p.loc.Common, "worktrees")
	ents, _ := os.ReadDir(wts)
	for _, e := range ents {
		if b, err := os.ReadFile(filepath.Join(wts, e.Name(), "gitdir")); err == nil {
			if gd := strings.TrimSpace(string(b)); gd != "" {
				add(filepath.Dir(gd)) // gitdir names <worktree>/.git
			}
		}
	}
	return p.trees
}

// treeOf is the deepest worktree of the repo that holds abs ("" when abs is outside the repo).
func (p *coordPolicy) treeOf(abs string) string {
	best := ""
	for _, t := range p.allTrees() {
		if inside(abs, t) && len(t) > len(best) {
			best = t
		}
	}
	return best
}

// contracted reports whether a worktree is a worker's (a contract names it).
func (p *coordPolicy) contracted(tree string) bool {
	c, err := contract.Load(filepath.Base(tree))
	if err != nil {
		return !errors.Is(err, contract.ErrNone) // a broken contract still marks a worker
	}
	return contract.Real(c.Worktree) == tree
}

// judgePath judges one write target (absolute). shown is how the caller named it.
func (p *coordPolicy) judgePath(abs, shown string) string {
	abs = contract.Real(abs)
	if inside(abs, contract.Real(p.loc.Common)) {
		return fmt.Sprintf("%s is inside the repository's git directory (the run marker lives there); the coordinator never writes it", shown)
	}
	if md := contract.Real(filepath.Dir(p.loc.Marker)); inside(abs, md) {
		if first, _, _ := strings.Cut(filepath.ToSlash(mustRel(md, abs)), "/"); strings.HasPrefix(first, "rein-run") {
			return fmt.Sprintf("%s is run state; the coordinator never writes it", shown)
		}
	}
	tree := p.treeOf(abs)
	if tree == "" {
		return "" // outside the repo: the run dir, temp files, notes
	}
	rel := filepath.ToSlash(mustRel(tree, abs))
	switch {
	case rel == ".":
		return fmt.Sprintf("%s is a worktree of the repo itself", shown)
	case rel == ".git" || strings.HasPrefix(rel, ".git/"):
		return fmt.Sprintf("%s is a worktree's .git; the coordinator never writes it", shown)
	case p.contracted(tree):
		return fmt.Sprintf("%s is inside worker worktree %s; the worker owns it", shown, filepath.Base(tree))
	case !glob.Match(rel, p.m.CoordinatorWritable):
		return fmt.Sprintf("%s is not on coordinator_writable (%s) while run %s is active; %s",
			rel, strings.Join(p.m.CoordinatorWritable, ", "), p.m.Run, coordHow)
	}
	return ""
}

// coordSessionStart rebinds the run to a new session id of the same live Claude process (/clear, compact, resume)
// and tells a session in a repo with a dead owner's run that the run is pending.
func coordSessionStart(a Action, loc run.Loc, m *run.Marker, w io.Writer) int {
	var msg string
	switch {
	case !m.OwnerAlive():
		msg = fmt.Sprintf("[rein] run %q (started %s) is pending in %s: its coordinator process %d is gone, so the coordinator guard is silent. "+
			"Close it with `rein run end %s` (the user can force it with --abandon) before starting a new run.", m.Run, m.StartedAt, m.Root, m.PID, m.Root)
	case a.SessionID != "" && a.SessionID != m.SessionID && (a.Source == "clear" || a.Source == "compact" || a.Source == "resume") && sameProcess(m):
		if _, err := run.Resume(loc.Top, a.SessionID, m.PID); err != nil {
			msg = fmt.Sprintf("[rein] run %q could not follow this session (%v); run `rein run resume %s`", m.Run, err, m.Root)
		} else {
			msg = fmt.Sprintf("[rein] run %q is active: the coordinator guard now follows this session.", m.Run)
		}
	}
	if msg != "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "SessionStart", "additionalContext": msg}})
	}
	return 0
}

// sameProcess reports whether the hook runs on behalf of the marker's Claude process (CLAUDE_PID, or the hook's
// parent when Claude runs the hook directly).
func sameProcess(m *run.Marker) bool {
	if p, err := strconv.Atoi(os.Getenv("CLAUDE_PID")); err == nil && p == m.PID {
		return true
	}
	return os.Getppid() == m.PID
}
