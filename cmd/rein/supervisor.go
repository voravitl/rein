package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/voravitl/rein/internal/supervisor"
)

func cmdSupervisor(args []string) int {
	if len(args) > 0 && args[0] == "invocation" {
		return cmdGraphInvocation(args[1:])
	}
	if len(args) == 0 || (args[0] != "validate" && args[0] != "run" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "usage: rein supervisor <validate|run|status> --plan file [--json] [--watch[=2s]]")
		return 2
	}
	fs := flag.NewFlagSet("supervisor "+args[0], flag.ContinueOnError)
	path := fs.String("plan", "", "explicitly approved supervisor plan")
	var asJSON bool
	watch := supervisorWatchFlag{interval: 2 * time.Second}
	if args[0] == "status" {
		fs.BoolVar(&asJSON, "json", false, "render status as JSON")
		fs.Var(&watch, "watch", "refresh status (default 2s; minimum 100ms)")
	}
	if err := fs.Parse(args[1:]); err != nil || *path == "" || fs.NArg() != 0 {
		return 2
	}
	p, hash, err := supervisor.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if args[0] == "status" && p.Schema != 3 {
		fmt.Fprintln(os.Stderr, "supervisor status requires schema 3")
		return 2
	}
	if p.Schema == 2 {
		if err := validateLocalGraphPlan(p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	if p.Schema == 3 {
		if err := validateLocalDAGPlan(p); err != nil {
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
	if args[0] == "status" {
		return supervisorStatusLoop(ctx, os.Stdout, os.Stderr, supervisorStatusOptions{
			json: asJSON, watch: watch.enabled, interval: watch.interval, terminal: supervisorStatusTerminal(os.Stdout),
		}, func(ctx context.Context) (supervisor.DAGStatus, error) {
			return supervisor.ReadDAGStatus(ctx, p, hash)
		}, waitSupervisorStatus)
	}
	if p.Schema == 3 {
		result, err := supervisor.RunDAG(ctx, p, hash, dagOperations(p, hash))
		json.NewEncoder(os.Stdout).Encode(result)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
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

type supervisorWatchFlag struct {
	enabled  bool
	interval time.Duration
}

func (f *supervisorWatchFlag) String() string {
	return f.interval.String()
}

func (f *supervisorWatchFlag) IsBoolFlag() bool {
	return true
}

func (f *supervisorWatchFlag) Set(value string) error {
	interval := 2 * time.Second
	if value != "true" {
		var err error
		interval, err = time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid watch duration: %w", err)
		}
	}
	if interval < 100*time.Millisecond {
		return fmt.Errorf("watch duration must be at least 100ms")
	}
	f.enabled, f.interval = true, interval
	return nil
}

type supervisorStatusOptions struct {
	json     bool
	watch    bool
	terminal bool
	interval time.Duration
}

func waitSupervisorStatus(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Read and wait are injected only by Go callers; no CLI flag can bypass polling
// or supply a fabricated frame. The production path always observes the journal.
func supervisorStatusLoop(ctx context.Context, out, stderr io.Writer, options supervisorStatusOptions, read func(context.Context) (supervisor.DAGStatus, error), wait func(context.Context, time.Duration) error) int {
	for frame := 0; ; frame++ {
		if ctx.Err() != nil {
			return 130
		}
		view, err := read(ctx)
		if ctx.Err() != nil {
			return 130
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if options.watch {
			if options.terminal {
				_, err = fmt.Fprint(out, "\x1b[H\x1b[2J")
			} else if frame > 0 {
				_, err = fmt.Fprintln(out)
			}
		}
		if err == nil {
			if options.json {
				err = json.NewEncoder(out).Encode(view)
			} else {
				err = writeSupervisorStatusTable(out, view)
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !options.watch || view.Terminal() {
			return 0
		}
		if err := wait(ctx, options.interval); err != nil {
			if ctx.Err() != nil {
				return 130
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
}

func writeSupervisorStatusTable(out io.Writer, view supervisor.DAGStatus) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Run %s  %s\n", statusTableText(view.Run), statusTableText(view.Status))
	fmt.Fprintln(w, "TASK\tSTATE\tKIND\tLAYER\tATTEMPT\tAGENT/MODEL\tELAPSED\tTOKENS\tGUARD")
	for _, n := range view.Nodes {
		model := "\u2014"
		if n.Agent != nil && n.Model != nil {
			model = *n.Agent + "/" + *n.Model
			if n.Effort != nil && *n.Effort != "" {
				model += ":" + *n.Effort
			}
		}
		elapsed := "\u2014"
		if n.ElapsedMS != nil {
			elapsed = (time.Duration(*n.ElapsedMS) * time.Millisecond).String()
		}
		guard := "\u2014"
		if n.GuardLine != nil {
			guard = *n.GuardLine
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d/%d\t%s\t%s\t\u2014\t%s\n", statusTableText(n.Task), statusTableText(n.State), statusTableText(n.Kind), n.Layer, n.Attempt, n.MaxAttempts, statusTableText(model), elapsed, statusTableText(guard))
	}
	return w.Flush()
}

func statusTableText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || (r >= 127 && r <= 159) {
			return ' '
		}
		return r
	}, value)
}

func supervisorStatusTerminal(out *os.File) bool {
	info, err := out.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// Confirm a known terminal device, rather than treating every character
	// device (including /dev/null) as a TTY. Unknown platforms use plain frames.
	for _, dir := range []string{"/dev/pts", "/dev"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if dir == "/dev" && !strings.HasPrefix(name, "tty") && !strings.HasPrefix(name, "pty") && name != "console" {
				continue
			}
			device, err := os.Stat(filepath.Join(dir, name))
			if err == nil && device.Mode()&os.ModeCharDevice != 0 && os.SameFile(info, device) {
				return true
			}
		}
	}
	return false
}
