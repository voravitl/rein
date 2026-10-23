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
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/sandbox"
)

// Default vendors: Claude workers are covered by the plugin's global hook, so "claude" is opt-in.
// "hermes" is opt-in too: its plugin lives in the task's Hermes home, and installing it by default
// would hide a worker's own .hermes files from git status and drift on every other vendor.
var Default = []string{"codex", "agy", "kiro", "opencode"}

// All is every vendor `rein hooks install` knows.
var All = []string{"codex", "agy", "kiro", "opencode", "claude", "hermes"}

// launch is what the coordinator must do when starting each vendor's worker, or its hook is silently skipped.
var launch = map[string]string{
	"agy":      "start agy inside the worktree (agy -p ... or interactive); no extra flag. Hooks load from <worktree>/.agents/hooks.json",
	"kiro":     "kiro-cli chat --agent rein --trust-all-tools --model <model> ...   (--agent rein is required on EVERY launch; without it no hook runs)",
	"opencode": "start opencode2 inside the worktree (opencode2 run --standalone --auto -m <model> ...); no extra flag. The plugin loads from <worktree>/.opencode/plugins/rein",
	"hermes":   "start hermes inside the worktree with HERMES_ENABLE_PROJECT_PLUGINS=1 and the task's HERMES_HOME, through `rein route launch --task <t> --run <r> -- hermes --cli chat --model <provider:model> --query <prompt> --oneshot`; the plugin loads from <worktree>/.hermes/plugins/rein-guard. Do not use Orca worker-start --agent hermes: it starts Hermes with the user's own home, so the task's plugin never loads",
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

var writers = map[string]writer{"codex": writeCodex, "agy": writeAgy, "kiro": writeKiro, "opencode": writeOpencode, "claude": writeClaude, "hermes": writeHermes}

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
	if err != nil {
		return fmt.Errorf("cannot check tracked destination %s: %w", rel, err)
	}
	if strings.TrimSpace(string(out)) != "" {
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
	if doc == nil {
		return nil, fmt.Errorf("%s: expected JSON object", path)
	}
	if value, exists := doc["hooks"]; exists {
		if hooks, ok := value.(map[string]any); !ok {
			return nil, fmt.Errorf("%s: hooks must be an object", path)
		} else {
			for _, event := range []string{"PreToolUse", "PostToolUse", "Stop"} {
				if groups, exists := hooks[event]; exists {
					list, ok := groups.([]any)
					if !ok {
						return nil, fmt.Errorf("%s: %s must be an array", path, event)
					}
					for _, group := range list {
						gm, ok := group.(map[string]any)
						if !ok {
							return nil, fmt.Errorf("%s: malformed %s group", path, event)
						}
						entries, ok := gm["hooks"].([]any)
						if !ok {
							return nil, fmt.Errorf("%s: malformed %s hooks", path, event)
						}
						for _, entry := range entries {
							if _, ok := entry.(map[string]any); !ok {
								return nil, fmt.Errorf("%s: malformed hook entry", path)
							}
						}
					}
				}
			}
		}
	}
	return doc, nil
}

func writeJSON(path string, doc any) error {
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// writeFileAtomic replaces path through a temp file in the same directory, so a
// crash or full disk cannot truncate settings that also hold the user's own
// hooks.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".rein-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mergeGroups puts group into doc["hooks"][event]. An earlier rein entry (found by its command) is removed from
// whatever group holds it, so a second install changes nothing; the user's own entries stay, and a group that
// also held other hooks keeps them and its metadata.
func mergeGroups(doc map[string]any, event, marker string, group map[string]any) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		if group == nil {
			return
		}
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
			if m, ok := e.(map[string]any); !ok || !ownedCommand(m["command"], marker) {
				rest = append(rest, e)
			}
		}
		if len(rest) == len(entries) {
			keep = append(keep, g)
		} else if len(rest) > 0 || hasGroupMetadata(gm) {
			if rest == nil {
				rest = []any{}
			}
			gm["hooks"] = rest
			keep = append(keep, gm)
		}
	}
	if group != nil {
		keep = append(keep, group)
	}
	if len(keep) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = keep
	}
	doc["hooks"] = hooks
}

