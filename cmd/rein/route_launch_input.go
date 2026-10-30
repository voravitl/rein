package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

// graphEditPermission is the one edit-permission flag and value each harness may
// carry in an approved graph argv, so a print-mode worker can write its worktree.
var graphEditPermission = map[string]struct{ flag, value string }{
	"claude": {"--permission-mode", "acceptEdits"},
	"codex":  {"--sandbox", "workspace-write"},
	"agy":    {"--mode", "accept-edits"},
}

// Only literal-prompt forms are accepted; unknown options could change input or resume semantics.
func graphPromptSlot(argv []string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("graph requires a provider argv")
	}
	name := filepath.Base(argv[0])
	if name == "hermes" {
		return hermesPromptSlot(argv)
	}
	start, printMode := 1, false
	standalone := false
	switch name {
	case "codex", "opencode", "opencode2":
		want := "exec"
		if name == "opencode" || name == "opencode2" {
			want = "run"
		}
		if len(argv) < 2 || argv[1] != want {
			return 0, fmt.Errorf("graph requires %s %s", name, want)
		}
		start, printMode = 2, true
	case "claude", "agy":
	default:
		return 0, fmt.Errorf("graph literal prompt unsupported for %s", name)
	}
	slot := -1
	selectionFlags := map[string]bool{}
	permissionFlag := false
	for i := start; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			if i+2 != len(argv) || slot != -1 {
				return 0, fmt.Errorf("graph requires exactly one literal prompt")
			}
			slot = i + 1
			break
		}
		if name == "claude" && (arg == "-p" || arg == "--print") {
			printMode = true
			continue
		}
		// agy's -p, --print and --prompt all take the NEXT argument as the
		// prompt; a flag after a bare -p would be read as the prompt itself.
		if name == "agy" && (arg == "-p" || arg == "--print" || arg == "--prompt") {
			if i+1 >= len(argv) || slot != -1 {
				return 0, fmt.Errorf("graph requires one explicit prompt")
			}
			i++
			slot = i
			printMode = true
			continue
		}
		if (name == "opencode" || name == "opencode2") && arg == "--standalone" {
			standalone = true
			continue
		}
		// Agent/plugin selection belongs to the approved plan argv. It neither
		// changes provider identity nor grants permissions or trust. Keep these
		// flags harness-specific so another CLI cannot reinterpret their values.
		selectionFlag := ((name == "claude" || name == "agy") && arg == "--agent") || (name == "claude" && arg == "--plugin-dir")
		if selectionFlag {
			if selectionFlags[arg] || i+1 >= len(argv) {
				return 0, fmt.Errorf("graph requires one explicit %s value", arg)
			}
			value := argv[i+1]
			if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "-") || strings.ContainsRune(value, 0) {
				return 0, fmt.Errorf("invalid graph %s value", arg)
			}
			if arg == "--plugin-dir" && (!filepath.IsAbs(value) || filepath.Clean(value) != value) {
				return 0, fmt.Errorf("graph plugin directory must be absolute and clean")
			}
			if arg == "--agent" && strings.ContainsAny(value, " \t\r\n") {
				return 0, fmt.Errorf("graph agent name must be a single nonempty name")
			}
			selectionFlags[arg] = true
			i++
			continue
		}
		valueFlag := arg == "--model" || arg == "-m" || arg == "-C" || arg == "--cd" || arg == "--cwd" || arg == "--directory"
		// Edit permission for a noninteractive worker: only the mode that lets
		// the CLI edit files without a prompt while every tool call still goes
		// through the Rein hook. Bypass, auto and full-access modes stay refused.
		if editMode, ok := graphEditPermission[name]; ok && arg == editMode.flag {
			if permissionFlag || i+1 >= len(argv) || argv[i+1] != editMode.value {
				return 0, fmt.Errorf("graph %s accepts only %s %s", name, editMode.flag, editMode.value)
			}
			permissionFlag = true
			i++
			continue
		}
		if (name == "claude" || name == "agy") && arg == "--effort" {
			valueFlag = true
		}
		if name == "codex" && (arg == "-c" || arg == "--config") {
			valueFlag = true
		}
		if valueFlag {
			if i+1 >= len(argv) {
				return 0, fmt.Errorf("missing graph option value %s", arg)
			}
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return 0, fmt.Errorf("unsupported graph literal-prompt option %q", arg)
		}
		if slot != -1 {
			return 0, fmt.Errorf("graph requires exactly one literal prompt")
		}
		slot = i
	}
	if !printMode || ((name == "opencode" || name == "opencode2") && !standalone) || slot < 0 || strings.TrimSpace(argv[slot]) == "" || argv[slot] == "-" || strings.ContainsRune(argv[slot], 0) {
		return 0, fmt.Errorf("graph requires noninteractive literal prompt")
	}
	return slot, nil
}

