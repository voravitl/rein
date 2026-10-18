package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/voravitl/rein/internal/supervisor"
)

func cmdSupervisor(args []string) int {
	if len(args) == 0 || (args[0] != "validate" && args[0] != "run") {
		fmt.Fprintln(os.Stderr, "usage: rein supervisor <validate|run> --plan file")
		return 2
	}
	fs := flag.NewFlagSet("supervisor "+args[0], flag.ContinueOnError)
	path := fs.String("plan", "", "explicitly approved supervisor plan")
	if err := fs.Parse(args[1:]); err != nil || *path == "" || fs.NArg() != 0 {
		return 2
	}
	p, hash, err := supervisor.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if p.Schema == 2 {
		if err := validateLocalGraphPlan(p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	if args[0] == "validate" {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"valid": true, "plan_hash": hash, "run": p.Run})
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if p.Schema == 2 {
		result, err := supervisor.RunGraph(ctx, p, hash, graphOperations(p))
		json.NewEncoder(os.Stdout).Encode(result)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	result, err := supervisor.Monitor(ctx, p, hash, nil)
	json.NewEncoder(os.Stdout).Encode(result)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
