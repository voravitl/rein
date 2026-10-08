package guard

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Action is what a vendor's hook event means, normalised so one judge (decide) serves every vendor.
type Action struct {
	Event      string   // "pretool" | "stop" | "" (an event the guard does not judge)
	Kind       string   // "bash" | "write" | "other"
	Tool       string   // the vendor's tool name, for the seen log and messages
	Command    string   // shell command (Kind bash)
	Paths      []string // files the tool writes (Kind write); relative paths are relative to Cwd
	Cwd        string   // directory the tool runs in
	Anchor     string   // directory that identifies the worker's worktree; "" means Cwd
	StopActive bool     // this stop already continued once (let it end)
	DenyCoord  bool     // PreToolUse(Agent) starting the pipeline coordinator as a Claude subagent: denied in every session, contract or not
}

// Vendors are the values accepted by `rein hook --vendor`.
var Vendors = []string{"claude", "codex", "agy", "kiro", "opencode"}

// coordAgents are the coordinator agents the plugin must never start as Claude subagents: the coordinator runs in
// the main session through Orca orchestration, and steward jobs start as Orca workers (ADR 0002 B0).
var coordAgents = map[string]bool{"rein:orca-swarm": true, "rein:orca-steward": true}

const coordAgentReject = "rein:orca-swarm / rein:orca-steward must not run as Claude subagents: run the coordinator " +
	"in the main session with Orca orchestration (orca orchestration run-create -> worker-start -> check --wait) " +
	"and start steward jobs as Orca workers"

// editTools are Claude Code's file-writing tools; codex uses the same names for its Edit/Write aliases.
var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// parse turns a vendor's stdin into an Action; ok is false when the input is not an event of that vendor.
func parse(vendor string, raw []byte) (Action, bool) {
	switch vendor {
	case "claude":
		return parseClaude(raw, false)
	case "codex":
		return parseClaude(raw, true) // codex speaks Claude's format and adds apply_patch
	case "agy":
		return parseAgy(raw)
	case "kiro":
		return parseKiro(raw)
	case "opencode":
		return parseOpencode(raw)
	}
	return Action{}, false
}

// str reads the first non-empty string value among keys.
func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// patchText joins the string fields a patch may arrive in (codex puts it under command, others under input/patch).
func patchText(in map[string]any) string {
	var b []string
	for _, k := range []string{"command", "input", "patch", "patchText"} {
		if v, ok := in[k].(string); ok {
			b = append(b, v)
		}
	}
	return strings.Join(b, "\n")
}

// withWorkdir applies a tool's own workdir argument as the command's cwd. The original cwd stays the anchor that
// identifies the worker, so a workdir pointing at another checkout cannot hide the call from the guard.
func withWorkdir(a *Action, in map[string]any) {
	wd := str(in, "workdir")
	if wd == "" {
		return
	}
	a.Anchor = a.Cwd
	if !filepath.IsAbs(wd) && a.Cwd != "" {
		wd = filepath.Join(a.Cwd, wd)
	}
	a.Cwd = wd
}

