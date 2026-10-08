// Package contract holds the machine-checked part of a task spec. The coordinator writes one contract per task
// before worker-start; the guard hook and the drift check read the same file, so both judge with the same rules.
package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AlwaysDeny is added to every contract: generic files no worker should edit. Project-specific ones come from
// the profile's deny_paths.
var AlwaysDeny = []string{"**/node_modules/**", ".git", "**/.git/**"}

// Profile holds a project's own rules (its live stack, owner-only scripts, local artifacts). It is copied into
// each contract at creation, so the hook reads one file. Keep real profiles out of public repos.
type Profile struct {
	Name                     string   `json:"name,omitempty"`
	WorktreeRoot             string   `json:"worktree_root,omitempty"`              // default for contract new
	ProtectContainerPrefixes []string `json:"protect_container_prefixes,omitempty"` // docker stop/rm/exec/... on these is denied
	ProtectPorts             []int    `json:"protect_ports,omitempty"`              // localhost:<port> anywhere in a command is denied
	OwnerScripts             []string `json:"owner_scripts,omitempty"`              // path suffixes only the owner runs (release, seed)
	DenyCommands             []string `json:"deny_commands,omitempty"`              // "psql", "docker compose", "playwright test"
	DenyPaths                []string `json:"deny_paths,omitempty"`                 // never-edit globs added to every contract
	LocalArtifacts           []string `json:"local_artifacts,omitempty"`            // untracked paths the drift check ignores
}

// LoadProfile reads a profile file; "" means $REIN_PROFILE, then no profile (generic rules only).
func LoadProfile(path string) (*Profile, error) {
	if path == "" {
		path = os.Getenv("REIN_PROFILE")
	}
	if path == "" {
		return &Profile{}, nil
	}
	b, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	return &p, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

type Contract struct {
	Name            string   `json:"name"`
	Issue           int      `json:"issue,omitempty"`
	Worktree        string   `json:"worktree"`
	Allow           []string `json:"allow"`
	Deny            []string `json:"deny"`
	Scope           []string `json:"scope"`
	ReportPath      string   `json:"report_path"`
	WorkerWritable  []string `json:"worker_writable,omitempty"` // exact files outside the worktree the worker may write
	MaxChangedLines int      `json:"max_changed_lines,omitempty"`
	Profile         Profile  `json:"profile"`
}

// IndexDir is where the hook looks contracts up by worktree directory name.
func IndexDir() string {
	if d := os.Getenv("PIPELINE_CONTRACTS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "worktree-pipeline", "contracts")
}

func PathOf(name string) string { return filepath.Join(IndexDir(), name+".json") }

// ErrNone means "no contract": the session is not a pipeline worker.
var ErrNone = errors.New("no contract")

func Load(name string) (*Contract, error) {
	b, err := os.ReadFile(PathOf(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNone
	}
	if err != nil {
		return nil, err
	}
	var c Contract
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("contract %s: %w", name, err)
	}
	if c.Name == "" || c.Worktree == "" || len(c.Allow) == 0 || len(c.Scope) == 0 || c.ReportPath == "" {
		return nil, fmt.Errorf("contract %s: name, worktree, allow, scope and report_path are required", name)
	}
	return &c, nil
}

// Writable reports whether abs (an absolute, cleaned path outside the worktree) is a file the worker may write.
func (c *Contract) Writable(abs string) bool {
	for _, p := range append([]string{c.ReportPath}, c.WorkerWritable...) {
		if SamePath(abs, p) {
			return true
		}
	}
	return false
}

// SamePath compares two paths after resolving symlinks where possible (macOS /tmp -> /private/tmp).
func SamePath(a, b string) bool {
	return Real(a) == Real(b)
}

// Real cleans a path and resolves symlinks of its longest existing prefix (the file itself may not exist yet).
func Real(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if dir == "" || dir == p {
		return p
	}
	return filepath.Join(Real(strings.TrimSuffix(dir, string(filepath.Separator))), base)
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// New builds a contract from CLI values plus a profile and writes it to the run dir and the index.
// worktreeRoot "" falls back to the profile's worktree_root.
func New(name string, issue int, runDir, allow, deny, scope, worktreeRoot, reportPath, writable string, maxLines int, prof *Profile) (*Contract, error) {
	expand := expandHome
	if prof == nil {
		prof = &Profile{}
	}
	if worktreeRoot == "" {
		worktreeRoot = prof.WorktreeRoot
	}
	if worktreeRoot == "" {
		return nil, errors.New("--worktree-root is required (or set worktree_root in the profile)")
	}
	runDir, worktreeRoot = expand(runDir), expand(worktreeRoot)
	c := &Contract{Name: name, Issue: issue, Worktree: filepath.Join(worktreeRoot, name),
		Allow: splitList(allow), Scope: splitList(scope), MaxChangedLines: maxLines, Profile: *prof}
	seen := map[string]bool{}
	all := append(append(append([]string{}, AlwaysDeny...), prof.DenyPaths...), splitList(deny)...)
	for _, d := range all {
		if !seen[d] {
			seen[d] = true
			c.Deny = append(c.Deny, d)
		}
	}
	if len(c.Allow) == 0 || len(c.Scope) == 0 {
		return nil, errors.New("--allow and --scope must not be empty")
	}
	c.ReportPath = expand(reportPath)
	if c.ReportPath == "" {
		c.ReportPath = filepath.Join(runDir, "reports", name+".md")
	}
	for _, w := range splitList(writable) {
		c.WorkerWritable = append(c.WorkerWritable, expand(w))
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	for _, p := range []string{filepath.Join(runDir, "contracts", name+".json"), PathOf(name)} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Snippet is the text to paste into the spec under "## Ownership (machine-checked)".
func (c *Contract) Snippet() string {
	lines := []string{
		"## Ownership (machine-checked; the guard hook and the coordinator's drift check enforce it)",
		"- Edit ONLY files matching: " + strings.Join(c.Allow, ", "),
		"- Never edit: " + strings.Join(c.Deny, ", "),
		"- Scope ids: your report needs one heading per id (" + strings.Join(c.Scope, ", ") + ") with status done / partly / not done",
		"- Report path (write it before you finish): " + c.ReportPath,
	}
	if c.MaxChangedLines > 0 {
		lines = append(lines, fmt.Sprintf("- Change budget: about %d changed lines; say why if you exceed it", c.MaxChangedLines))
	}
	return strings.Join(lines, "\n")
}
