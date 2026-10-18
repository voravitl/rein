package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/voravitl/rein/internal/routing"
)

func cmdCheckpoint(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rein checkpoint <finalize|verify|restore> --task T --attempt A [options]")
		return 2
	}
	action := args[0]
	if action != "finalize" && action != "verify" && action != "restore" {
		fmt.Fprintln(os.Stderr, "unknown checkpoint action:", action)
		return 2
	}
	fs := flag.NewFlagSet("checkpoint "+action, flag.ContinueOnError)
	task := fs.String("task", "", "task identity")
	attempt := fs.String("attempt", "", "attempt identity")
	var run, wt, base, hash, untracked, dest string
	if action == "finalize" {
		fs.StringVar(&run, "run", "", "run identity")
		fs.StringVar(&wt, "worktree", "", "source worktree")
		fs.StringVar(&base, "base", "origin/main", "base commit or ref")
		fs.StringVar(&hash, "contract-hash", "", "contract digest")
		fs.StringVar(&untracked, "untracked", "", "comma-separated allowed untracked paths")
	}
	if action == "restore" {
		fs.StringVar(&dest, "dest", "", "new destination directory")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *task == "" || *attempt == "" || (action == "finalize" && (run == "" || wt == "" || hash == "")) || (action == "restore" && dest == "") {
		fmt.Fprintln(os.Stderr, "checkpoint: required identity or destination missing, or unexpected arguments")
		return 2
	}
	var err error
	switch action {
	case "finalize":
		var paths []string
		if untracked != "" {
			paths = strings.Split(untracked, ",")
		}
		var cp *routing.Checkpoint
		cp, err = routing.FinalizeCheckpoint(*task, *attempt, run, wt, base, paths, hash)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(map[string]any{"task": cp.Task, "attempt": cp.Attempt, "run": cp.Run, "head_sha": cp.HeadSHA, "base_sha": cp.BaseSHA, "worktree": cp.Worktree, "contract_hash": cp.ContractHash, "complete": cp.Complete})
		}
	case "verify":
		err = routing.VerifyCheckpoint(*task, *attempt)
	case "restore":
		err = routing.RestoreCheckpoint(*task, *attempt, dest)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkpoint:", err)
		return 1
	}
	return 0
}