func object(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

func parseClaude(raw []byte, codex bool) (Action, bool) {
	var ev struct {
		HookEventName  string          `json:"hook_event_name"`
		ToolName       string          `json:"tool_name"`
		Cwd            string          `json:"cwd"`
		ToolInput      json.RawMessage `json:"tool_input"`
		StopHookActive bool            `json:"stop_hook_active"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return Action{}, false
	}
	a := Action{Tool: ev.ToolName, Cwd: ev.Cwd, StopActive: ev.StopHookActive, Kind: "other"}
	switch ev.HookEventName {
	case "Stop":
		a.Event = "stop"
		return a, true
	case "PreToolUse":
		a.Event = "pretool"
	default:
		return a, true
	}
	in := object(ev.ToolInput)
	if codex {
		withWorkdir(&a, in)
	}
	// A PreToolUse(Agent) that starts the pipeline coordinator (or its steward) as a Claude subagent bypasses Orca
	// orchestration entirely, so it is denied before any contract lookup, in every session (ADR 0002 B0).
	if !codex && ev.ToolName == "Agent" && coordAgents[str(in, "subagent_type")] {
		a.DenyCoord = true
	}
	switch {
	case ev.ToolName == "Bash":
		a.Kind, a.Command = "bash", str(in, "command")
	case codex && (editTools[ev.ToolName] || ev.ToolName == "apply_patch"):
		// a known writer is a write even when its target is unreadable: decide then denies it
		a.Kind = "write"
		if p := str(in, "file_path", "notebook_path", "path"); p != "" {
			a.Paths = []string{p}
		}
		a.Paths = append(a.Paths, patchPaths(patchText(in))...)
	case editTools[ev.ToolName]:
		if p := str(in, "file_path", "notebook_path"); p != "" {
			a.Kind, a.Paths = "write", []string{p}
		}
	}
	return a, true
}

// agyWriteTools are agy's file-writing tools: write_to_file creates, replace_file_content edits one block,
// multi_replace_file_content edits several.
var agyWriteTools = map[string]bool{"write_to_file": true, "replace_file_content": true, "multi_replace_file_content": true}

func parseAgy(raw []byte) (Action, bool) {
	var ev struct {
		ToolCall *struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"toolCall"`
		TerminationReason string   `json:"terminationReason"`
		WorkspacePaths    []string `json:"workspacePaths"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return Action{}, false
	}
	a := Action{Kind: "other"}
	if len(ev.WorkspacePaths) > 0 {
		a.Anchor = ev.WorkspacePaths[0] // the worktree agy was started in, wherever the command runs
	}
	if ev.ToolCall == nil { // Stop has no toolCall
		if ev.TerminationReason == "" {
			return Action{}, false
		}
		a.Event, a.Cwd = "stop", a.Anchor
		return a, true
	}
	a.Event, a.Tool = "pretool", ev.ToolCall.Name
	args := ev.ToolCall.Args
	target := str(args, "TargetFile", "AbsolutePath", "FilePath", "Path", "File")
	switch {
	case a.Tool == "run_command":
		a.Kind, a.Command, a.Cwd = "bash", str(args, "CommandLine"), str(args, "Cwd")
	case agyWriteTools[a.Tool] || (target != "" && !agyReadOnly(a.Tool)):
		// a tool with a path-like argument writes it unless it is known to only read, so a write tool we have
		// not seen yet is still judged
		a.Kind = "write"
		if target != "" {
			a.Paths = []string{target}
		}
	}
	if a.Cwd == "" && target != "" {
		a.Cwd = filepath.Dir(target)
	}
	if a.Cwd == "" {
		a.Cwd = a.Anchor
	}
	return a, true
}

// agyReadOnly lists the tools that take a path only to read it (step types are lowercased by agy).
func agyReadOnly(name string) bool {
	switch name {
	case "view_file", "grep_search", "list_dir", "find_by_name", "read_url_content", "code_search":
		return true
	}
	for _, p := range []string{"view_", "read_", "list_", "find_", "grep_", "search_"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func parseKiro(raw []byte) (Action, bool) {
	var ev struct {
		HookEventName string          `json:"hook_event_name"`
		Cwd           string          `json:"cwd"`
		ToolName      string          `json:"tool_name"`
		ToolInput     json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return Action{}, false
	}
	a := Action{Tool: ev.ToolName, Cwd: ev.Cwd, Kind: "other"}
	switch ev.HookEventName {
	case "stop":
		a.Event = "stop"
		return a, true
	case "preToolUse":
		a.Event = "pretool"
	default:
		return a, true
	}
	in := object(ev.ToolInput)
	switch ev.ToolName {
	case "execute_bash", "shell":
		a.Kind, a.Command = "bash", str(in, "command")
	case "fs_write", "write": // every fs_write command (create, str_replace, insert, append) carries path
		a.Kind = "write"
		if p := str(in, "path"); p != "" {
			a.Paths = []string{p}
		}
	}
	return a, true
}

// opencodeWriteTools write files; patch tools carry the paths inside the patch text.
var opencodeWriteTools = map[string]bool{"edit": true, "write": true, "multiedit": true, "patch": true, "apply_patch": true}

// parseOpencode reads what the rein plugin pipes: the tool, its input and the worktree dir (the plugin adds cwd).
func parseOpencode(raw []byte) (Action, bool) {
	var ev struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
		Cwd   string          `json:"cwd"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Tool == "" {
		return Action{}, false
	}
	a := Action{Event: "pretool", Tool: ev.Tool, Cwd: ev.Cwd, Kind: "other"}
	in := object(ev.Input)
	withWorkdir(&a, in)
	switch {
	case ev.Tool == "shell" || ev.Tool == "bash":
		a.Kind, a.Command = "bash", str(in, "command")
	case opencodeWriteTools[ev.Tool]:
		a.Kind = "write"
		if p := str(in, "path", "filePath", "file_path"); p != "" {
			a.Paths = []string{p}
		}
		for _, v := range in { // patch tools: any string field holding a patch
			if s, ok := v.(string); ok && strings.Contains(s, patchMarker) {
				a.Paths = append(a.Paths, patchPaths(s)...)
			}
		}
	}
	return a, true
}
