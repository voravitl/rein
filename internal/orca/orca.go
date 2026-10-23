// Package orca implements native Go orchestration and worker lifecycle management
// for Orca IDE (https://onorca.dev), incorporating operational learnings and safety gates
// from multi-agent production runs (Claude, Codex, Antigravity, Kiro).
package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Runner abstracts command execution for testability.
type Runner interface {
	Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
}

type defaultRunner struct{}

func (d *defaultRunner) Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

var currentRunner Runner = &defaultRunner{}

var launchToken = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Safety refusal errors are distinct from Git/API failures for CLI exit mapping.
var (
	ErrDirtyWorktree = errors.New("uncommitted changes")
	ErrUniqueCommits = errors.New("unique commits")
)

// SetRunner overrides the command runner (used in tests).
func SetRunner(r Runner) {
	if r == nil {
		currentRunner = &defaultRunner{}
	} else {
		currentRunner = r
	}
}

// LaunchOptions configures launching a worker inside Orca.
type LaunchOptions struct {
	Name        string
	BaseBranch  string
	SpecContent string
	Agent       string // "kiro", "agy" / "antigravity", "claude", "codex"
	Model       string
	Effort      string
	RepoID      string
	RepoDir     string
	RunID       string
	CoordHandle string
	RunDir      string
	AddDirs     []string
}

// LaunchResult holds the IDs and paths created by launch.
type LaunchResult struct {
	Name           string `json:"name"`
	WorktreePath   string `json:"worktree_path"`
	TerminalHandle string `json:"terminal_handle,omitempty"`
	WorkerHandle   string `json:"worker_handle,omitempty"`
	TaskID         string `json:"task_id"`
	DispatchID     string `json:"dispatch_id"`
	Model          string `json:"model"`
	Effort         string `json:"effort,omitempty"`
	Subject        string `json:"subject"`
}

// OrcaSubject composes the Orca terminal/task title so the subject names the
// worker's agent and model outright.
func OrcaSubject(name, agent, model string) string {
	return fmt.Sprintf("rein %s · %s:%s", name, agent, model)
}

// IsExecutableName reports whether name is one of the Orca CLI executables
// (orca, orca-dev, orca-ide). It is the single source of that set.
func IsExecutableName(name string) bool {
	switch name {
	case "orca", "orca-dev", "orca-ide":
		return true
	default:
		return false
	}
}

