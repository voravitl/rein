package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/spec"
)

// loadContract loads a contract by task name or file path.
// If contractArg looks like a file path (ends with .json, contains /, or exists on disk),
// it uses contract.LoadFile. Otherwise it tries contract.Load (task name),
// then falls back to contract.LoadFile if the file exists.
func loadContract(contractArg string) (*contract.Contract, error) {
	// Check if it looks like a file path
	looksLikeFile := strings.HasSuffix(contractArg, ".json") || strings.Contains(contractArg, "/")

	// If it looks like a file, try LoadFile first
	if looksLikeFile {
		if c, err := contract.LoadFile(contractArg); err == nil {
			return c, nil
		} else if !os.IsNotExist(err) {
			// Real error, not just "file doesn't exist"
			return nil, err
		}
	}

	// Try as task name
	c, err := contract.Load(contractArg)
	if err == nil {
		return c, nil
	}

	// If task name failed and file exists, try LoadFile as fallback
	if _, statErr := os.Stat(contractArg); statErr == nil {
		return contract.LoadFile(contractArg)
	}

	// Return the original error from contract.Load
	return nil, err
}

// cmdSpec implements `rein spec check <spec> <contract> [--standing PATH] [--repo PATH]`.
// Exit codes: 0 = pass, 1 = lint failures, 2 = file/arg error.
func cmdSpec(args []string) int {
	usage := func() {
		fmt.Fprintln(os.Stderr, "usage: rein spec check <spec> <contract> [--standing PATH] [--repo PATH]")
		fmt.Fprintln(os.Stderr, "  <spec>      path to spec file or spec content")
		fmt.Fprintln(os.Stderr, "  <contract>  task name or path to contract JSON")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Exits 0 if all checks pass, 1 if lint failures, 2 on file/arg error")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  --standing PATH  path to standing.md file (optional)")
		fmt.Fprintln(os.Stderr, "  --repo PATH      repository path (optional, for future use)")
	}

	// Must have subcommand "check"
	if len(args) == 0 || args[0] != "check" {
		usage()
		return 2
	}

	// Parse flags and positionals manually to support flags anywhere
	var standingPath, repoPath string
	var positionals []string

	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--standing" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --standing requires a value")
				usage()
				return 2
			}
			standingPath = args[i+1]
			i++ // skip the value
		} else if arg == "--repo" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --repo requires a value")
				usage()
				return 2
			}
			repoPath = args[i+1]
			i++ // skip the value
		} else {
			positionals = append(positionals, arg)
		}
	}

	// Avoid unused variable warning
	_ = repoPath

	// Must have spec and contract arguments
	if len(positionals) < 2 {
		usage()
		return 2
	}

	specArg := positionals[0]
	contractArg := positionals[1]

	// Load contract: try as task name first, then as file path
	c, err := loadContract(contractArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[spec] ERROR: cannot load contract: %v\n", err)
		return 2
	}

	// Run spec check
	result, warnings := spec.Check(specArg, c, standingPath)

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
