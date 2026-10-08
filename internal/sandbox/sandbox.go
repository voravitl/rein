// Package sandbox writes the Claude Code OS sandbox settings into a worker's worktree before the worker starts.
// The sandbox is the wall (the OS enforces it on every shell command and its children); the guard hook is the
// seatbelt. Claude Code's sandbox covers Bash only, runs on macOS, Linux and WSL2, and not on native Windows.
package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/voravitl/rein/internal/contract"
)

const settingsRel = ".claude/settings.local.json"

// Settings returns the sandbox block for a contract.
func Settings(c *contract.Contract) map[string]any {
	allowWrite := append([]string{c.ReportPath}, c.WorkerWritable...)
	sb := map[string]any{
		"enabled":                  true,
		"allowUnsandboxedCommands": false, // no dangerouslyDisableSandbox retries
		"filesystem":               map[string]any{"allowWrite": allowWrite},
	}
	p := c.Profile
	if len(p.SandboxExcludedCommands) > 0 {
		sb["excludedCommands"] = p.SandboxExcludedCommands
	}
	if len(p.SandboxAllowedDomains) > 0 {
		sb["network"] = map[string]any{"allowedDomains": p.SandboxAllowedDomains}
	}
	if p.SandboxFailIfUnavailable {
		sb["failIfUnavailable"] = true
	}
	return sb
}

// Apply merges the sandbox block into <worktree>/.claude/settings.local.json (other keys are kept) and keeps that
// file out of git status via the repository's info/exclude. It returns the file path and a platform note.
func Apply(c *contract.Contract) (string, string, error) {
	wt := c.Worktree
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("worktree %s does not exist yet: create it first (orca worktree create --name %s ...), then run rein sandbox, then start the worker on it", wt, c.Name)
	}
	path := filepath.Join(wt, filepath.FromSlash(settingsRel))
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return "", "", fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	doc["sandbox"] = Settings(c)
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return "", "", err
	}
	if err := Exclude(wt, settingsRel); err != nil {
		return path, "", fmt.Errorf("settings written, but could not add %s to info/exclude: %w", settingsRel, err)
	}
	note := ""
	if runtime.GOOS == "windows" {
		note = "native Windows: Claude Code runs commands unsandboxed; use WSL2 for the sandbox. The guard hook and drift check still apply."
	}
	return path, note, nil
}

// Exclude adds a line to <git common dir>/info/exclude once (shared by all worktrees of the repository).
func Exclude(wt, line string) error {
	out, err := exec.Command("git", "-C", wt, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return err
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(wt, common)
	}
	ex := filepath.Join(common, "info", "exclude")
	b, _ := os.ReadFile(ex)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(ex, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		line = "\n" + line
	}
	_, err = f.WriteString(line + "\n")
	return err
}