// Launch starts a worker or reviewer inside Orca on a new worktree.
func Launch(ctx context.Context, opts LaunchOptions) (*LaunchResult, error) {
	if opts.Name == "" {
		return nil, errors.New("name is required")
	}
	if opts.BaseBranch == "" {
		return nil, errors.New("base branch is required")
	}
	if strings.TrimSpace(opts.SpecContent) == "" {
		return nil, errors.New("spec content is missing or empty")
	}
	if opts.RunID == "" {
		return nil, errors.New("ORCA_RUN is required")
	}
	if opts.CoordHandle == "" {
		return nil, errors.New("ORCA_COORD is required: every orchestration call requires explicit coordinator handle")
	}
	if opts.RepoID == "" {
		return nil, errors.New("ORCA_REPO_ID is required")
	}
	if opts.RepoDir == "" {
		return nil, errors.New("ORCA_REPO_DIR is required")
	}

	if !launchToken.MatchString(opts.Agent) {
		return nil, fmt.Errorf("invalid agent %q: use letters, digits, dot, underscore or hyphen", opts.Agent)
	}
	agent := strings.ToLower(opts.Agent)
	model := opts.Model
	effort := opts.Effort
	if effort == "" {
		effort = "high"
	}

	switch effort {
	case "low", "medium", "high", "max":
	default:
		return nil, fmt.Errorf("invalid effort %q (want low, medium, high, max)", effort)
	}

	// Validate agent-specific model constraints
	switch agent {
	case "kiro", "kiro-cli":
		if model == "" {
			model = "claude-sonnet-5.5"
		}
		if model != "claude-sonnet-5.5" && model != "claude-opus-5.5" {
			return nil, fmt.Errorf("model %s is not allowed through Kiro (only claude-sonnet-5.5 or claude-opus-5.5)", model)
		}
	case "agy", "antigravity":
		if model == "" {
			model = "gemini-3.8-flash-high"
		}
	case "claude":
		if model == "" {
			model = "claude-sonnet-5-5"
		}
	case "codex":
		if model == "" {
			model = "gpt-6.1-sol"
		}
	default:
		return nil, fmt.Errorf("unsupported agent %q (want kiro, agy, claude, codex)", opts.Agent)
	}

	if !launchToken.MatchString(model) {
		return nil, fmt.Errorf("invalid model %q: use letters, digits, dot, underscore or hyphen", model)
	}

	subject := OrcaSubject(opts.Name, agent, model)

	unlock, err := legacyController(opts.RepoDir, opts.RunID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// 1. Create worktree
	wtOut, err := currentRunner.Run(ctx, opts.RepoDir, "orca", "worktree", "create",
		"--name", opts.Name,
		"--repo", "id:"+opts.RepoID,
		"--base-branch", opts.BaseBranch,
		"--setup", "skip",
		"--no-parent",
		"--json",
	)
	if err != nil {
		return nil, fmt.Errorf("worktree create failed: %w: %s", err, string(wtOut))
	}
	wtPath, err := extractJSONPath(wtOut, "result.worktree.path")
	if err != nil {
		return nil, fmt.Errorf("extract worktree.path: %w", err)
	}

	if agent == "claude" || agent == "codex" {
		out, err := currentRunner.Run(ctx, "/tmp", "orca", "orchestration", "worker-start",
			"--spec", opts.SpecContent,
			"--worktree", "path:"+wtPath,
			"--agent", agent,
			"--model", model,
			"--effort", effort,
			"--run", opts.RunID,
			"--from", opts.CoordHandle,
			"--task-title", subject,
			"--json",
		)
		if err != nil {
			return nil, fmt.Errorf("start %s worker failed (worktree=%s): %w (output: %s)", agent, wtPath, err, strings.TrimSpace(string(out)))
		}
		var receipt struct {
			OK     bool `json:"ok"`
			Result struct {
				State      string `json:"state"`
				TaskID     string `json:"taskId"`
				DispatchID string `json:"dispatchId"`
				Effects    []struct {
					Kind  string `json:"kind"`
					Role  string `json:"role"`
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"effects"`
			} `json:"result"`
		}
		if err := json.Unmarshal(CleanJSON(out), &receipt); err != nil {
			return nil, fmt.Errorf("parse worker-start receipt (worktree=%s): %w", wtPath, err)
		}
		worker := receipt.Result
		if !receipt.OK || worker.State != "ready" || strings.TrimSpace(worker.TaskID) == "" || strings.TrimSpace(worker.DispatchID) == "" {
			return nil, fmt.Errorf("worker-start did not confirm ready task/dispatch (worktree=%s output=%s)", wtPath, out)
		}
		handle := ""
		for _, effect := range worker.Effects {
			if effect.Kind == "dispatch_input" && effect.Role == "agent" && effect.State == "accepted" {
				handle = effect.ID
			}
		}
		if strings.TrimSpace(handle) == "" {
			return nil, fmt.Errorf("worker-start receipt missing accepted worker handle (dispatch=%s worktree=%s)", worker.DispatchID, wtPath)
		}
		// Structured workers have an assignee handle but legitimately no PTY.
		// Confirm the exact supervised dispatch rather than treating its handle as a terminal.
		shown, err := currentRunner.Run(ctx, "/tmp", "orca", "orchestration", "worker-show", "--dispatch", worker.DispatchID, "--json")
		if err != nil {
			return nil, fmt.Errorf("worker ready but dispatch observation unconfirmed (task=%s dispatch=%s): %w", worker.TaskID, worker.DispatchID, err)
		}
		terminalHandle, err := ValidateWorkerObservation(CleanJSON(shown), opts.RunID, worker.TaskID, worker.DispatchID, handle)
		if err != nil {
			return nil, err
		}

		return &LaunchResult{Name: opts.Name, WorktreePath: wtPath, TerminalHandle: terminalHandle, WorkerHandle: handle,
			TaskID: worker.TaskID, DispatchID: worker.DispatchID, Model: model, Effort: effort, Subject: subject}, nil
	}

	// 2. Create terminal in worktree
	termOut, err := currentRunner.Run(ctx, "/tmp", "orca", "terminal", "create",
		"--worktree", "path:"+wtPath,
		"--title", subject,
		"--json",
	)
	if err != nil {
		return nil, fmt.Errorf("terminal create failed: %w: %s", err, string(termOut))
	}
	termHandle, err := extractJSONPath(termOut, "result.terminal.handle")
	if err != nil {
		return nil, fmt.Errorf("extract terminal.handle: %w", err)
	}

	// 4. Create orchestration task
	taskOut, err := currentRunner.Run(ctx, "/tmp", "orca", "orchestration", "task-create",
		"--spec", opts.SpecContent,
		"--run", opts.RunID,
		"--from", opts.CoordHandle,
		"--json",
	)
	if err != nil {
		return nil, fmt.Errorf("task create failed: %w: %s", err, string(taskOut))
	}
	taskID, err := extractJSONPath(taskOut, "result.task.id")
	if err != nil {
		return nil, fmt.Errorf("extract task.id: %w", err)
	}

	// 5. Dispatch task
	dispOut, err := currentRunner.Run(ctx, "/tmp", "orca", "orchestration", "dispatch",
		"--task", taskID,
		"--to", termHandle,
		"--run", opts.RunID,
		"--from", opts.CoordHandle,
		"--return-preamble",
		"--json",
	)
	if err != nil {
		return nil, fmt.Errorf("dispatch failed: %w: %s", err, string(dispOut))
	}
	dispatchID, err := extractJSONPath(dispOut, "result.dispatch.id")
	if err != nil {
		return nil, fmt.Errorf("extract dispatch.id: %w", err)
	}
	preamble, err := extractJSONPath(dispOut, "result.preamble")
	if err != nil {
		return nil, fmt.Errorf("extract preamble: %w", err)
	}

	// 6. Write preamble to worktree and exclude from git
	taskFilePath := filepath.Join(wtPath, ".orca-task.md")
	taskFile, err := os.OpenFile(taskFilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("write task file: %w", err)
	}
	_, writeErr := taskFile.WriteString(preamble)
	closeErr := taskFile.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, fmt.Errorf("write task file: %w", err)
	}
	excludeTaskFile(ctx, wtPath)

	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// 7. Send agent launch prompt to terminal
	var startOut []byte
	var startErr error
	switch agent {
	case "kiro", "kiro-cli":
		prompt := fmt.Sprintf("Your worktree and current directory is %s . Read the file %s/.orca-task.md and follow it exactly: it is your task and your reporting protocol. Your first command is: git -C %s rev-parse HEAD", wtPath, wtPath, quote(wtPath))
		cmd := fmt.Sprintf("kiro-cli chat --model %s --effort %s --trust-all-tools %s", quote(model), quote(effort), quote(prompt))
		startOut, startErr = currentRunner.Run(ctx, "/tmp", "orca", "terminal", "send", "--terminal", termHandle, "--text", cmd, "--enter", "--json")

	case "agy", "antigravity":
		addDirs := fmt.Sprintf("--add-dir %s", quote(filepath.Join(opts.RepoDir, ".git")))
		if opts.RunDir != "" {
			addDirs += fmt.Sprintf(" --add-dir %s", quote(opts.RunDir))
		}
		for _, d := range opts.AddDirs {
			addDirs += fmt.Sprintf(" --add-dir %s", quote(d))
		}
		prompt := fmt.Sprintf("Your worktree is %s . The shell tool does NOT start there: begin every shell command with cd %s && , and use git -C %s for git. GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM are set to /dev/null because the sandbox blocks ~/.gitconfig. Read the file %s/.orca-task.md and follow it exactly: it is your task and your reporting protocol. Your first command is: cd %s && git rev-parse HEAD", wtPath, quote(wtPath), quote(wtPath), wtPath, quote(wtPath))
		cmd := fmt.Sprintf("cd %s && export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null && agy --model %s --dangerously-skip-permissions %s -i %s", quote(wtPath), quote(model), addDirs, quote(prompt))
		startOut, startErr = currentRunner.Run(ctx, "/tmp", "orca", "terminal", "send", "--terminal", termHandle, "--text", cmd, "--enter", "--json")

	}

	if startErr != nil {
		return nil, fmt.Errorf("start %s worker failed (worktree=%s terminal=%s task=%s dispatch=%s): %w (output: %s)", agent, wtPath, termHandle, taskID, dispatchID, startErr, strings.TrimSpace(string(startOut)))
	}

	return &LaunchResult{
		Name:           opts.Name,
		WorktreePath:   wtPath,
		TerminalHandle: termHandle,
		TaskID:         taskID,
		DispatchID:     dispatchID,
		Model:          model,
		Effort:         effort,
		Subject:        subject,
	}, nil
}

// excludeTaskFile appends .orca-task.md to .git/info/exclude in the common dir.
func excludeTaskFile(ctx context.Context, wtPath string) {
	gcOut, err := currentRunner.Run(ctx, wtPath, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return
	}
	gc := strings.TrimSpace(string(gcOut))
	if !filepath.IsAbs(gc) {
		gc = filepath.Join(wtPath, gc)
	}
	excludeFile := filepath.Join(gc, "info", "exclude")
	b, _ := os.ReadFile(excludeFile)
	if !strings.Contains(string(b), ".orca-task.md") {
		f, err := os.OpenFile(excludeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			defer f.Close()
			_, _ = f.WriteString(".orca-task.md\n")
		}
	}
}

// WaitOptions configures blocking wait on coordinator terminal.
type WaitOptions struct {
	CoordHandle string
	TimeoutMs   int
	MaxRounds   int
	LogDir      string
	Label       string
}

// WaitResult holds the signal message if received.
type WaitResult struct {
	SignalRound int                    `json:"signal_round"`
	MessageType string                 `json:"message_type"`
	Payload     map[string]interface{} `json:"payload,omitempty"`
	Raw         string                 `json:"raw,omitempty"`
}

// Wait loops up to MaxRounds (default 60) waiting for worker_done, escalation, or question.
func Wait(ctx context.Context, opts WaitOptions) (*WaitResult, error) {
	unlock, err := legacyController("", "")
	if err != nil {
		return nil, err
	}
	defer unlock()

	if opts.CoordHandle == "" {
		return nil, errors.New("coordinator handle is required")
	}
	timeoutMs := opts.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 600000 // 10 minutes
	}
	maxRounds := opts.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 60
	}

	for i := 1; i <= maxRounds; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		out, err := currentRunner.Run(ctx, "/tmp", "orca", "orchestration", "check",
			"--terminal", opts.CoordHandle,
			"--wait",
			"--types", "worker_done,escalation,question",
			"--timeout-ms", fmt.Sprintf("%d", timeoutMs),
			"--json",
		)
		outStr := string(out)

		var envelope struct {
			Result struct {
				Messages []struct {
					Type string `json:"type"`
				} `json:"messages"`
			} `json:"result"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		parseErr := json.Unmarshal(CleanJSON(out), &envelope)
		delay := time.Duration(0)
		if parseErr == nil && envelope.Error != nil && envelope.Error.Code == "waiter_exists" {
			delay = 25 * time.Second
		} else if err != nil {
			delay = 2 * time.Second
		} else if parseErr != nil {
			return nil, fmt.Errorf("parse orchestration check: %w", parseErr)
		} else if envelope.Error != nil {
			return nil, fmt.Errorf("orchestration check: %s: %s", envelope.Error.Code, envelope.Error.Message)
		} else {
			for _, sig := range []string{"worker_done", "escalation", "question"} {
				for _, message := range envelope.Result.Messages {
					if message.Type == sig {
						var parsed map[string]interface{}
						if err := json.Unmarshal(CleanJSON(out), &parsed); err != nil {
							return nil, err
						}
						return &WaitResult{SignalRound: i, MessageType: sig, Payload: parsed, Raw: outStr}, nil
					}
				}
			}
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, errors.New("no signal: reached max rounds without worker_done/question/escalation")
}

// DeliveryMessage holds parsed details of an orchestration delivery.
type DeliveryMessage struct {
	Type       string `json:"type"`
	Subject    string `json:"subject,omitempty"`
	FromHandle string `json:"from_handle,omitempty"`
	Payload    string `json:"payload,omitempty"`
	Body       string `json:"body,omitempty"`
}

// DrainResult summarizes drained messages and heartbeats.
type DrainResult struct {
	Heartbeats int               `json:"heartbeats"`
	Messages   []DeliveryMessage `json:"messages"`
}

// Drain reads and acknowledges all pending deliveries on the coordinator terminal.
func Drain(ctx context.Context, coordHandle string, ack bool) (*DrainResult, error) {
	unlock, err := legacyController("", "")
	if err != nil {
		return nil, err
	}
	defer unlock()

	if coordHandle == "" {
		return nil, errors.New("coordinator handle is required")
	}

	result := &DrainResult{
		Messages: []DeliveryMessage{},
	}

	var did string
	for round := 0; round < 25; round++ {
		args := []string{"orchestration", "check", "--terminal", coordHandle, "--json"}
		if ack && did != "" {
			args = append(args, "--ack", did)
		}
		out, err := currentRunner.Run(ctx, "/tmp", "orca", args...)
		if err != nil {
			if round == 0 {
				return nil, fmt.Errorf("drain check: %w (output: %s)", err, strings.TrimSpace(string(out)))
			}
			return result, fmt.Errorf("partial drain: check failed at round %d: %w", round+1, err)
		}

		var resp struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Result *struct {
				DeliveryID string `json:"deliveryId"`
				Messages   []struct {
					Type       string `json:"type"`
					Subject    string `json:"subject"`
					FromHandle string `json:"from_handle"`
					Payload    string `json:"payload"`
					Body       string `json:"body"`
				} `json:"messages"`
			} `json:"result"`
		}
		if err := json.Unmarshal(CleanJSON(out), &resp); err != nil {
			if round == 0 {
				return nil, fmt.Errorf("parse drain check: %w", err)
			}
			return result, fmt.Errorf("partial drain: parse check at round %d: %w", round+1, err)
		}

		if resp.Error != nil || resp.Result == nil {
			return result, fmt.Errorf("partial drain: invalid check result at round %d: %s", round+1, strings.TrimSpace(string(out)))
		}
		if len(resp.Result.Messages) == 0 {
			return result, nil
		}

		for _, m := range resp.Result.Messages {
			if m.Type == "heartbeat" {
				result.Heartbeats++
			} else {
				result.Messages = append(result.Messages, DeliveryMessage{
					Type:       m.Type,
					Subject:    m.Subject,
					FromHandle: m.FromHandle,
					Payload:    m.Payload,
					Body:       m.Body,
				})
			}
		}

		did = resp.Result.DeliveryID
		if !ack {
			return result, nil
		}
		if did == "" {
			return result, errors.New("partial drain: messages have no delivery ID")
		}
	}

	return result, errors.New("partial drain: reached 25 delivery limit; pending delivery remains unacknowledged")
}

// CloseOptions holds options for closing a worker worktree safely.
type CloseOptions struct {
	WorktreePath   string
	TerminalHandle string
	DispatchID     string
	CheckOnly      bool
}

// CloseResult summarizes the result of closing a worker.
type CloseResult struct {
	Safe           bool   `json:"safe"`
	WorktreeGone   bool   `json:"worktree_gone"`
	TerminalClosed bool   `json:"terminal_closed"`
	ReleaseState   string `json:"release_state,omitempty"`
	RefusalReason  string `json:"refusal_reason,omitempty"`
}

// Close closes a finished worker with mandatory safety checks.
func Close(ctx context.Context, opts CloseOptions) (*CloseResult, error) {
	unlock, err := legacyController(opts.WorktreePath, "")
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if coord := os.Getenv("ORCA_COORD"); coord != "" && opts.TerminalHandle == coord {
		return nil, errors.New("refusing to close coordinator terminal")
	}
	if opts.WorktreePath == "" {
		return nil, errors.New("worktree path is required")
	}

	res := &CloseResult{}

	// 1. Safety check: uncommitted changes
	if _, err := os.Stat(opts.WorktreePath); err != nil && !os.IsNotExist(err) {
		return res, fmt.Errorf("stat worktree %s: %w", opts.WorktreePath, err)
	} else if err == nil {
		statusOut, err := currentRunner.Run(ctx, opts.WorktreePath, "git", "status", "--porcelain")
		if err != nil {
			return res, fmt.Errorf("git status failed in %s: %w (%s)", opts.WorktreePath, err, strings.TrimSpace(string(statusOut)))
		}
		dirty := strings.TrimSpace(string(statusOut))
		if dirty != "" {
			res.RefusalReason = fmt.Sprintf("REFUSED: uncommitted changes in %s:\n%s", opts.WorktreePath, dirty)
			return res, fmt.Errorf("%w: %s", ErrDirtyWorktree, res.RefusalReason)
		}

		// 2. Safety check: unique commits
		brOut, err := currentRunner.Run(ctx, opts.WorktreePath, "git", "branch", "--show-current")
		if err != nil {
			return res, fmt.Errorf("git branch failed in %s: %w (%s)", opts.WorktreePath, err, strings.TrimSpace(string(brOut)))
		}
		branch := strings.TrimSpace(string(brOut))
		refsOut, err := currentRunner.Run(ctx, opts.WorktreePath, "git", "for-each-ref",
			"--contains", "HEAD",
			"--format=%(refname)",
			"refs/heads", "refs/remotes", "refs/tags", "refs/keep",
		)
		if err != nil {
			return res, fmt.Errorf("git for-each-ref failed in %s: %w (%s)", opts.WorktreePath, err, strings.TrimSpace(string(refsOut)))
		}
		refs := strings.Split(strings.TrimSpace(string(refsOut)), "\n")
		hasOther := false
		for _, r := range refs {
			r = strings.TrimSpace(r)
			if strings.HasPrefix(r, "refs/") && r != "refs/heads/"+branch {
				hasOther = true
				break
			}
		}
		if !hasOther {
			headOut, err := currentRunner.Run(ctx, opts.WorktreePath, "git", "rev-parse", "--short", "HEAD")
			if err != nil {
				return res, fmt.Errorf("git rev-parse failed in %s: %w (%s)", opts.WorktreePath, err, strings.TrimSpace(string(headOut)))
			}
			res.RefusalReason = fmt.Sprintf("REFUSED: HEAD %s of branch %q is contained in no other ref (unique commits). Create keep ref first: git branch keep/<name> <sha>", strings.TrimSpace(string(headOut)), branch)
			return res, fmt.Errorf("%w: %s", ErrUniqueCommits, res.RefusalReason)
		}
	}

	res.Safe = true
	if opts.CheckOnly {
		return res, nil
	}

	// 3. Release dispatch
	if opts.DispatchID != "" {
		relOut, err := currentRunner.Run(ctx, "/tmp", "orca", "orchestration", "worker-release", "--dispatch", opts.DispatchID, "--json")
		if err != nil {
			return res, fmt.Errorf("worker-release: %w (output: %s)", err, strings.TrimSpace(string(relOut)))
		}
		var relResp struct {
			OK     *bool           `json:"ok"`
			Error  json.RawMessage `json:"error"`
			Result struct {
				State  string `json:"state"`
				Reason string `json:"reason"`
			} `json:"result"`
		}
		if err := json.Unmarshal(CleanJSON(relOut), &relResp); err != nil {
			return res, fmt.Errorf("parse worker-release: %w", err)
		}
		if (relResp.OK != nil && !*relResp.OK) || (len(relResp.Error) != 0 && string(relResp.Error) != "null") {
			return res, fmt.Errorf("worker-release error response: %s", strings.TrimSpace(string(relOut)))
		}
		res.ReleaseState = relResp.Result.State
		if res.ReleaseState != "released" && res.ReleaseState != "already_released" {
			return res, fmt.Errorf("worker-release did not confirm closure: state=%q reason=%s", res.ReleaseState, relResp.Result.Reason)
		}
		res.TerminalClosed = true
	}

	// 4. Close terminal
	if opts.TerminalHandle != "" && opts.DispatchID == "" {
		out, err := currentRunner.Run(ctx, "/tmp", "orca", "terminal", "close", "--terminal", opts.TerminalHandle, "--tab", "--json")
		if err != nil {
			return res, fmt.Errorf("terminal close: %w (output: %s)", err, strings.TrimSpace(string(out)))
		}
		res.TerminalClosed = true
	}

	// A worktree removal must not implicitly tear down an unrelated terminal.
	out, err := currentRunner.Run(ctx, "/tmp", "orca", "terminal", "list", "--worktree", "path:"+opts.WorktreePath, "--json")
	if err != nil {
		return res, fmt.Errorf("list remaining terminals: %w", err)
	}
	var inventory struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Terminals  *[]json.RawMessage `json:"terminals"`
			Truncated  *bool              `json:"truncated"`
			TotalCount *int               `json:"totalCount"`
			HostScope  struct {
				HostIDs        []string  `json:"hostIds"`
				OmittedHostIDs *[]string `json:"omittedHostIds"`
			} `json:"hostScope"`
		} `json:"result"`
	}
	if err := json.Unmarshal(CleanJSON(out), &inventory); err != nil {
		return res, fmt.Errorf("parse remaining terminals: %w", err)
	}
	r := inventory.Result
	local := false
	for _, host := range r.HostScope.HostIDs {
		local = local || host == "local"
	}
	if (inventory.OK != nil && !*inventory.OK) || (len(inventory.Error) != 0 && string(inventory.Error) != "null") ||
		r.Terminals == nil || r.Truncated == nil || *r.Truncated || r.TotalCount == nil || *r.TotalCount != 0 ||
		len(*r.Terminals) != 0 || !local || r.HostScope.OmittedHostIDs == nil || len(*r.HostScope.OmittedHostIDs) != 0 {
		return res, errors.New("refusing worktree removal: complete empty terminal inventory not proven")
	}

	// 6. Remove worktree
	if out, err := currentRunner.Run(ctx, "/tmp", "orca", "worktree", "rm", "--worktree", "path:"+opts.WorktreePath, "--json"); err != nil {
		return res, fmt.Errorf("worktree rm: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	// Orca removes worktrees asynchronously; never report success before disappearance.
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if _, err := os.Stat(opts.WorktreePath); os.IsNotExist(err) {
			res.WorktreeGone = true
			return res, nil
		} else if err != nil {
			return res, fmt.Errorf("verify worktree removal: %w", err)
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-deadline.C:
			return res, fmt.Errorf("timed out waiting for worktree removal: %s", opts.WorktreePath)
		case <-tick.C:
		}
	}
}

// TidyOptions holds options for auditing/cleaning Orca sessions.
type TidyOptions struct {
	RepoDir       string
	WorkspacesDir string
	CoordWorktree string
	Apply         bool
}

// TerminalItem represents an audited terminal.
type TerminalItem struct {
	Handle   string `json:"handle"`
	Title    string `json:"title"`
	Worktree string `json:"worktree"`
	Category string `json:"category"` // "foreign", "shell_candidate", "active_worker"
}

// TidyResult summarizes the audit.
type TidyResult struct {
	Terminals []TerminalItem `json:"terminals"`
	Worktrees []string       `json:"worktrees"`
	Closed    []string       `json:"closed,omitempty"`
}

// Tidy audits Orca terminals; titles alone cannot prove that a shell is idle.
func Tidy(ctx context.Context, opts TidyOptions) (*TidyResult, error) {
	if opts.WorkspacesDir == "" {
		home, _ := os.UserHomeDir()
		opts.WorkspacesDir = filepath.Join(home, "orca", "workspaces")
	}

	out, err := currentRunner.Run(ctx, "/tmp", "orca", "terminal", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("list terminals: %w", err)
	}

	var resp struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Truncated bool `json:"truncated"`
			Terminals []struct {
				Handle       string `json:"handle"`
				Title        string `json:"title"`
				WorktreePath string `json:"worktreePath"`
			} `json:"terminals"`
		} `json:"result"`
	}
	if err := json.Unmarshal(CleanJSON(out), &resp); err != nil {
		return nil, fmt.Errorf("parse terminals: %w", err)
	}

	if (resp.OK != nil && !*resp.OK) || (len(resp.Error) != 0 && string(resp.Error) != "null") || resp.Result.Terminals == nil || resp.Result.Truncated {
		return nil, errors.New("incomplete or failed terminal inventory")
	}

	wsp := filepath.Clean(opts.WorkspacesDir) + string(filepath.Separator)
	res := &TidyResult{
		Terminals: []TerminalItem{},
		Closed:    []string{},
	}

	for _, t := range resp.Result.Terminals {
		item := TerminalItem{
			Handle:   t.Handle,
			Title:    t.Title,
			Worktree: t.WorktreePath,
		}
		wt := filepath.Clean(t.WorktreePath)
		mine := t.Handle != "" && t.Handle != os.Getenv("ORCA_COORD") && strings.HasPrefix(wt, wsp) && (opts.CoordWorktree == "" || filepath.Base(wt) != opts.CoordWorktree)

		if !mine {
			item.Category = "foreign"
		} else if strings.HasPrefix(t.Title, "~/") || strings.HasPrefix(t.Title, "/") {
			item.Category = "shell_candidate"
			// No CLI idle/ownership proof is available; preserve the terminal even with Apply.
		} else {
			item.Category = "active_worker"
		}
		res.Terminals = append(res.Terminals, item)
	}

	// Git worktrees check
	if opts.RepoDir != "" {
		wtOut, err := currentRunner.Run(ctx, opts.RepoDir, "git", "worktree", "list")
		if err != nil {
			return res, fmt.Errorf("list git worktrees: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(string(wtOut)), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, opts.RepoDir+" ") {
				res.Worktrees = append(res.Worktrees, l)
			}
		}
	}

	return res, nil
}

// CleanJSON extracts the outermost valid JSON object or array from raw output bytes,
// stripping any leading/trailing process logs or error messages (e.g. Chromium/Electron noise).
func CleanJSON(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if json.Valid(trimmed) {
		return trimmed
	}

	// Try finding the line that begins valid JSON
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		tl := bytes.TrimSpace(line)
		if len(tl) > 0 && (tl[0] == '{' || tl[0] == '[') {
			joined := bytes.Join(lines[i:], []byte("\n"))
			endObj := bytes.LastIndexByte(joined, '}')
			endArr := bytes.LastIndexByte(joined, ']')
			end := endObj
			if endArr > end {
				end = endArr
			}
			if end >= 0 {
				candidate := joined[:end+1]
				if json.Valid(candidate) {
					return candidate
				}
			}
		}
	}

	// Fallback to first '{'
	startObj := bytes.IndexByte(data, '{')
	if startObj != -1 {
		endObj := bytes.LastIndexByte(data, '}')
		if endObj >= startObj {
			candidate := data[startObj : endObj+1]
			if json.Valid(candidate) {
				return candidate
			}
		}
	}

	return data
}

// extractJSONPath parses dot-separated fields from a JSON response.
func extractJSONPath(data []byte, path string) (string, error) {
	data = CleanJSON(data)
	var root interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("invalid json: %w", err)
	}
	parts := strings.Split(path, ".")
	curr := root
	for _, p := range parts {
		m, ok := curr.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("expected map at %q, got %T", p, curr)
		}
		val, exists := m[p]
		if !exists {
			return "", fmt.Errorf("key %q not found in json", p)
		}
		curr = val
	}
	v, ok := curr.(string)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("expected nonempty string at %q, got %T", path, curr)
	}
	return v, nil
}

// PrettyJSON formats an object as indented JSON bytes.
func PrettyJSON(v interface{}) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return buf.Bytes()
}
