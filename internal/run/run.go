// Package run is the run marker of the coordinator guard (ADR 0002 B0). `rein run start` writes one marker under the
// repository's git common dir; while its owner session is alive the hook denies the coordinator's own writes, tools
// and subagents outside the allowlists the marker carries. The hook finds the marker WITHOUT exec'ing git (Find), the
// CLI may exec git (Start, End, Audit).
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
)

// Schema is the marker's version. A hook that reads another version denies: it cannot know what the fields mean.
const Schema = 1

// MarkerFile is the marker's file name under the git common dir (or under $REIN_RUN_DIR).
const MarkerFile = "rein-run.json"

// DefaultWritable is what the coordinator may write in any worktree of the repo when the profile names nothing:
// docs, markdown, the plugin manifest (version bumps) and the changelog.
var DefaultWritable = []string{"docs/**", "**/*.md", ".claude-plugin/plugin.json", "CHANGELOG*"}

// DefaultTools are the tools besides Bash, the write tools and Agent (which have their own rules) that a coordinator
// may call: read, search, list and ask tools, Orca, and Claude Code's task/skill bookkeeping. MCP tools are not on
// the list, so they are denied until a profile names them.
var DefaultTools = []string{"View", "ViewFile", "Read", "ReadFile", "Glob", "Grep", "Search", "List",
	"AskUserQuestion", "ask_question", "orca", "orchestration",
	"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskOutput", "BashOutput", "Skill", "ToolSearch",
	"WebFetch", "WebSearch"}

// DefaultReadonlyAgents lists standard read-only subagents permitted without rein run allow:
// research, exploration, critique, review, and test verification roles.
var DefaultReadonlyAgents = []string{
	"Explore", "explore",
	"Critic", "critic",
	"Research", "research",
	"code-reviewer", "test-engineer",
}

