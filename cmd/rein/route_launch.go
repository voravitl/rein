package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

// cmdRouteLaunch runs an argv, never shell text, after validating its prepared routing decision.
func cmdRouteLaunch(args []string) int {
	for _, name := range []string{"--task", "--run"} {
		if _, err := launchFlag(args, name); err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 2
		}
	}
	fs := flag.NewFlagSet("route launch", flag.ContinueOnError)
	task := fs.String("task", "", "rein contract name")
	runID := fs.String("run", "", "run identity")
	if fs.Parse(args) != nil || *task == "" || *runID == "" || len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "[route] launch requires --task T --run R -- <provider argv>")
		return 2
	}
	c, err := contract.Load(*task)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	argv := fs.Args()
	agent, model, err := launchIdentity(argv, c, *runID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	d, err := routing.Validate(c, *runID, agent, model)
	if err == nil && !strings.HasPrefix(d.Chain, "worker:") {
		err = fmt.Errorf("implementation launch requires a worker route; use rein advise for read-only review")
	}
	if err == nil && isOrcaLaunch(argv) && d.Launch != "orca" {
		err = fmt.Errorf("selected provider requires the shell route; Orca does not forward its model/guard flags")
	}
	if err == nil && isOrcaLaunch(argv) && (agent == "codex" || agent == "kiro" || agent == "opencode" || agent == "opencode2") {
		err = fmt.Errorf("this harness requires the direct shell route for model and hook flags")
	}
	if err == nil {
		err = routeBudget(c, *runID)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	if !isOrcaLaunch(argv) && agent == "codex" {
		bin, binErr := os.Executable()
		if binErr != nil {
			fmt.Fprintln(os.Stderr, "[route]", binErr)
			return 1
		}
		argv = append([]string{argv[0], "exec"}, append(hooks.CodexArgs(bin, c.Name), argv[2:]...)...)
	}
	if !isOrcaLaunch(argv) && agent == "kiro" {
		selected, _ := launchFlag(argv[1:], "--agent")
		if selected == "" {
			argv = append([]string{argv[0], "chat", "--agent", "rein"}, argv[2:]...)
		}
	}
	// Record before starting: a missing ledger must never leave an untracked model call.
	note, _ := json.Marshal(map[string]any{"provider": d.Provider, "chain": d.Chain, "launch": d.Launch})
	row := ledger.Row{Kind: "routing_launch", Task: c.Name, Run: *runID, Provider: d.Provider,
		Model: model, Role: d.Chain, Notes: string(note)}
	if err := ledger.Append(row); err != nil {
		fmt.Fprintln(os.Stderr, "[route] cannot record launch:", err)
		return 1
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = c.Worktree
	cmd.Env = append(os.Environ(), "ADVISE_RUN="+*runID, "ADVISE_TASK="+c.Name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	started := time.Now()
	err = cmd.Run()
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "[route] provider launch:", err)
	}
	ok, minutes := code == 0, time.Since(started).Minutes()
	row.Kind, row.OK, row.Minutes = "routing_exit", &ok, &minutes
	if err := ledger.Append(row); err != nil {
		fmt.Fprintln(os.Stderr, "[route] cannot record provider exit:", err)
		return 1
	}
	return code
}

func isOrcaLaunch(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	n := filepath.Base(argv[0])
	return n == "orca" || n == "orca-dev" || n == "orca-ide"
}

func routeBudget(c *contract.Contract, runID string) error {
	p, err := contract.LoadProfile("")
	if err != nil {
		return err
	}
	if p.Budget == nil {
		p = &c.Profile
	}
	loc, found := run.Find(c.Worktree)
	if !found {
		if p.Budget != nil {
			return fmt.Errorf("configured budget requires a current Run marker")
		}
		return nil
	}
	m, err := run.Load(loc.Marker)
	if err != nil || m.Run != runID {
		return fmt.Errorf("routing Run differs from current Run marker")
	}
	if !m.Allows("task", c.Name) {
		return fmt.Errorf("routing task is not allowed in current Run")
	}
	if p.Budget == nil {
		return nil
	}
	for _, task := range []string{"", c.Name} {
		result, err := budget.Check(loc.Marker, p, task)
		if err != nil {
			return err
		}
		if result.Code == budget.ExitHard {
			return fmt.Errorf("routing budget denied: %s", result.Message)
		}
	}
	return nil
}

// One explicit value avoids CLI last-value/first-value disagreement at the routing boundary.
func launchFlag(args []string, names ...string) (string, error) {
	value, count := "", 0
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		for _, name := range names {
			if args[i] == name {
				if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
					return "", fmt.Errorf("missing %s value", name)
				}
				count++
				value = args[i+1]
				i++
				break
			}
			if v, found := strings.CutPrefix(args[i], name+"="); found {
				if v == "" {
					return "", fmt.Errorf("missing %s value", name)
				}
				count++
				value = v
			}
		}
	}
	if count > 1 {
		return "", fmt.Errorf("duplicate %s", names[0])
	}
	return value, nil
}

