// Package hooks writes the per-vendor hook files that make each worker CLI call `rein hook --vendor X` inside a
// task's worktree, so one contract and one judge (internal/guard) protect codex, agy, kiro, opencode and Claude.
// Every file is written inside the worktree and kept out of git status; nothing global is touched.
package hooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/sandbox"
)

// Default vendors: Claude workers are covered by the plugin's global hook, so "claude" is opt-in.
var Default = []string{"codex", "agy", "kiro", "opencode"}

// All is every vendor `rein hooks install` knows.
var All = []string{"codex", "agy", "kiro", "opencode", "claude"}

// launch is what the coordinator must do when starting each vendor's worker, or its hook is silently skipped.
var launch = map[string]string{
	"agy":      "start agy inside the worktree (agy -p ... or interactive); no extra flag. Hooks load from <worktree>/.agents/hooks.json",
	"kiro":     "kiro-cli chat --agent rein --trust-all-tools --model <model> ...   (--agent rein is required on EVERY launch; without it no hook runs)",
	"opencode": "start opencode2 inside the worktree (opencode2 run --standalone --auto -m <model> ...); no extra flag. The plugin loads from <worktree>/.opencode/plugins/rein",
	"claude":   "no flag; Claude Code reads <worktree>/.claude/settings.local.json. Do not combine with the rein plugin's global hook unless you accept two calls per tool use",
}

// CodexFlags are the codex launch flags that make the guard run (POSIX sh quoting; on Windows use Git Bash).
// Codex loads <worktree>/.codex/hooks.json only for a trusted project, and for a LINKED git worktree it reads the
// main checkout's .codex instead, so the file alone is not reliable. Hooks given with -c work anywhere, and
// --dangerously-bypass-hook-trust is needed because codex otherwise skips hooks it has no stored trust hash for,
// without a message.
func CodexFlags(bin, task string) string {
	a := CodexArgs(bin, task)
	return a[0] + " -c " + shellQuote(a[2]) + " -c " + shellQuote(a[4])
}

// CodexArgs is also used by the routed launcher, so it cannot omit or replace the worker hook.
func CodexArgs(bin, task string) []string {
	h := `[{type="command",command=` + tomlStr(command(bin, "codex", task)) + `,timeout=10}]`
	return []string{"--dangerously-bypass-hook-trust", "-c", `hooks.PreToolUse=[{matcher="*",hooks=` + h + `}]`, "-c", `hooks.Stop=[{hooks=` + h + `}]`}
}

// LaunchLine is the exact launch advice for one vendor. Commands are POSIX sh (Git Bash on Windows); PowerShell
// and cmd.exe quoting are not covered.
func LaunchLine(vendor, bin, task string) string {
	if vendor == "codex" {
		return "codex exec " + CodexFlags(bin, task) + " -C <worktree> ...   (POSIX sh syntax; drop `exec` for the interactive TUI; Orca `worker-start --agent codex` cannot pass these flags: start codex in a shell terminal (preamble route) or with the direct codex exec fallback; both are operator-owned and unsupervised unless an adoption receipt proves otherwise; re-install hooks before every retry/fallback and use the fresh flags before testing a tool call)"
	}
	return launch[vendor]
}

func tomlStr(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// shellQuote single-quotes s for POSIX sh: nothing inside single quotes is special, so Windows backslashes and
// spaces survive `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_./:@+-]+$`)

func quoteWord(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return shellQuote(s)
}

// Result is what one vendor's install did.
type Result struct {
	Vendor string
	Files  []string // paths written, relative to the worktree (slash form)
	Launch string
	Err    error
}

// Install writes the hook files of the given vendors into the contract's worktree. bin is the absolute path of
// the rein binary the hooks call. It records the installed vendors, the install time and a new generation id in
// the contract and returns the contract files it updated.
func Install(c *contract.Contract, bin string, vendors []string) ([]Result, []string, error) {
	wt := c.Worktree
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		return nil, nil, fmt.Errorf("worktree %s does not exist yet: create it first (orca worktree create --name %s ...), then run rein hooks install", wt, c.Name)
	}
	canon, err := filepath.EvalSymlinks(wt)
	if err != nil {
		return nil, nil, err
	}
	var res []Result
	var ok []string
	for _, v := range vendors {
		r := Result{Vendor: v, Launch: LaunchLine(v, bin, c.Name)}
		w, has := writers[v]
		if !has {
			r.Err = fmt.Errorf("unknown vendor %q (known: %s)", v, strings.Join(All, ", "))
			res = append(res, r)
			continue
		}
		r.Files, r.Err = w(canon, c, bin)
		if r.Err == nil {
			for _, f := range r.Files {
				if err := sandbox.Exclude(canon, f); err != nil {
					r.Err = fmt.Errorf("written, but could not add %s to info/exclude: %w", f, err)
					break
				}
			}
		}
		if r.Err == nil {
			ok = append(ok, v)
		}
		res = append(res, r)
	}
	if len(ok) == 0 {
		return res, nil, nil
	}
	c.HooksInstalled = union(c.HooksInstalled, ok)
	c.HooksInstalledAt = time.Now().Format(time.RFC3339)
	c.HooksGeneration = newGeneration()
	saved, err := c.Save()
	if err != nil {
		return res, nil, fmt.Errorf("hooks written, but the contract could not be updated: %w", err)
	}
	return res, saved, nil
}