func graphLaunchInput(control *run.Controller, p supervisor.Plan, t supervisor.Task, d *routing.Decision, argv []string, receipt graphHandoffRef) ([]string, string, error) {
	slot, err := graphPromptSlot(argv)
	if err != nil {
		return nil, "", err
	}
	c, err := contract.Load(t.Task)
	if err != nil {
		return nil, "", err
	}
	if err = graphLaunchAuthority(control, c, p.Run); err != nil {
		return nil, "", err
	}
	if d == nil || d.Attempt == "" || d.Task != t.Task || d.Run != p.Run || d.Worktree != c.Worktree || d.ContractHash != t.ContractHash || routing.ContractHash(c) != t.ContractHash {
		return nil, "", fmt.Errorf("graph input identity differs from current approved task")
	}
	metadata := struct {
		Run          string     `json:"run"`
		Task         string     `json:"task"`
		Attempt      string     `json:"attempt"`
		Worktree     string     `json:"worktree"`
		ContractHash string     `json:"contract_hash"`
		Allow        []string   `json:"allow"`
		Deny         []string   `json:"deny"`
		Verify       [][]string `json:"verify"`
	}{p.Run, t.Task, d.Attempt, c.Worktree, t.ContractHash, c.Allow, c.Deny, t.Verify}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", err
	}
	input := "Rein approved task context (does not grant additional permissions):\n" + string(raw) + "\n\nUser-facing communication: default to natural, grammatically correct, concise Thai. Lead with the essential result; ask clear, specific questions. Use a table or diagram when it makes relationships or choices easier to understand. Preserve exact commands and errors. Follow an explicit user request for another language within its stated scope.\n"
	original := argv[slot]
	if receipt.Path != "" {
		handoff, readErr := readGraphHandoff(receipt, p.Run, t)
		if readErr != nil {
			return nil, "", readErr
		}
		b, err := json.Marshal(handoff)
		if err != nil {
			return nil, "", err
		}
		original = handoff.Objective
		input += "\nVerified continuation receipt (preserve partial work; this is not a new authorization):\n" + string(b) + "\n"
	}
	input += "\nOriginal approved task prompt (verbatim):\n" + original
	out := append([]string(nil), argv...)
	out[slot] = input
	return out, graphHash([]byte(input)), nil
}

// Keep Hermes on its classic one-query path; other flags can re-home the runtime or bypass its plugin.
func hermesPromptSlot(argv []string) (int, error) {
	if len(argv) < 3 || argv[1] != "--cli" || argv[2] != "chat" {
		return 0, fmt.Errorf("Hermes launch requires --cli chat --query PROMPT --oneshot")
	}
	slot, oneshot := -1, false
	seen := map[string]bool{}
	for i := 3; i < len(argv); i++ {
		arg := argv[i]
		key := arg
		switch arg {
		case "-m":
			key = "--model"
		case "-q":
			key = "--query"
		case "-Q":
			key = "--quiet"
		}
		if seen[key] {
			return 0, fmt.Errorf("duplicate Hermes option %s", key)
		}
		seen[key] = true
		switch key {
		case "--oneshot":
			oneshot = true
		case "--quiet":
		case "--model", "--reasoning", "--query":
			if i+1 >= len(argv) || strings.TrimSpace(argv[i+1]) == "" || strings.HasPrefix(argv[i+1], "-") || strings.ContainsRune(argv[i+1], 0) {
				return 0, fmt.Errorf("missing or invalid Hermes option value %s", key)
			}
			i++
			if key == "--model" {
				provider, model, qualified := strings.Cut(argv[i], ":")
				if !qualified || strings.TrimSpace(provider) != provider || provider == "" || strings.TrimSpace(model) != model || model == "" {
					return 0, fmt.Errorf("Hermes launch requires a provider-qualified model")
				}
			}
			if key == "--query" {
				slot = i
			}
			if key == "--reasoning" {
				switch strings.ToLower(argv[i]) {
				case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
				default:
					return 0, fmt.Errorf("invalid Hermes reasoning effort %q", argv[i])
				}
			}
		default:
			return 0, fmt.Errorf("unsupported Hermes launch option %q", arg)
		}
	}
	if !oneshot || slot < 0 || !seen["--model"] {
		return 0, fmt.Errorf("Hermes launch requires one model and noninteractive literal query")
	}
	return slot, nil
}
