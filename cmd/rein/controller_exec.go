package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/run"
)

// cmdControllerExec holds legacy authority through one bounded Orca check/ACK.
func cmdControllerExec(args []string) int {
	repo, rest := splitRepo(args)
	fs := flag.NewFlagSet("run controller-exec", flag.ContinueOnError)
	runID := fs.String("run", "", "legacy run")
	if fs.Parse(rest) != nil || !run.ValidName(*runID) {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return 2
	}
	// Only the waiter check protocol is supported, never arbitrary commands.
	flags := map[string]bool{"--types": true, "--ack": true, "--terminal": true, "--timeout-ms": true, "--wait": false, "--peek": false}
	seen := map[string]bool{}
	for i := 0; i < len(argv); i++ {
		value, ok := flags[argv[i]]
		if !ok || seen[argv[i]] {
			return 2
		}
		seen[argv[i]] = true
		if value {
			i++
			if i == len(argv) || argv[i] == "" || strings.HasPrefix(argv[i], "-") {
				return 2
			}
			if argv[i-1] == "--timeout-ms" {
				n, err := strconv.Atoi(argv[i])
				if err != nil || n < 1 || n > 60000 {
					return 2
				}
			}
		}
	}
	unlock, err := run.AcquireLegacyController(repo, *runID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer unlock()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeoutCancel := context.WithTimeout(ctx, 62*time.Second)
	defer timeoutCancel()
	cmd := exec.CommandContext(ctx, "orca", append([]string{"orchestration", "check", "--run", *runID, "--json"}, argv...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = repo, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
