package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/voravitl/rein/internal/supervisor"
)

// cmdGraphInvocation reads execution telemetry. It does not acquire controller
// authority, launch work, signal processes, or infer descendant termination.
func cmdGraphInvocation(args []string) int {
	fs := flag.NewFlagSet("supervisor invocation", flag.ContinueOnError)
	path := fs.String("plan", "", "exact supervisor plan")
	id := fs.String("invocation", "", "exact recorded invocation ID")
	if err := fs.Parse(args); err != nil || *path == "" || *id == "" || fs.NArg() != 0 {
		return 2
	}
	p, hash, err := supervisor.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	result, err := supervisor.ReadGraphInvocation(p, hash, *id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
