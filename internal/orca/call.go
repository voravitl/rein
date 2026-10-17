package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrInvalidRequest identifies malformed requests and protected terminal targets.
var ErrInvalidRequest = errors.New("invalid orca call request")

// CallRequest represents a structured tool-call invocation for Orca.
type CallRequest struct {
	Action string                 `json:"action"` // "status", "launch", "wait", "drain", "close", "tidy", "send", "read", "terminals", "worktrees"
	Args   map[string]interface{} `json:"args,omitempty"`
}

// CallResponse represents the structured JSON output returned to an agent or caller.
type CallResponse struct {
	OK     bool        `json:"ok"`
	Action string      `json:"action"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// StatusResult summarizes the status of the Orca daemon.
type StatusResult struct {
	Running   bool        `json:"running"`
	State     string      `json:"state"`
	Reachable bool        `json:"reachable"`
	RuntimeID string      `json:"runtime_id,omitempty"`
	AppPID    int         `json:"app_pid,omitempty"`
	Raw       interface{} `json:"raw,omitempty"`
	Terminal  interface{} `json:"terminal,omitempty"`
}

// Call executes the requested Orca action with the provided arguments.
func Call(ctx context.Context, req CallRequest) (*CallResponse, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		err := fmt.Errorf("%w: missing action in call request", ErrInvalidRequest)
		return &CallResponse{OK: false, Action: "", Error: err.Error()}, err
	}

	args := req.Args
	if args == nil {
		args = make(map[string]interface{})
	}

	if action == "close" || action == "send" || action == "read" {
		terminal, _ := args["terminal"].(string)
		dispatch, _ := args["dispatch"].(string)
		var reason string
		if strings.TrimSpace(terminal) == "" && (action != "close" || strings.TrimSpace(dispatch) == "") {
			reason = "--terminal is required for " + action
		} else if coord := os.Getenv("ORCA_COORD"); coord != "" && terminal == coord {
			reason = "refusing operation on coordinator terminal ($ORCA_COORD)"
		}
		if reason != "" {
			err := fmt.Errorf("%w: %s", ErrInvalidRequest, reason)
			return &CallResponse{OK: false, Action: action, Error: err.Error()}, err
		}
	}

	var res interface{}
	var err error

	switch action {
	case "status":
		res, err = executeStatus(ctx, args)
	case "launch":
		res, err = executeLaunch(ctx, args)
	case "wait":
		res, err = executeWait(ctx, args)
	case "drain":
		res, err = executeDrain(ctx, args)
	case "close":
		res, err = executeClose(ctx, args)
	case "tidy":
		res, err = executeTidy(ctx, args)
	case "send":
		res, err = executeSend(ctx, args)
	case "read":
		res, err = executeRead(ctx, args)
	case "terminals":
		res, err = executeTerminals(ctx, args)
	case "worktrees":
		res, err = executeWorktrees(ctx, args)
	default:
		err = fmt.Errorf("%w: unknown action %q; supported actions: status, launch, wait, drain, close, tidy, send, read, terminals, worktrees", ErrInvalidRequest, action)
	}

	if err != nil {
		return &CallResponse{
			OK:     false,
			Action: action,
			Result: res,
			Error:  err.Error(),
		}, err
	}

	return &CallResponse{
		OK:     true,
		Action: action,
		Result: res,
	}, nil
}

// CallFromJSON unmarshals a JSON request payload and invokes Call.
func CallFromJSON(ctx context.Context, data []byte) (*CallResponse, error) {
	data = CleanJSON(data)
	var req CallRequest
	if err := json.Unmarshal(data, &req); err != nil {
		resp := &CallResponse{
			OK:     false,
			Action: "",
			Error:  fmt.Sprintf("invalid json payload: %v", err),
		}
		return resp, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return Call(ctx, req)
}

func executeStatus(ctx context.Context, args map[string]interface{}) (*StatusResult, error) {
	out, err := currentRunner.Run(ctx, "/tmp", "orca", "status", "--json")
	if err != nil {
		return &StatusResult{
			Running:   false,
			State:     "error",
			Reachable: false,
			Raw:       string(out),
		}, fmt.Errorf("orca status failed: %w (output: %s)", err, string(out))
	}

	cleaned := CleanJSON(out)
	var parsed struct {
		Result struct {
			App struct {
				Running bool `json:"running"`
				PID     int  `json:"pid"`
			} `json:"app"`
			Runtime struct {
				State     string `json:"state"`
				Reachable bool   `json:"reachable"`
				RuntimeID string `json:"runtimeId"`
			} `json:"runtime"`
		} `json:"result"`
	}

	res := &StatusResult{
		Running: false,
		State:   "unknown",
	}

	if jsonErr := json.Unmarshal(cleaned, &parsed); jsonErr == nil {
		res.Running = parsed.Result.App.Running
		res.State = parsed.Result.Runtime.State
		res.Reachable = parsed.Result.Runtime.Reachable
		res.RuntimeID = parsed.Result.Runtime.RuntimeID
		res.AppPID = parsed.Result.App.PID
	} else {
		res.Raw = string(cleaned)
	}

	terminal := getString(args, "terminal", "")
	if terminal == "" {
		terminal = os.Getenv("ORCA_COORD")
	}
	if terminal != "" {
		out, err := currentRunner.Run(ctx, "/tmp", "orca", "terminal", "show", "--terminal", terminal, "--json")
		if err != nil {
			return res, fmt.Errorf("terminal status: %w (output: %s)", err, out)
		}
		if err := json.Unmarshal(CleanJSON(out), &res.Terminal); err != nil {
			return res, fmt.Errorf("terminal status response: %w", err)
		}
	}
	return res, nil
}

func executeLaunch(ctx context.Context, args map[string]interface{}) (*LaunchResult, error) {
	name := getString(args, "name", "")
	base := getString(args, "base", getString(args, "base_branch", ""))
	agent := getString(args, "agent", "claude")
	model := getString(args, "model", "")
	effort := getString(args, "effort", "high")
	runID := getString(args, "run", getString(args, "run_id", os.Getenv("ORCA_RUN")))
	coord := getString(args, "coord", getString(args, "coord_handle", os.Getenv("ORCA_COORD")))
	repoID := getString(args, "repo", getString(args, "repo_id", os.Getenv("ORCA_REPO_ID")))
	repoDir := getString(args, "repo_dir", os.Getenv("ORCA_REPO_DIR"))

	specContent := getString(args, "spec_content", getString(args, "spec", ""))
	specFile := getString(args, "spec_file", "")

	if specContent == "" && specFile != "" {
		b, err := os.ReadFile(specFile)
		if err != nil {
			return nil, fmt.Errorf("read spec_file %q: %w", specFile, err)
		}
		specContent = string(b)
	} else if specContent != "" && !strings.Contains(specContent, "\n") && fileExists(specContent) {
		// If specContent is just a path to an existing file
		b, err := os.ReadFile(specContent)
		if err == nil {
			specContent = string(b)
		}
	}

	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}

	opts := LaunchOptions{
		Name:        name,
		BaseBranch:  base,
		SpecContent: specContent,
		Agent:       agent,
		Model:       model,
		Effort:      effort,
		RepoID:      repoID,
		RepoDir:     repoDir,
		RunID:       runID,
		CoordHandle: coord,
	}

	return Launch(ctx, opts)
}

func executeWait(ctx context.Context, args map[string]interface{}) (*WaitResult, error) {
	coord := getString(args, "terminal", getString(args, "coord", os.Getenv("ORCA_COORD")))
	timeoutMs := getInt(args, "timeout_ms", 60000)
	rounds := getInt(args, "rounds", 60)

	opts := WaitOptions{
		CoordHandle: coord,
		TimeoutMs:   timeoutMs,
		MaxRounds:   rounds,
	}
	return Wait(ctx, opts)
}

func executeDrain(ctx context.Context, args map[string]interface{}) (*DrainResult, error) {
	coord := getString(args, "terminal", getString(args, "coord", os.Getenv("ORCA_COORD")))
	ack := getBool(args, "ack", true)
	if _, hasNoAck := args["no_ack"]; hasNoAck {
		ack = !getBool(args, "no_ack", false)
	}
	return Drain(ctx, coord, ack)
}

func executeClose(ctx context.Context, args map[string]interface{}) (*CloseResult, error) {
	wt := getString(args, "worktree", "")
	if wt == "" {
		return nil, fmt.Errorf("%w: worktree path is required", ErrInvalidRequest)
	}
	terminal := getString(args, "terminal", "")
	dispatch := getString(args, "dispatch", "")
	checkOnly := getBool(args, "check_only", false)

	opts := CloseOptions{
		WorktreePath:   wt,
		TerminalHandle: terminal,
		DispatchID:     dispatch,
		CheckOnly:      checkOnly,
	}
	return Close(ctx, opts)
}

func executeTidy(ctx context.Context, args map[string]interface{}) (*TidyResult, error) {
	apply := getBool(args, "apply", false)
	workspaces := getString(args, "workspaces", os.Getenv("ORCA_WORKSPACES"))
	coordWt := getString(args, "coord_wt", os.Getenv("ORCA_COORD_WT"))
	repoDir := getString(args, "repo_dir", os.Getenv("ORCA_REPO_DIR"))

	opts := TidyOptions{
		WorkspacesDir: workspaces,
		CoordWorktree: coordWt,
		RepoDir:       repoDir,
		Apply:         apply,
	}
	return Tidy(ctx, opts)
}

func executeSend(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	unlock, err := legacyController(getString(args, "repo_dir", ""), getString(args, "run", ""))
	if err != nil {
		return nil, err
	}
	defer unlock()

	terminal := getString(args, "terminal", "")
	text := getString(args, "text", "")
	if terminal == "" {
		return nil, errors.New("terminal handle is required for send")
	}

	cmdArgs := []string{"terminal", "send", "--terminal", terminal, "--text", text}
	if getBool(args, "enter", false) {
		cmdArgs = append(cmdArgs, "--enter")
	}
	if getBool(args, "interrupt", false) {
		cmdArgs = append(cmdArgs, "--interrupt")
	}
	if ws := getInt(args, "wait_submit", 0); ws > 0 {
		cmdArgs = append(cmdArgs, "--wait-submit", strconv.Itoa(ws))
	}
	cmdArgs = append(cmdArgs, "--json")

	out, err := currentRunner.Run(ctx, "/tmp", "orca", cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("terminal send: %w (output: %s)", err, string(out))
	}

	var parsed interface{}
	if err := json.Unmarshal(CleanJSON(out), &parsed); err != nil {
		return map[string]interface{}{"raw": string(out)}, nil
	}
	return parsed, nil
}

func executeRead(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	terminal := getString(args, "terminal", "")
	if terminal == "" {
		return nil, errors.New("terminal handle is required for read")
	}

	cmdArgs := []string{"terminal", "read", "--terminal", terminal}
	if cursor := getInt(args, "cursor", -1); cursor >= 0 {
		cmdArgs = append(cmdArgs, "--cursor", strconv.Itoa(cursor))
	}
	if limit := getInt(args, "limit", 50); limit > 0 {
		cmdArgs = append(cmdArgs, "--limit", strconv.Itoa(limit))
	}
	cmdArgs = append(cmdArgs, "--json")

	out, err := currentRunner.Run(ctx, "/tmp", "orca", cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("terminal read: %w (output: %s)", err, string(out))
	}

	var parsed interface{}
	if err := json.Unmarshal(CleanJSON(out), &parsed); err != nil {
		return map[string]interface{}{"raw": string(out)}, nil
	}
	return parsed, nil
}

func executeTerminals(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	cmdArgs := []string{"terminal", "list"}
	if wt := getString(args, "worktree", ""); wt != "" {
		cmdArgs = append(cmdArgs, "--worktree", wt)
	}
	if limit := getInt(args, "limit", 0); limit > 0 {
		cmdArgs = append(cmdArgs, "--limit", strconv.Itoa(limit))
	}
	cmdArgs = append(cmdArgs, "--json")

	out, err := currentRunner.Run(ctx, "/tmp", "orca", cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("terminal list: %w (output: %s)", err, string(out))
	}

	var parsed interface{}
	if err := json.Unmarshal(CleanJSON(out), &parsed); err != nil {
		return map[string]interface{}{"raw": string(out)}, nil
	}
	return parsed, nil
}

func executeWorktrees(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	cmdArgs := []string{"worktree", "ps"}
	if limit := getInt(args, "limit", 0); limit > 0 {
		cmdArgs = append(cmdArgs, "--limit", strconv.Itoa(limit))
	}
	cmdArgs = append(cmdArgs, "--json")

	out, err := currentRunner.Run(ctx, "/tmp", "orca", cmdArgs...)
	if err == nil {
		var parsed interface{}
		if err := json.Unmarshal(CleanJSON(out), &parsed); err == nil {
			return parsed, nil
		}
	}

	// Fallback to git worktree list
	repoDir := getString(args, "repo_dir", os.Getenv("ORCA_REPO_DIR"))
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	gitOut, gitErr := currentRunner.Run(ctx, repoDir, "git", "worktree", "list")
	if gitErr != nil {
		return nil, fmt.Errorf("worktree list failed: %w", gitErr)
	}
	lines := strings.Split(strings.TrimSpace(string(gitOut)), "\n")
	return map[string]interface{}{"worktrees": lines}, nil
}

// ToolSchema returns the official JSON schema describing Orca call tool capabilities.
func ToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"name":        "orca_call",
		"description": "Deterministic tool for executing Orca multi-agent orchestration lifecycle operations (status, launch, wait, drain, close, tidy, send, read, terminals, worktrees).",
		"type":        "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"status", "launch", "wait", "drain", "close", "tidy", "send", "read", "terminals", "worktrees",
				},
				"description": "The Orca orchestration action to execute",
			},
			"args": map[string]interface{}{
				"type":        "object",
				"description": "Arguments specific to the action",
				"properties": map[string]interface{}{
					"terminal":     map[string]interface{}{"type": "string", "description": "Terminal handle or coordinator handle"},
					"worktree":     map[string]interface{}{"type": "string", "description": "Worktree path or selector"},
					"name":         map[string]interface{}{"type": "string", "description": "Task / branch name for launch"},
					"base":         map[string]interface{}{"type": "string", "description": "Base branch for worktree"},
					"spec_file":    map[string]interface{}{"type": "string", "description": "Path to spec markdown file"},
					"spec_content": map[string]interface{}{"type": "string", "description": "Inline task spec content"},
					"agent":        map[string]interface{}{"type": "string", "description": "Target agent runner (claude, codex, agy, kiro)"},
					"model":        map[string]interface{}{"type": "string", "description": "Model override"},
					"effort":       map[string]interface{}{"type": "string", "description": "Reasoning effort (low, medium, high, max)"},
					"run":          map[string]interface{}{"type": "string", "description": "ORCA_RUN run container ID"},
					"coord":        map[string]interface{}{"type": "string", "description": "ORCA_COORD coordinator terminal handle"},
					"text":         map[string]interface{}{"type": "string", "description": "Text to send to terminal"},
					"enter":        map[string]interface{}{"type": "boolean", "description": "Append enter after sending text"},
					"interrupt":    map[string]interface{}{"type": "boolean", "description": "Send interrupt before text"},
					"timeout_ms":   map[string]interface{}{"type": "integer", "description": "Timeout in milliseconds"},
					"rounds":       map[string]interface{}{"type": "integer", "description": "Check rounds count"},
					"cursor":       map[string]interface{}{"type": "integer", "description": "Cursor offset for read"},
					"limit":        map[string]interface{}{"type": "integer", "description": "Result limit"},
					"ack":          map[string]interface{}{"type": "boolean", "description": "Acknowledge message deliveries in drain"},
					"apply":        map[string]interface{}{"type": "boolean", "description": "Apply destructive cleanup changes in tidy"},
					"check_only":   map[string]interface{}{"type": "boolean", "description": "Safety check only without mutating in close"},
				},
			},
		},
		"required": []string{"action"},
	}
}

// ToolSchemaJSON returns the ToolSchema formatted as indented JSON bytes.
func ToolSchemaJSON() []byte {
	return PrettyJSON(ToolSchema())
}

func getString(m map[string]interface{}, key string, def string) string {
	if v, ok := m[key]; ok && v != nil {
		switch s := v.(type) {
		case string:
			return s
		case fmt.Stringer:
			return s.String()
		default:
			return fmt.Sprintf("%v", s)
		}
	}
	return def
}

func getInt(m map[string]interface{}, key string, def int) int {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case string:
			if parsed, err := strconv.Atoi(n); err == nil {
				return parsed
			}
		}
	}
	return def
}

func getBool(m map[string]interface{}, key string, def bool) bool {
	if v, ok := m[key]; ok && v != nil {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			b = strings.ToLower(strings.TrimSpace(b))
			if b == "true" || b == "1" || b == "yes" {
				return true
			}
			if b == "false" || b == "0" || b == "no" {
				return false
			}
		case int:
			return b != 0
		}
	}
	return def
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