func launchIdentity(argv []string, c *contract.Contract, runID string) (string, string, error) {
	name, args := filepath.Base(argv[0]), argv[1:]
	agent := map[string]string{"claude": "claude", "codex": "codex", "agy": "antigravity", "kiro-cli": "kiro", "opencode": "opencode", "opencode2": "opencode2"}[name]
	if isOrcaLaunch(argv) {
		if len(args) < 2 || args[0] != "orchestration" || args[1] != "worker-start" {
			return "", "", fmt.Errorf("route launch only supports Orca orchestration worker-start or a provider CLI")
		}
		var err error
		agent, err = launchFlag(args, "--agent")
		if err != nil {
			return "", "", err
		}
		r, err := launchFlag(args, "--run")
		if err != nil || r != runID {
			return "", "", fmt.Errorf("Orca --run must match prepared routing Run")
		}
		wt, err := launchFlag(args, "--worktree")
		if err != nil || !strings.HasPrefix(wt, "path:") || !filepath.IsAbs(strings.TrimPrefix(wt, "path:")) || contract.Real(strings.TrimPrefix(wt, "path:")) != contract.Real(c.Worktree) {
			return "", "", fmt.Errorf("Orca --worktree must name the exact contracted path")
		}
	} else {
		if agent == "" {
			return "", "", fmt.Errorf("unsupported provider executable %q", name)
		}
		if (name == "codex" && (len(args) == 0 || args[0] != "exec")) || (name == "kiro-cli" && (len(args) == 0 || args[0] != "chat")) || ((name == "opencode" || name == "opencode2") && (len(args) == 0 || args[0] != "run")) {
			return "", "", fmt.Errorf("unsupported %s launch subcommand", name)
		}
		for _, arg := range args {
			if arg == "--" {
				break
			}
			if arg == "--worktree" || strings.HasPrefix(arg, "--worktree=") || (name == "claude" && strings.HasPrefix(arg, "-w")) || (strings.HasPrefix(arg, "-C") && arg != "-C") {
				return "", "", fmt.Errorf("provider worktree override is outside prepared routing")
			}
		}
		for _, flag := range []string{"-C", "--cd", "--cwd", "--directory"} {
			if dir, err := launchFlag(args, flag); err != nil || (dir != "" && contract.Real(dir) != contract.Real(c.Worktree)) {
				return "", "", fmt.Errorf("provider directory override differs from contracted worktree")
			}
		}
		if name == "codex" {
			for i, arg := range args {
				if arg == "--profile" || arg == "-p" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "-p") || arg == "--oss" || strings.HasPrefix(arg, "--local-provider") {
					return "", "", fmt.Errorf("Codex profile override is outside prepared routing")
				}
				value := ""
				if arg == "-c" || arg == "--config" {
					if i+1 < len(args) {
						value = args[i+1]
					}
				} else if v, ok := strings.CutPrefix(arg, "--config="); ok {
					value = v
				} else if strings.HasPrefix(arg, "-c") && len(arg) > 2 {
					value = arg[2:]
				}
				if value != "" {
					key, _, has := strings.Cut(value, "=")
					key = strings.TrimSpace(key)
					if !has || key == "model" || key == "model_provider" || key == "profile" || strings.HasPrefix(key, "profiles.") || key == "hooks" || strings.HasPrefix(key, "hooks.") {
						return "", "", fmt.Errorf("Codex configuration changes prepared model identity")
					}
				}
			}
		}
		if name == "kiro-cli" {
			selected, err := launchFlag(args, "--agent")
			if err != nil || (selected != "" && selected != "rein") {
				return "", "", fmt.Errorf("Kiro worker agent must be rein")
			}
		}
	}
	model, err := launchFlag(args, "--model", "-m")
	if err != nil || agent == "" || model == "" {
		return "", "", fmt.Errorf("launch requires one explicit agent and model: %v", err)
	}
	return agent, model, nil
}
