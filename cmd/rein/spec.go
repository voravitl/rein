package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/spec"
)

// cmdSpec implements `rein spec check <spec> <contract> [--standing PATH] [--repo PATH]`.
// Exit codes: 0 = pass, 1 = lint failures, 2 = file/arg error.
func cmdSpec(args []string) int {
	fs := flag.NewFlagSet("spec", flag.ExitOnError)
	standingPath := fs.String("standing", "", "path to standing.md file (optional)")
	_ = fs.String("repo", "", "repository path (optional, for future use)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: rein spec check <spec> <contract> [--standing PATH] [--repo PATH]")
		fmt.Fprintln(os.Stderr, "  <spec>      path to spec file or spec content")
		fmt.Fprintln(os.Stderr, "  <contract>  task name or path to contract JSON")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Exits 0 if all checks pass, 1 if lint failures, 2 on file/arg error")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Must have subcommand "check"
	if fs.NArg() < 1 || fs.Arg(0) != "check" {
		fs.Usage()
		return 2
	}

	// Must have spec and contract arguments
	if fs.NArg() < 3 {
		fs.Usage()
		return 2
	}

	specArg := fs.Arg(1)
	contractArg := fs.Arg(2)

	// Load contract
	c, err := contract.Load(contractArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[spec] ERROR: cannot load contract: %v\n", err)
		return 2
	}

	// Run spec check
	result, warnings := spec.Check(specArg, c, *standingPath)

	// Print warnings if any
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "[spec] WARNING: %s\n", w)
	}

	// Check for violations
	if len(result.Violations) == 0 {
		fmt.Printf("[spec] OK: %s (tier: %s)\n", specArg, result.Tier)
		return 0
	}

	// Print violations
	fmt.Fprintf(os.Stderr, "[spec] LINT FAILED:\n")
	for _, v := range result.Violations {
		fmt.Fprintf(os.Stderr, "  - %s\n", v)
	}

	return 1
}