func hasGroupMetadata(group map[string]any) bool {
	for key := range group {
		if key != "matcher" && key != "hooks" {
			return true
		}
	}
	return false
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
	if err := cleanShared(doc, vendor, task); err != nil {
		return nil, err
	}
	h := []any{map[string]any{"type": "command", "command": command(bin, vendor, task), "timeout": 10}}
	marker := vendor + "\x00" + task
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
	next := map[string]any{ // Stop is flat in agy: no matcher/hooks wrapper
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{cmd}}},
		"Stop":       []any{cmd},
	}
	if old, exists := doc["rein"]; exists && !ownedObject(old, "agy", c.Name) {
		return nil, fmt.Errorf("%s: foreign rein namespace; refusing replacement", rel)
	}
	delete(doc, "rein")
	if err := noTaskCommands(doc, "agy", c.Name); err != nil {
		return nil, err
	}
	doc["rein"] = next
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
	if old, err := readJSON(path); err != nil {
		return nil, err
	} else if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) && !ownedObject(old, "kiro", c.Name) {
		return nil, fmt.Errorf("%s: foreign agent; refusing replacement", rel)
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
    const guard = (i) => {
      const r = spawnSync(REIN, ["hook", "--vendor", "opencode", "--task", REIN_TASK], {
        input: JSON.stringify({ tool: i.tool, input: i.input, cwd: WORKTREE }),
        encoding: "utf8",
        timeout: 10000,
      });
      if (r.error) throw new Error("rein: guard did not run (" + r.error.message + ")");
      if (r.status !== 0) throw new Error((r.stderr || "").trim() || "rein: guard exited " + r.status);
    };
    ctx.tool.hook("execute.before", guard);
    ctx.shell.hook("create.before", (i) => guard({ tool: "shell", input: { command: i.command, workdir: i.cwd } }));
  },
};
`

func writeOpencode(wt string, c *contract.Contract, bin string) ([]string, error) {
	rels := []string{".opencode/plugins/rein/server.js", ".opencode/plugins/rein/package.json"}
	for _, rel := range rels {
		if _, err := prepare(wt, rel); err != nil {
			return nil, err
		}
	}
	if err := checkPlugin(wt, c.Name); err != nil {
		return nil, err
	}
	binLit, _ := json.Marshal(bin)
	taskLit, _ := json.Marshal(c.Name)
	wtLit, _ := json.Marshal(wt)
	path := filepath.Join(wt, rels[0])
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(pluginJS, binLit, taskLit, wtLit)), 0644); err != nil {
		return nil, err
	}
	return rels, os.WriteFile(filepath.Join(wt, rels[1]), []byte(pluginPackage), 0644)
}

const pluginPackage = `{
  "type": "module",
  "main": "server.js"
}
`

// hermesPluginYAML is the manifest Hermes' project-plugin loader reads.
const hermesPluginYAML = `name: rein-guard
version: 0.1.0
description: "rein guard for task %s"
kind: standalone
provides_hooks:
  - pre_tool_call
  - completion_check
  - pre_verify
`

// hermesConfigYAML enables the guard plugin in the task's Hermes home. Hermes loads plugins from an opt-in
// allow-list, so the plugin is written next to the list that activates it.
const hermesConfigYAML = `plugins:
  enabled:
    - rein-guard
`

// Recognize only these exact task-home templates; never rewrite arbitrary user YAML.
const hermesCruxConfigYAML = `plugins:
  enabled:
    - crux
`

const hermesCombinedConfigYAML = `plugins:
  enabled:
    - rein-guard
    - crux
`

// hermesPluginInit is the Hermes project plugin: it pipes every tool call to `rein hook --vendor hermes --task T`
// and blocks the tool when the guard denies. The task and worktree are fixed here (Hermes puts no directory in
// the hook input), and a guard that cannot run is a denial too: the plugin only exists inside a contracted worktree.
const hermesPluginInit = `import subprocess, json, sys

REIN = %s
REIN_TASK = %s
WORKTREE = %s


