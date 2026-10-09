package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func cmdAdvise(args []string) int {
	fs := flag.NewFlagSet("advise", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to run advisor with (codex|kiro; default $ADVISE_PROVIDER or codex)")
	model := fs.String("model", "", "model for advisor (default $ADVISE_MODEL or provider default)")
	timeout := fs.Duration("timeout", 0, "timeout for advisor call")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	posArgs := fs.Args()
	if len(posArgs) < 4 {
		fmt.Fprintln(os.Stderr, "usage: rein advise <role> <repo-or-worktree dir> <task-file> <out-file> [--provider codex|kiro] [--model M]")
		return 2
	}

	role := posArgs[0]
	dir := posArgs[1]
	taskFile := posArgs[2]
	outFile := posArgs[3]

	script := findAdviseScript()
	if script == "" {
		fmt.Fprintln(os.Stderr, "rein advise error: could not locate advise.sh script")
		return 1
	}

	cmd := exec.Command(script, role, dir, taskFile, outFile)
	if *model != "" {
		cmd.Args = append(cmd.Args, *model)
	}
	cmd.Env = os.Environ()
	if *provider != "" {
		cmd.Env = append(cmd.Env, "ADVISE_PROVIDER="+*provider)
	}
	if *timeout > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("ADVISE_TIMEOUT=%d", int(timeout.Seconds())))
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "rein advise error: %v\n", err)
		return 1
	}
	return 0
}

func findAdviseScript() string {
	var candidates []string
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, "skills", "worktree-pipeline", "scripts", "advise.sh"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "skills", "worktree-pipeline", "scripts", "advise.sh"))
		candidates = append(candidates, filepath.Join(cwd, "scripts", "advise.sh"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "skills", "worktree-pipeline", "scripts", "advise.sh"))
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "advise.sh"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "rein", "skills", "worktree-pipeline", "scripts", "advise.sh"))
		candidates = append(candidates, filepath.Join(home, "dev", "rein", "skills", "worktree-pipeline", "scripts", "advise.sh"))
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}