func newGeneration() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// BinaryPath is the rein binary the hook files should call: the plugin's stable path when it exists (it survives
// plugin updates), else the running binary with symlinks resolved. warn is non-empty when that path will likely
// not survive (a temp directory or a version-numbered directory).
func BinaryPath() (path, warn string, err error) {
	if d := os.Getenv("CLAUDE_PLUGIN_DATA"); d != "" {
		name := "rein"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(d, "bin", name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, "", nil
		}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", "", err
	}
	return exe, unstable(exe), nil
}

var versionDir = regexp.MustCompile(`^v?\d+\.\d+(\.\d+)?([-+].*)?$`)

func unstable(p string) string {
	temps := []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp", "/private/var/folders"}
	for _, t := range temps {
		if r, err := filepath.EvalSymlinks(t); err == nil {
			t = r
		}
		if rel, err := filepath.Rel(t, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p + " is under a temp directory; the hooks stop working when it is cleaned. Install rein to a stable path or set up the plugin"
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(p)), "/") {
		if versionDir.MatchString(part) {
			return p + " is under a version-numbered directory (" + part + "); the hooks break when that version is replaced"
		}
	}
	return ""
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

type writer func(wt string, c *contract.Contract, bin string) ([]string, error)

var writers = map[string]writer{"codex": writeCodex, "agy": writeAgy, "kiro": writeKiro, "opencode": writeOpencode, "claude": writeClaude}

// command is what a vendor runs: the hook bound to its task, so it cannot be redirected to another contract.
func command(bin, vendor, task string) string {
	return shellQuote(bin) + " hook --vendor " + vendor + " --task " + quoteWord(task)
}

// safeDest rejects a destination whose file or any parent below the canonical worktree is a symlink: writing
// through one could rewrite a file outside the worktree (for example a global vendor config).
func safeDest(canon, rel string) error {
	p := canon
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil // the rest does not exist yet, so it cannot be a link
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link (%s); refusing to write through it", filepath.ToSlash(strings.TrimPrefix(p, canon+string(filepath.Separator))), p)
		}
	}
	return nil
}

// prepare runs the checks every destination needs before anything is written.
func prepare(wt, rel string) (string, error) {
	if err := safeDest(wt, rel); err != nil {
		return "", err
	}
	if err := refuseTracked(wt, rel); err != nil {
		return "", err
	}
	return filepath.Join(wt, filepath.FromSlash(rel)), nil
}

// refuseTracked stops an install that would modify a file the repository tracks: the edit would show up as
// uncommitted work in the worker's diff and the drift check would blame the worker.
func refuseTracked(wt, rel string) error {
	out, err := exec.Command("git", "-C", wt, "ls-files", "--", rel).Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("%s is tracked by git; installing would dirty the worktree (add the hook to that file in the repository instead)", rel)
	}
	return nil
}

func readJSON(path string) (map[string]any, error) {
	doc := map[string]any{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
	}
	return doc, nil
}

func writeJSON(path string, doc any) error {
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// mergeGroups puts group into doc["hooks"][event]. An earlier rein entry (found by its command) is removed from
// whatever group holds it, so a second install changes nothing; the user's own entries stay, and a group that
// also held other hooks keeps them and its metadata.
func mergeGroups(doc map[string]any, event, marker string, group map[string]any) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	var keep []any
	list, _ := hooks[event].([]any)
	for _, g := range list {
		gm, isMap := g.(map[string]any)
		if !isMap {
			keep = append(keep, g)
			continue
		}
		entries, _ := gm["hooks"].([]any)
		var rest []any
		for _, e := range entries {
			if b, _ := json.Marshal(e); !strings.Contains(string(b), marker) {
				rest = append(rest, e)
			}
		}
		if len(rest) == len(entries) {
			keep = append(keep, g)
		} else if len(rest) > 0 {
			gm["hooks"] = rest
			keep = append(keep, gm)
		}
	}
	hooks[event] = append(keep, group)
	doc["hooks"] = hooks
}