def _guard(i=None, **kwargs):
    tool = i.get("tool_name") if isinstance(i, dict) and i.get("tool_name") else (kwargs.get("tool_name") or (i.get("tool") if isinstance(i, dict) else None))
    args = (i.get("args") if isinstance(i, dict) and i.get("args") else None) or kwargs.get("args") or (i.get("tool_input") if isinstance(i, dict) else None) or {}
    payload = {"tool": tool, "input": args, "cwd": WORKTREE}
    r = subprocess.run([REIN, "hook", "--vendor", "hermes", "--task", REIN_TASK],
                       input=json.dumps(payload), capture_output=True, text=True, timeout=10)
    try:
        out = json.loads(r.stdout) if r.stdout.strip() else {}
    except Exception:
        out = {}
    msg = out.get("message")
    if out.get("action") == "block" and msg:
        return {"action": "block", "message": msg}
    # A guard that could not run is a denial, not a silent allow.
    if r.returncode not in (0, None) or (not r.stdout.strip() and r.stderr.strip()):
        return {"action": "block", "message": "[rein] guard did not run: " + (r.stderr.strip() or "exit " + str(r.returncode))}
    return None


def _report_gate(**kwargs):
    """Mandatory completion policy, checked even after read-only turns."""
    payload = {"tool": "pre_verify", "input": {}, "cwd": WORKTREE,
               "hook_event_name": "pre_verify"}
    try:
        r = subprocess.run([REIN, "hook", "--vendor", "hermes", "--task", REIN_TASK],
                           input=json.dumps(payload), capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return {"action": "block", "message": "[rein] guard did not run: " + str(exc)}
    if r.returncode != 0:
        return {"action": "block", "message": "[rein] report guard failed: " + (r.stderr.strip() or str(r.returncode))}
    # Rein's successful stop check deliberately emits no output.
    if not r.stdout.strip() and not r.stderr.strip():
        return {"action": "allow"}
    try:
        out = json.loads(r.stdout)
    except (ValueError, TypeError):
        return {"action": "block", "message": "[rein] report guard returned malformed output"}
    if isinstance(out, dict) and out.get("action") == "continue" and isinstance(out.get("message"), str) and out["message"].strip():
        return {"action": "continue", "message": out["message"]}
    return {"action": "block", "message": "[rein] report guard returned an invalid decision"}


def register(ctx):
    ctx.register_hook("pre_tool_call", _guard)
    ctx.register_hook("completion_check", _report_gate)
    # Older Hermes retains this optional gate; Rein also checks the report after exit.
    ctx.register_hook("pre_verify", _report_gate)
`

func writeHermes(wt string, c *contract.Contract, bin string) ([]string, error) {
	rels := []string{".hermes/plugins/rein-guard/plugin.yaml", ".hermes/plugins/rein-guard/__init__.py", ".hermes/config.yaml"}
	for _, rel := range rels {
		if _, err := prepare(wt, rel); err != nil {
			return nil, err
		}
	}
	if err := checkHermesPlugin(wt, c.Name, bin); err != nil {
		return nil, err
	}
	cfg := filepath.Join(wt, ".hermes", "config.yaml")
	existing, err := os.ReadFile(cfg)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	configExists := err == nil
	base, credentialHome, err := splitHermesCredentials(string(existing))
	if err != nil {
		return nil, err
	}
	// Only the generated config can be verified without interpreting arbitrary YAML.
	config := hermesConfigYAML
	switch base {
	case "":
		if configExists {
			return nil, fmt.Errorf("existing empty .hermes/config.yaml is not owned by rein; preserve it and use a fresh task Hermes home")
		}
	case hermesConfigYAML:
	case hermesCruxConfigYAML, hermesCombinedConfigYAML:
		config = hermesCombinedConfigYAML
	default:
		return nil, fmt.Errorf("cannot verify rein-guard is enabled in existing .hermes/config.yaml; preserve it and use a fresh task Hermes home")
	}
	config = hermesCredentialsConfig(config, credentialHome)
	dir := filepath.Join(wt, ".hermes", "plugins", "rein-guard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(fmt.Sprintf(hermesPluginYAML, c.Name)), 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.py"), []byte(hermesInit(bin, c.Name, wt)), 0644); err != nil {
		return nil, err
	}
	if string(existing) != config {
		if err := os.WriteFile(cfg, []byte(config), 0644); err != nil {
			return nil, err
		}
	}
	// The task's Hermes home is the guard's own state (plugin, config, runtime caches); a worker's
	// `git add -A` would otherwise stage the guard's files into the worktree's commit.
	if err := sandbox.Exclude(wt, ".hermes/"); err != nil {
		return nil, fmt.Errorf("written, but could not exclude .hermes/ from git status: %w", err)
	}
	return rels, nil
}

func hermesInit(bin, task, wt string) string {
	binLit, _ := json.Marshal(bin)
	taskLit, _ := json.Marshal(task)
	wtLit, _ := json.Marshal(wt)
	return fmt.Sprintf(hermesPluginInit, binLit, taskLit, wtLit)
}

// Validate the whole plugin before any write or removal; never traverse a linked parent.
// CheckHermesPlugin verifies that the installed guard plugin is still the one rein wrote.
// A replaced plugin file means the worker would run a different judge.
func CheckHermesPlugin(wt, task string) error {
	return checkHermesPlugin(wt, task, "")
}

// CheckHermesGuard verifies, at launch, that the task's guard plugin is installed, complete and still
// calls this rein. The install-time check cannot do this: it tolerates a missing plugin (it is about to
// write one) and it accepts whatever binary the file names, because a self-consistent file can still
// point somewhere else. A launch without this check runs the worker with no judge at all: a deleted
// plugin directory, a partial plugin, or a plugin whose REIN names another program all look installed.
func CheckHermesGuard(wt, task string) error {
	const dir = ".hermes/plugins/rein-guard"
	entries, err := os.ReadDir(filepath.Join(wt, filepath.FromSlash(dir)))
	if err != nil {
		return fmt.Errorf("the Hermes guard plugin is not installed at %s: %w", dir, err)
	}
	present := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "__pycache__" {
			// Python writes the cache next to the source and loads it in preference to the source,
			// so a cached .pyc can run instead of the file that was checked. A cache under the
			// plugin directory is therefore never trusted: it is removed, and a plugin whose
			// cache cannot be removed is refused.
			if err := removeHermesCache(filepath.Join(wt, filepath.FromSlash(dir), name)); err != nil {
				return err
			}
			continue
		}
		if name != "plugin.yaml" && name != "__init__.py" {
			return fmt.Errorf("foreign Hermes plugin file %s; refusing the launch", name)
		}
		present[name] = true
	}
	for _, name := range []string{"plugin.yaml", "__init__.py"} {
		if !present[name] {
			return fmt.Errorf("the Hermes guard plugin is incomplete: %s is missing from %s; reinstall it with rein hooks uninstall <task> --vendors hermes && rein hooks install <task> --vendors hermes", name, dir)
		}
	}
	if err := checkHermesPlugin(wt, task, ""); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(wt, filepath.FromSlash(dir+"/__init__.py")))
	if err != nil {
		return err
	}
	_, rest, found := strings.Cut(string(body), "\nREIN = ")
	if !found {
		return fmt.Errorf("the Hermes guard plugin names no rein binary; refusing the launch")
	}
	binLine, _, _ := strings.Cut(rest, "\n")
	var bin string
	if json.Unmarshal([]byte(binLine), &bin) != nil {
		return fmt.Errorf("the Hermes guard plugin names no rein binary; refusing the launch")
	}
	if !isReinBinary(bin) {
		return fmt.Errorf("the Hermes guard plugin calls %s instead of this rein; refusing the launch: reinstall it with rein hooks uninstall <task> --vendors hermes && rein hooks install <task> --vendors hermes", bin)
	}
	return nil
}

// removeHermesCache deletes the Python bytecode cache under the guard plugin. A symlinked or
// non-directory cache is refused rather than removed: following it would delete a path outside
// the plugin, and a cache that cannot be removed is a cache that may still be loaded.
func removeHermesCache(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("the Hermes guard plugin cache %s is not a plain directory; refusing the launch", filepath.Base(path))
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("cannot remove the Hermes guard plugin cache %s: %w", filepath.Base(path), err)
	}
	return nil
}

// CheckHermesPluginDir refuses a plugin beside the guard that the worker could have written. Hermes
// loads every enabled plugin into the worker's own process, so a sibling plugin can neutralise the
// guard: patching subprocess.run makes every tool call look allowed. A plugin that is a symlink to a
// directory outside the worktree (the crux plugin, installed that way) is not the worker's to write,
// so it is allowed; anything the worker could have created inside the worktree is refused.
func CheckHermesPluginDir(wt string) error {
	const dir = ".hermes/plugins"
	root := filepath.Join(wt, filepath.FromSlash(dir))
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "rein-guard" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("the Hermes plugin %s is a broken symlink: %w", entry.Name(), err)
			}
			if insideWorktree(target, wt) {
				return fmt.Errorf("the Hermes plugin %s points inside the worktree; the worker could replace it, so remove it or use a fresh task Hermes home", entry.Name())
			}
			continue
		}
		return fmt.Errorf("the Hermes task home holds another plugin (%s); it shares the worker process and can neutralise the guard, so remove it or use a fresh task Hermes home", entry.Name())
	}
	return nil
}

// insideWorktree reports whether p lies inside dir.
func insideWorktree(p, dir string) bool {
	rel, err := filepath.Rel(contract.Real(dir), contract.Real(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isReinBinary reports whether path is the rein binary this process runs. A plugin that calls another
// program is not a guard: it would answer every tool call with success and judge nothing.
func isReinBinary(path string) bool {
	if path == "" {
		return false
	}
	if exe, err := os.Executable(); err == nil && sameFile(path, exe) {
		return true
	}
	if bin, _, err := BinaryPath(); err == nil && bin != "" && sameFile(path, bin) {
		return true
	}
	return false
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func checkHermesPlugin(wt, task, expectedBin string) error {
	const dir = ".hermes/plugins/rein-guard"
	if _, err := prepare(wt, dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(wt, dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "__pycache__" && entry.IsDir() {
			continue // Python owns its cache; leave it untouched on uninstall.
		}
		if name != "plugin.yaml" && name != "__init__.py" {
			return fmt.Errorf("foreign Hermes plugin file %s; refusing modification", name)
		}
		path, err := prepare(wt, dir+"/"+name)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		expected := fmt.Sprintf(hermesPluginYAML, task)
		if name == "__init__.py" {
			_, rest, found := strings.Cut(string(body), "\nREIN = ")
			binLine, _, _ := strings.Cut(rest, "\n")
			var bin string
			if !found || json.Unmarshal([]byte(binLine), &bin) != nil || (expectedBin != "" && bin != expectedBin) {
				return fmt.Errorf("foreign Hermes plugin file %s; refusing modification", name)
			}
			expected = hermesInit(bin, task, wt)
		}
		if string(body) != expected {
			return fmt.Errorf("foreign, modified, or older Hermes plugin file %s; refusing modification (preserve and inspect it before using a fresh task Hermes home)", name)
		}
	}
	return nil
}

func ownedCommand(value any, identity string) bool {
	parts := strings.SplitN(identity, "\x00", 2)
	if len(parts) != 2 {
		return false
	}
	_, ok := commandBinary(value, parts[0], parts[1])
	return ok
}

func commandBinary(value any, vendor, task string) (string, bool) {
	cmd, ok := value.(string)
	if !ok {
		return "", false
	}
	suffix := " hook --vendor " + vendor + " --task " + quoteWord(task)
	if !strings.HasSuffix(cmd, suffix) {
		return "", false
	}
	quoted := strings.TrimSuffix(cmd, suffix)
	if len(quoted) < 3 || quoted[0] != '\'' || quoted[len(quoted)-1] != '\'' {
		return "", false
	}
	bin := strings.ReplaceAll(quoted[1:len(quoted)-1], `'\''`, "'")
	return bin, filepath.IsAbs(bin) && shellQuote(bin) == quoted
}

func objectCommand(value any) any {
	switch v := value.(type) {
	case map[string]any:
		if cmd, ok := v["command"]; ok {
			return cmd
		}
		for _, child := range v {
			if cmd := objectCommand(child); cmd != nil {
				return cmd
			}
		}
	case []any:
		for _, child := range v {
			if cmd := objectCommand(child); cmd != nil {
				return cmd
			}
		}
	}
	return nil
}

func ownedObject(value any, vendor, task string) bool {
	bin, ok := commandBinary(objectCommand(value), vendor, task)
	if !ok {
		return false
	}
	cmd := command(bin, vendor, task)
	var expected any
	if vendor == "agy" {
		expected = map[string]any{"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 10}}}}, "Stop": []any{map[string]any{"type": "command", "command": cmd, "timeout": 10}}}
	} else {
		expected = map[string]any{"name": "rein", "description": "rein guard for task " + task, "prompt": nil, "tools": []string{"*"}, "hooks": map[string]any{"preToolUse": []any{map[string]any{"matcher": "*", "command": cmd}}, "stop": []any{map[string]any{"command": cmd}}}}
	}
	b, _ := json.Marshal(expected)
	var normalized any
	json.Unmarshal(b, &normalized)
	return reflect.DeepEqual(value, normalized)
}

func checkPlugin(wt, task string) error {
	if _, err := prepare(wt, ".opencode/plugins/rein/server.js"); err != nil {
		return err
	}
	if _, err := prepare(wt, ".opencode/plugins/rein/package.json"); err != nil {
		return err
	}
	dir := filepath.Join(wt, ".opencode/plugins/rein")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		if _, err := os.Lstat(filepath.Join(dir, "server.js")); err != nil {
			return fmt.Errorf("OpenCode plugin has no owned server.js: %w", err)
		}
	}
	for _, e := range entries {
		if e.Name() != "server.js" && e.Name() != "package.json" {
			return fmt.Errorf("foreign OpenCode plugin file %s", e.Name())
		}
		rel := ".opencode/plugins/rein/" + e.Name()
		if _, err := prepare(wt, rel); err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if e.Name() == "package.json" {
			var got, want any
			json.Unmarshal(b, &got)
			json.Unmarshal([]byte(pluginPackage), &want)
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("foreign OpenCode package.json")
			}
			continue
		}
		pattern := regexp.MustCompile(`(?m)^const REIN = (.+);$`)
		match := pattern.FindSubmatch(b)
		var bin string
		if len(match) != 2 || json.Unmarshal(match[1], &bin) != nil {
			return fmt.Errorf("foreign OpenCode server.js")
		}
		binLit, _ := json.Marshal(bin)
		taskLit, _ := json.Marshal(task)
		wtLit, _ := json.Marshal(wt)
		if string(b) != fmt.Sprintf(pluginJS, binLit, taskLit, wtLit) {
			return fmt.Errorf("foreign or modified OpenCode server.js")
		}
	}
	return nil
}

// Uninstall removes only generated hooks belonging to this contract, preserving other owners.
func Uninstall(c *contract.Contract, vendors []string) ([]Result, []string, error) {
	wt, err := filepath.EvalSymlinks(c.Worktree)
	if err != nil {
		return nil, nil, err
	}
	var results []Result
	removed := map[string]bool{}
	for _, v := range vendors {
		r := Result{Vendor: v}
		if _, ok := writers[v]; !ok {
			r.Err = fmt.Errorf("unknown vendor %q", v)
		} else {
			r.Files, r.Err = uninstallVendor(wt, c.Name, v)
		}
		if r.Err == nil {
			removed[v] = true
		}
		results = append(results, r)
	}
	if len(removed) == 0 {
		return results, nil, nil
	}
	var remaining []string
	for _, v := range c.HooksInstalled {
		if !removed[v] {
			remaining = append(remaining, v)
		}
	}
	c.HooksInstalled = remaining
	if len(remaining) == 0 {
		c.HooksInstalledAt = ""
		c.HooksGeneration = ""
	} else {
		c.HooksGeneration = newGeneration()
	}
	saved, err := c.Save()
	return results, saved, err
}

func uninstallVendor(wt, task, vendor string) ([]string, error) {
	rels := map[string]string{"codex": ".codex/hooks.json", "claude": ".claude/settings.local.json", "agy": ".agents/hooks.json", "kiro": ".kiro/agents/rein.json"}
	if vendor == "hermes" {
		if err := checkHermesPlugin(wt, task, ""); err != nil {
			return nil, err
		}
		cfg, err := prepare(wt, ".hermes/config.yaml")
		if err != nil {
			return nil, err
		}
		config, err := os.ReadFile(cfg)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		dir := filepath.Join(wt, ".hermes", "plugins", "rein-guard")
		var removed []string
		for _, name := range []string{"plugin.yaml", "__init__.py"} {
			rel := ".hermes/plugins/rein-guard/" + name
			if err := os.Remove(filepath.Join(dir, name)); err == nil {
				removed = append(removed, rel)
			} else if !errors.Is(err, os.ErrNotExist) {
				return removed, err
			}
		}
		_ = os.Remove(dir)
		// Remove only the generated guard setting, preserving Crux and foreign config bytes.
		base, credentialHome, _ := splitHermesCredentials(string(config))
		switch base {
		case hermesConfigYAML:
			if err := os.Remove(cfg); err != nil {
				return removed, err
			}
			removed = append(removed, ".hermes/config.yaml")
		case hermesCombinedConfigYAML:
			if err := os.WriteFile(cfg, []byte(hermesCredentialsConfig(hermesCruxConfigYAML, credentialHome)), 0644); err != nil {
				return removed, err
			}
			removed = append(removed, ".hermes/config.yaml")
		}
		return removed, nil
	}
	if vendor == "opencode" {
		if err := checkPlugin(wt, task); err != nil {
			return nil, err
		}
		var removed []string
		for _, name := range []string{"server.js", "package.json"} {
			rel := ".opencode/plugins/rein/" + name
			if err := os.Remove(filepath.Join(wt, rel)); err == nil {
				removed = append(removed, rel)
			} else if !errors.Is(err, os.ErrNotExist) {
				return removed, err
			}
		}
		_ = os.Remove(filepath.Join(wt, ".opencode/plugins/rein"))
		return removed, nil
	}
	rel := rels[vendor]
	path, err := prepare(wt, rel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	doc, err := readJSON(path)
	if err != nil {
		return nil, err
	}
	if vendor == "kiro" {
		if !ownedObject(doc, vendor, task) {
			return nil, fmt.Errorf("foreign or modified Kiro agent; refusing removal")
		}
		return []string{rel}, os.Remove(path)
	}
	before, _ := json.Marshal(doc)
	if vendor == "agy" {
		if value, ok := doc["rein"]; ok {
			if !ownedObject(value, vendor, task) {
				return nil, fmt.Errorf("foreign or modified rein namespace; refusing removal")
			}
			delete(doc, "rein")
		}
		if err := noTaskCommands(doc, vendor, task); err != nil {
			return nil, err
		}
	} else {
		if err := cleanShared(doc, vendor, task); err != nil {
			return nil, err
		}
	}
	after, _ := json.Marshal(doc)
	if string(before) == string(after) {
		return nil, nil
	}
	return []string{rel}, writeJSON(path, doc)
}

// A modified executable hook is a conflict, not proof that this task is uninstalled.
func modifiedCommand(value any, vendor, task string) bool {
	cmd, ok := value.(string)
	if !ok || ownedCommand(cmd, vendor+"\x00"+task) {
		return false
	}
	suffix := " hook --vendor " + vendor + " --task " + quoteWord(task)
	for start := 0; start < len(cmd); start++ {
		if cmd[start] != '\'' {
			continue
		}
		end := strings.Index(cmd[start:], suffix)
		if end < 0 {
			continue
		}
		end += start + len(suffix)
		if _, ok := commandBinary(cmd[start:end], vendor, task); !ok {
			continue
		}
		if end == len(cmd) || strings.ContainsRune(" \t\r\n;|&<>#()", rune(cmd[end])) {
			return true
		}
	}
	return false
}

func walkCommands(value any, visit func(any) error) error {
	switch value := value.(type) {
	case map[string]any:
		if cmd, ok := value["command"]; ok {
			if err := visit(cmd); err != nil {
				return err
			}
		}
		for _, child := range value {
			if err := walkCommands(child, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := walkCommands(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanShared(doc map[string]any, vendor, task string) error {
	if err := walkCommands(doc, func(cmd any) error {
		if modifiedCommand(cmd, vendor, task) {
			return fmt.Errorf("modified %s Rein command for task %s; refusing to change hooks", vendor, task)
		}
		return nil
	}); err != nil {
		return err
	}
	events, _ := doc["hooks"].(map[string]any)
	for event, groups := range events {
		found := false
		_ = walkCommands(groups, func(cmd any) error {
			if ownedCommand(cmd, vendor+"\x00"+task) {
				found = true
			}
			return nil
		})
		if _, ok := groups.([]any); ok && found {
			mergeGroups(doc, event, vendor+"\x00"+task, nil)
		}
	}
	return walkCommands(doc, func(cmd any) error {
		if ownedCommand(cmd, vendor+"\x00"+task) {
			return fmt.Errorf("Rein command outside supported hook group; refusing false uninstall")
		}
		return nil
	})
}

func noTaskCommands(doc map[string]any, vendor, task string) error {
	return walkCommands(doc, func(cmd any) error {
		if ownedCommand(cmd, vendor+"\x00"+task) || modifiedCommand(cmd, vendor, task) {
			return fmt.Errorf("residual Rein command outside owned %s namespace; refusing to change hooks", vendor)
		}
		return nil
	})
}