// Allowance is something the USER recorded with `rein run allow`.
type Allowance struct {
	Kind   string `json:"kind"` // "task" (an Agent call may name rein-task: <Ref>) | "commit" (audit accepts commit Ref)
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// Marker is the run marker.
type Marker struct {
	Schema              int         `json:"schema"`
	Run                 string      `json:"run"`
	StartedAt           string      `json:"started_at"`
	Root                string      `json:"root"`              // main checkout root (not derivable from the common dir with separate-git-dir)
	RunDir              string      `json:"run_dir,omitempty"` // run directory for decisions.tsv and standing.md (ADR 0002 B4)
	StartSHA            string      `json:"start_sha"`         // HEAD of the main checkout at start: audit looks at start..BaseRef
	BaseRef             string      `json:"base_ref"`          // branch the main checkout was on ("HEAD" when detached)
	SessionID           string      `json:"session_id"`        // owner session; rebound by Resume
	PID                 int         `json:"pid"`               // owner process (the Claude process)
	StartTime           int64       `json:"start_time"`        // opaque, per platform: tells a reused pid from the owner
	BootID              string      `json:"boot_id,omitempty"` // extra guard on Linux
	CoordinatorWritable []string    `json:"coordinator_writable"`
	CoordinatorTools    []string    `json:"coordinator_tools"`
	ReadonlyAgents      []string    `json:"readonly_agents"`
	Allowed             []Allowance `json:"allowed,omitempty"`
}

// ErrNoRun means there is no marker.
var ErrNoRun = errors.New("no active run")

// ErrActive means a marker already exists.
type ErrActive struct {
	Run   string
	Alive bool
}

func (e *ErrActive) Error() string {
	if e.Alive {
		return fmt.Sprintf("run %q is already active; end it first with `rein run end`", e.Run)
	}
	return fmt.Sprintf("run %q is pending (its owner process is gone); close it with `rein run end` (the user can force it with --abandon)", e.Run)
}

// BadMarker is a marker the hook cannot trust: corrupt JSON, unknown schema, missing fields.
type BadMarker struct {
	Path   string
	Reason string
}

func (e *BadMarker) Error() string {
	return fmt.Sprintf("run marker %s is unusable (%s); the user clears it with `rein run end --abandon --reason <text>` or by deleting the file", e.Path, e.Reason)
}

// MarkerPath is where the marker of a repo with this git common dir lives. $REIN_RUN_DIR redirects it (tests).
func MarkerPath(common string) string {
	if d := os.Getenv("REIN_RUN_DIR"); d != "" {
		return filepath.Join(d, MarkerFile)
	}
	return filepath.Join(common, MarkerFile)
}

func redirected() bool { return os.Getenv("REIN_RUN_DIR") != "" }

// Loc is a repository found from a directory.
type Loc struct {
	Top         string // the directory that holds .git (the worktree root)
	Common      string // git common dir
	Marker      string // marker path
	Linked      bool   // Top is a linked worktree
	EnvRedirect bool   // GIT_DIR / GIT_COMMON_DIR is set: lookup from the directory cannot be trusted
}

func real(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// commonOf reads a gitdir the way git does, without running it. A gitdir with a commondir file is a linked
// worktree only when it sits at <common>/worktrees/<name>; a gitdir without one is its own common dir (a main
// checkout with a separate git dir, or a submodule, which has no marker and is walked past).
func commonOf(gitdir string) (common string, linked, ok bool) {
	gitdir = filepath.Clean(gitdir)
	b, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return gitdir, false, true
		}
		return "", false, false
	}
	common = strings.TrimSpace(string(b))
	if common == "" {
		return "", false, false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	common = filepath.Clean(common)
	wt := filepath.Dir(gitdir)
	if filepath.Base(wt) != "worktrees" || real(filepath.Dir(wt)) != real(common) {
		return "", false, false
	}
	return common, true, true
}

// repoAt reports the git common dir of dir when dir holds a .git (directory, or file with a gitdir: line).
func repoAt(dir string) (common string, linked, ok bool) {
	g := filepath.Join(dir, ".git")
	fi, err := os.Stat(g)
	if err != nil {
		return "", false, false
	}
	if fi.IsDir() {
		return g, false, true
	}
	b, err := os.ReadFile(g)
	if err != nil || len(b) > 4096 {
		return "", false, false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !found {
		return "", false, false
	}
	gd := strings.TrimSpace(rest)
	if gd == "" {
		return "", false, false
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	return commonOf(gd)
}

func gitEnvSet() bool { return os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_COMMON_DIR") != "" }

// Locate finds the repository that holds dir (the nearest .git going up), ignoring GIT_DIR. It is what the CLI uses.
func Locate(dir string) (Loc, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return Loc{}, err
	}
	for {
		if common, linked, ok := repoAt(d); ok {
			return Loc{Top: d, Common: common, Marker: MarkerPath(common), Linked: linked}, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return Loc{}, fmt.Errorf("%s is not inside a git repository", dir)
		}
		d = parent
	}
}

// Find looks for a run marker from dir upwards without exec'ing git: at every directory with a .git it computes the
// common dir and stats the marker, so a submodule or nested clone inside a repo with a run still finds the run.
// ok is false when there is no marker (the hook then stays silent).
func Find(dir string) (Loc, bool) {
	d := filepath.Clean(dir)
	if !filepath.IsAbs(d) {
		return Loc{}, false
	}
	env := gitEnvSet()
	for {
		if common, linked, ok := repoAt(d); ok {
			mp := MarkerPath(common)
			if _, err := os.Stat(mp); err == nil {
				return Loc{Top: d, Common: common, Marker: mp, Linked: linked, EnvRedirect: env}, true
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if env { // GIT_DIR / GIT_COMMON_DIR may point at a repo with a run even when the directory is elsewhere
		for _, v := range []string{"GIT_COMMON_DIR", "GIT_DIR"} {
			p := os.Getenv(v)
			if p == "" {
				continue
			}
			common := filepath.Clean(p)
			if v == "GIT_DIR" {
				c, _, ok := commonOf(p)
				if !ok {
					continue
				}
				common = c
			}
			if mp := MarkerPath(common); fileExists(mp) {
				return Loc{Top: dir, Common: common, Marker: mp, EnvRedirect: true}, true
			}
		}
	}
	return Loc{}, false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Belongs reports whether the marker is the run of the repository loc was found in. It is always true unless
// $REIN_RUN_DIR redirects every repo to one marker, where the marker's own root must be the same repository.
func (l Loc) Belongs(m *Marker) bool {
	if !redirected() {
		return true
	}
	c, _, ok := repoAt(m.Root)
	return ok && real(c) == real(l.Common)
}

// Load reads and validates a marker file whole. A missing file is ErrNoRun; anything else wrong is *BadMarker.
func Load(path string) (*Marker, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoRun
	}
	if err != nil {
		return nil, &BadMarker{Path: path, Reason: err.Error()}
	}
	var v struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, &BadMarker{Path: path, Reason: "not valid JSON: " + err.Error()}
	}
	if v.Schema != Schema {
		return nil, &BadMarker{Path: path, Reason: fmt.Sprintf("schema %d, this rein reads schema %d", v.Schema, Schema)}
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, &BadMarker{Path: path, Reason: err.Error()}
	}
	if m.Run == "" || m.Root == "" || m.SessionID == "" || m.PID <= 0 {
		return nil, &BadMarker{Path: path, Reason: "run, root, session_id and pid are required"}
	}
	return &m, nil
}

// writeMarker writes the marker through a temp file in the same directory. exclusive makes it fail with
// *ErrActive-style fs.ErrExist when a marker exists already (a hard link is the atomic exclusive create).
func writeMarker(path string, m *Marker, exclusive bool) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".rein-run-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !exclusive {
		return os.Rename(tmp.Name(), path)
	}
	err = os.Link(tmp.Name(), path)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	f, ferr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) // no hard links on this filesystem
	if ferr != nil {
		return ferr
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// OwnerAlive reports whether the owner process still runs: pid + start time (+ boot id on Linux). A probe that
// fails for any reason but "no such process" counts as alive, so a permission error never silences the guard.
func (m *Marker) OwnerAlive() bool {
	if m.PID <= 0 {
		return false
	}
	st, err := ProcStart(m.PID)
	if errors.Is(err, ErrNoProc) {
		return false
	}
	if err != nil {
		return true
	}
	if m.StartTime != 0 && st != 0 && st != m.StartTime {
		return false
	}
	if m.BootID != "" {
		if b := bootID(); b != "" && b != m.BootID {
			return false
		}
	}
	return true
}

// Allows reports whether the user recorded an allowance of this kind for ref.
func (m *Marker) Allows(kind, ref string) bool {
	for _, a := range m.Allowed {
		if a.Kind == kind && (a.Ref == ref || (kind == "commit" && len(a.Ref) >= 7 && strings.HasPrefix(ref, a.Ref))) {
			return true
		}
	}
	return false
}

// Owner identifies the coordinator: the Claude session and the Claude process.
type Owner struct {
	SessionID string
	PID       int
}

// OwnerFromEnv reads the owner from the environment of a Bash tool call (CLAUDE_CODE_SESSION_ID, CLAUDE_PID).
func OwnerFromEnv() (Owner, error) {
	s, p := os.Getenv("CLAUDE_CODE_SESSION_ID"), os.Getenv("CLAUDE_PID")
	pid, err := strconv.Atoi(p)
	if s == "" || p == "" || err != nil || pid <= 0 {
		return Owner{}, errors.New("CLAUDE_CODE_SESSION_ID / CLAUDE_PID are not set (not a Claude Code Bash call); pass --session and --pid")
	}
	return Owner{SessionID: s, PID: pid}, nil
}

// UnderClaude reports whether this process runs inside a Claude Code tool call. User-only commands refuse then.
func UnderClaude() bool {
	return os.Getenv("CLAUDE_CODE_SESSION_ID") != "" || os.Getenv("CLAUDE_PID") != ""
}

var nameRx = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidName reports whether s is usable as a run or task name.
func ValidName(s string) bool { return nameRx.MatchString(s) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func pick(profile, def []string) []string {
	if len(profile) > 0 {
		return append([]string(nil), profile...)
	}
	return append([]string(nil), def...)
}

// Start writes the marker of a new run for the repository that holds repo. It refuses when a marker exists.
func Start(repo, name string, prof *contract.Profile, who Owner) (*Marker, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("run name %q: use letters, digits, '.', '_' and '-'", name)
	}
	if who.SessionID == "" || who.PID <= 0 {
		return nil, errors.New("owner needs a session id and a pid")
	}
	if prof == nil {
		prof = &contract.Profile{}
	}
	loc, err := Locate(repo)
	if err != nil {
		return nil, err
	}
	switch old, err := Load(loc.Marker); {
	case err == nil:
		return nil, &ErrActive{Run: old.Run, Alive: old.OwnerAlive()}
	case !errors.Is(err, ErrNoRun):
		return nil, err
	}
	st, err := ProcStart(who.PID)
	if err != nil {
		return nil, fmt.Errorf("owner pid %d: %w", who.PID, err)
	}
	root, err := mainRoot(loc.Top)
	if err != nil {
		return nil, err
	}
	head, err := git(root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("the main checkout %s has no commit to start from: %w", root, err)
	}
	ref, err := git(root, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || ref == "" {
		ref = "HEAD"
	}
	m := &Marker{Schema: Schema, Run: name, StartedAt: now(), Root: root, RunDir: "", StartSHA: head, BaseRef: ref,
		SessionID: who.SessionID, PID: who.PID, StartTime: st, BootID: bootID(),
		CoordinatorWritable: pick(prof.CoordinatorWritable, DefaultWritable),
		CoordinatorTools:    pick(prof.CoordinatorTools, DefaultTools),
		ReadonlyAgents:      pick(prof.ReadonlyAgents, DefaultReadonlyAgents)}
	if err := writeMarker(loc.Marker, m, true); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, &ErrActive{Run: name, Alive: true}
		}
		return nil, err
	}
	return m, nil
}

// RunDir returns the absolute run directory path for a run name.
// Checks PIPELINE_RUNS first, then falls back to ~/.cache/worktree-pipeline/runs/<name>.
func RunDir(name string) string {
	if d := os.Getenv("PIPELINE_RUNS"); d != "" {
		return filepath.Join(d, name)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "worktree-pipeline", "runs", name)
}

// SetRunDir updates the marker's RunDir field if not already set (ADR 0002 B4).
// This is called by contract.New when the first contract for a run is created.
func SetRunDir(markerPath, runDir string) error {
	if runDir == "" {
		return nil // nothing to set
	}
	m, err := Load(markerPath)
	if err != nil {
		return err
	}
	if m.RunDir != "" {
		return nil // already set
	}
	m.RunDir = runDir
	return writeMarker(markerPath, m, false) // not exclusive - updating existing marker
}

// load finds the marker of the repo that holds repo.
func load(repo string) (Loc, *Marker, error) {
	loc, err := Locate(repo)
	if err != nil {
		return Loc{}, nil, err
	}
	m, err := Load(loc.Marker)
	return loc, m, err
}

// Resume rebinds the run to a new session id (after /clear, compact or resume). Only the owner process itself may
// do it: pid must be the marker's pid and that process must still be the one that started the run.
func Resume(repo, newSessionID string, pid int) (*Marker, error) {
	if newSessionID == "" {
		return nil, errors.New("session id is empty")
	}
	loc, m, err := load(repo)
	if err != nil {
		return nil, err
	}
	if !m.OwnerAlive() {
		return nil, fmt.Errorf("run %q: the owner process %d is gone; end it with `rein run end` and start a new run", m.Run, m.PID)
	}
	if pid != m.PID {
		return nil, fmt.Errorf("run %q belongs to process %d, not %d", m.Run, m.PID, pid)
	}
	m.SessionID = newSessionID
	return m, writeMarker(loc.Marker, m, false)
}

// Allow records the user's permission for Agent calls naming `rein-task: <task>`.
func Allow(repo, task, reason string) (*Marker, error) {
	if !ValidName(task) {
		return nil, fmt.Errorf("task %q: use letters, digits, '.', '_' and '-'", task)
	}
	return allow(repo, Allowance{Kind: "task", Ref: task, Reason: reason})
}

// AllowCommit records a commit audit accepts although it is not worker output (a recorded exception).
func AllowCommit(repo, sha, reason string) (*Marker, error) {
	if len(sha) < 7 || strings.Trim(sha, "0123456789abcdefABCDEF") != "" {
		return nil, fmt.Errorf("commit %q: give at least 7 hex characters of the sha", sha)
	}
	return allow(repo, Allowance{Kind: "commit", Ref: strings.ToLower(sha), Reason: reason})
}

func allow(repo string, a Allowance) (*Marker, error) {
	if strings.TrimSpace(a.Reason) == "" {
		return nil, errors.New("--reason is required: the allowance is part of the audit trail")
	}
	loc, m, err := load(repo)
	if err != nil {
		return nil, err
	}
	a.At = now()
	if !m.Allows(a.Kind, a.Ref) {
		m.Allowed = append(m.Allowed, a)
	}
	return m, writeMarker(loc.Marker, m, false)
}

// EndResult is what End did.
type EndResult struct {
	Closed   bool
	Run      string
	Findings []Finding // COORDINATOR_DRIFT found by the audit of a clean end (the run stays open)
}

// End closes the run. A clean end audits first and stays open when there is drift; abandon always closes and
// records the reason in the history file beside the marker.
func End(repo string, abandon bool, reason string, o AuditOptions) (*EndResult, error) {
	if abandon && strings.TrimSpace(reason) == "" {
		return nil, errors.New("--abandon needs --reason: it is recorded")
	}
	loc, m, err := load(repo)
	if err != nil {
		var bad *BadMarker
		if !(abandon && errors.As(err, &bad)) { // a corrupt marker can only be abandoned
			return nil, err
		}
		m = &Marker{Run: "(corrupt marker)"}
	}
	res := &EndResult{Run: m.Run}
	if !abandon {
		if res.Findings, err = auditMarker(m, o); err != nil {
			return nil, err
		}
		if len(res.Findings) > 0 {
			return res, nil
		}
	}
	h, _ := json.Marshal(map[string]any{"run": m.Run, "ended_at": now(), "abandoned": abandon, "reason": reason,
		"started_at": m.StartedAt, "allowed": m.Allowed})
	histPath := filepath.Join(filepath.Dir(loc.Marker), "rein-run-history.jsonl")
	if f, err := os.OpenFile(histPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, string(h))
		f.Close()
	}
	ClearAgents(loc.Marker)
	if err := os.Remove(loc.Marker); err != nil {
		return nil, err
	}
	res.Closed = true
	return res, nil
}