// claudeStyle writes PreToolUse + PostToolUse + Stop groups in the Claude settings format (codex and Claude share it).
func claudeStyle(wt, rel, vendor, task, bin, matcher string) ([]string, error) {
	path, err := prepare(wt, rel)
	if err != nil {
		return nil, err
	}
	doc, err := readJSON(path)
	if err != nil {
		return nil, err
	}
	h := []any{map[string]any{"type": "command", "command": command(bin, vendor, task), "timeout": 10}}
	marker := "hook --vendor " + vendor
	mergeGroups(doc, "PreToolUse", marker, map[string]any{"matcher": matcher, "hooks": h})
	mergeGroups(doc, "PostToolUse", marker, map[string]any{"matcher": "AskUserQuestion", "hooks": h})
	mergeGroups(doc, "Stop", marker, map[string]any{"hooks": h})
	return []string{rel}, writeJSON(path, doc)
}

func writeCodex(wt string, c *contract.Contract, bin string) ([]string, error) {
	return claudeStyle(wt, ".codex/hooks.json", "codex", c.Name, bin, "*")
}

func writeClaude(wt string, c *contract.Contract, bin string) ([]string, error) {
	return claudeStyle(wt, ".claude/settings.local.json", "claude", c.Name, bin, "Bash|Edit|Write|MultiEdit|NotebookEdit|Agent|AskUserQuestion")
}

func writeAgy(wt string, c *contract.Contract, bin string) ([]string, error) {
	const rel = ".agents/hooks.json"
	path, err := prepare(wt, rel)
	if err != nil {
		return nil, err
	}
	doc, err := readJSON(path)
	if err != nil {
		return nil, err
	}
	cmd := map[string]any{"type": "command", "command": command(bin, "agy", c.Name), "timeout": 10}
	doc["rein"] = map[string]any{ // Stop is flat in agy: no matcher/hooks wrapper
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{cmd}}},
		"Stop":       []any{cmd},
	}
	return []string{rel}, writeJSON(path, doc)
}

func writeKiro(wt string, c *contract.Contract, bin string) ([]string, error) {
	const rel = ".kiro/agents/rein.json"
	path, err := prepare(wt, rel)
	if err != nil {
		return nil, err
	}
	cmd := command(bin, "kiro", c.Name)
	agent := map[string]any{
		"name": "rein", "description": "rein guard for task " + c.Name, "prompt": nil, "tools": []string{"*"},
		"hooks": map[string]any{
			"preToolUse": []any{map[string]any{"matcher": "*", "command": cmd}},
			"stop":       []any{map[string]any{"command": cmd}},
		},
	}
	return []string{rel}, writeJSON(path, agent)
}

// pluginJS is the opencode v2 plugin: it pipes each tool call to `rein hook --vendor opencode --task T` and
// throws unless the guard exits 0, which opencode shows the model. The task and worktree are fixed here (opencode
// puts no directory in the hook input), and a guard that cannot run is a denial too: the plugin only exists
// inside a contracted worktree.
const pluginJS = `import { spawnSync } from "node:child_process";

const REIN = %s;
const REIN_TASK = %s;
const WORKTREE = %s;

export default {
  id: "rein-guard",
  setup(ctx) {
    ctx.tool.hook("execute.before", async (i) => {
      const r = spawnSync(REIN, ["hook", "--vendor", "opencode", "--task", REIN_TASK], {
        input: JSON.stringify({ tool: i.tool, input: i.input, cwd: WORKTREE }),
        encoding: "utf8",
        timeout: 10000,
      });
      if (r.error) throw new Error("rein: guard did not run (" + r.error.message + ")");
      if (r.status !== 0) throw new Error((r.stderr || "").trim() || "rein: guard exited " + r.status);
    });
  },
};
`

func writeOpencode(wt string, c *contract.Contract, bin string) ([]string, error) {
	const rel = ".opencode/plugins/rein/server.js"
	path, err := prepare(wt, rel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// the plugin runs the binary without a shell: the raw path, as JS string literals
	binLit, _ := json.Marshal(bin)
	taskLit, _ := json.Marshal(c.Name)
	wtLit, _ := json.Marshal(wt)
	return []string{rel}, os.WriteFile(path, []byte(fmt.Sprintf(pluginJS, binLit, taskLit, wtLit)), 0o644)
}
