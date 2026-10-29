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
	"strconv"
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
		fmt.Fprintln(os.Stderr, "usage: rein supervisor <validate|run|status> --plan file [--json] [--view table|graph|statusline|tui] [--watch[=2s]] [--seq N] [--lines N]")
		return 2
	}
	fs := flag.NewFlagSet("supervisor "+args[0], flag.ContinueOnError)
	path := fs.String("plan", "", "explicitly approved supervisor plan")
	var asJSON bool
	watch := supervisorWatchFlag{interval: 2 * time.Second}
	view, seqText, linesText := "table", "", ""
	if args[0] == "status" {
		fs.BoolVar(&asJSON, "json", false, "render status as JSON")
		fs.Var(&watch, "watch", "refresh status (default 2s; minimum 100ms)")
		fs.StringVar(&view, "view", "table", "render status as table, graph, statusline or tui")
		fs.StringVar(&seqText, "seq", "", "render the graph from complete journal entries 1..N")
		fs.StringVar(&linesText, "lines", "", "statusline height cap (default 4)")
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	// Only the statusline may take its plan from REIN_PLAN instead of --plan.
	statusline := args[0] == "status" && view == "statusline"
	if *path == "" && !statusline {
		return 2
	}
	seqSet, linesSet := false, false
	fs.Visit(func(f *flag.Flag) {
		seqSet = seqSet || f.Name == "seq"
		linesSet = linesSet || f.Name == "lines"
	})
	seq, err := supervisorStatusViewOptions(view, asJSON, watch.enabled, seqSet, seqText)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	lines, err := supervisorStatusLines(view, linesSet, linesText)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if statusline {
		planPath := *path
		if planPath == "" {
			planPath = os.Getenv("REIN_PLAN")
		}
		host := supervisor.ReadDAGStatuslineHost(os.Stdin, supervisorStatuslineHostLimit, supervisorStatuslineHostBudget)
		_, _ = io.WriteString(os.Stdout, supervisorStatusline(planPath, lines, host))
		return 0
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
		read := func(ctx context.Context) (supervisor.DAGStatus, error) {
			return supervisor.ReadDAGStatus(ctx, p, hash)
		}
		if seq > 0 {
			read = func(ctx context.Context) (supervisor.DAGStatus, error) {
				return supervisor.ReadDAGStatusAt(ctx, p, hash, seq)
			}
		}
		options := supervisorStatusOptions{
			json: asJSON, watch: watch.enabled, interval: watch.interval, terminal: supervisorStatusTerminal(os.Stdout),
		}
		if view == "graph" || (view == "tui" && !options.terminal) {
			caption := ""
			if seq > 0 {
				caption = fmt.Sprintf("replay: complete journal entries 1..%d", seq)
			}
			// Width is re-detected per frame so a resized terminal reflows on redraw.
			options.render = func(out io.Writer, status supervisor.DAGStatus) error {
				width := supervisor.DAGTerminalColumns(os.Stdout, os.Getenv("COLUMNS"))
				return supervisor.RenderDAGGraph(out, p, status, supervisor.DAGGraphOptions{Width: width, Caption: caption})
			}
		}
		if view == "tui" && options.terminal {
			color := os.Getenv("NO_COLOR") == ""
			render := func(out io.Writer, status supervisor.DAGStatus, final bool) error {
				height := 0
				if !final {
					height = supervisor.DAGTerminalRows(os.Stdout)
				}
				return supervisor.RenderDAGTUI(out, p, status, supervisor.DAGTUIOptions{
					Width:    supervisor.DAGTerminalColumns(os.Stdout, os.Getenv("COLUMNS")),
					Height:   height,
					Color:    color,
					NowMS:    time.Now().UnixMilli(),
					Interval: watch.interval,
					Final:    final,
				})
			}
			return supervisorTUILoop(ctx, os.Stdout, os.Stderr, watch.interval, supervisorTUITick, read, render)
		}
		return supervisorStatusLoop(ctx, os.Stdout, os.Stderr, options, read, waitSupervisorStatus)
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
	render   func(io.Writer, supervisor.DAGStatus) error
}

// supervisorStatusViewOptions validates the display flags before the plan,
// journal or any output is touched, and returns the replay sequence (0 = live).
func supervisorStatusViewOptions(view string, asJSON, watch, seqSet bool, seqText string) (int, error) {
	switch view {
	case "table", "graph", "statusline", "tui":
	default:
		return 0, fmt.Errorf("invalid --view %q: want table, graph, statusline or tui", view)
	}
	if view != "table" && asJSON {
		return 0, fmt.Errorf("--view %s cannot be combined with --json", view)
	}
	// tui uses --watch as its journal re-read interval; statusline is one shot.
	if view == "statusline" && watch {
		return 0, fmt.Errorf("--view statusline cannot be combined with --watch")
	}
	if !seqSet {
		return 0, nil
	}
	if asJSON {
		return 0, fmt.Errorf("--seq cannot be combined with --json")
	}
	if watch {
		return 0, fmt.Errorf("--seq cannot be combined with --watch")
	}
	if view != "graph" {
		return 0, fmt.Errorf("--seq requires --view graph")
	}
	seq, err := strconv.Atoi(seqText)
	if err != nil || seq < 1 {
		return 0, fmt.Errorf("invalid --seq %q: want a positive integer", seqText)
	}
	return seq, nil
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
	type observation struct {
		view supervisor.DAGStatus
		err  error
	}
	for frame := 0; ; frame++ {
		if ctx.Err() != nil {
			return 130
		}
		// Only one reader is in flight. A blocked kernel read may outlive this
		// loop; its buffered result never writes output or starts another poll.
		finished := make(chan observation, 1)
		go func() {
			view, err := read(ctx)
			finished <- observation{view, err}
		}()
		var got observation
		select {
		case <-ctx.Done():
			return 130
		case got = <-finished:
		}
		if ctx.Err() != nil {
			return 130
		}
		view, err := got.view, got.err
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
			} else if options.render != nil {
				err = options.render(out, view)
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

const (
	// The host payload (Claude Code session JSON, AGY state JSON) is small;
	// anything larger is ignored. The budget bounds a host that keeps stdin
	// open without writing.
	supervisorStatuslineHostLimit  = 64 << 10
	supervisorStatuslineHostBudget = 500 * time.Millisecond
	// supervisorStatuslineReadBudget bounds the journal snapshot read.
	supervisorStatuslineReadBudget = 2 * time.Second
	supervisorTUITick              = 100 * time.Millisecond
)

// supervisorStatusLines validates --lines before any plan or journal read.
// Without --lines the statusline keeps four lines.
func supervisorStatusLines(view string, set bool, text string) (int, error) {
	if !set {
		return 4, nil
	}
	if view != "statusline" {
		return 0, fmt.Errorf("--lines requires --view statusline")
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid --lines %q: want a positive integer", text)
	}
	return n, nil
}

// supervisorStatusline renders the host status-line frame. It never fails:
// a status-line host blanks its line on a non-zero exit, so every plan or
// journal problem becomes one short "rein: <reason>" line instead.
func supervisorStatusline(planPath string, lines int, host []byte) string {
	fail := func(reason string) string {
		return supervisor.DAGStatuslineFallback(reason) + "\n"
	}
	if planPath == "" {
		return fail("no plan; set --plan or REIN_PLAN")
	}
	p, hash, err := supervisor.Load(planPath)
	if err != nil {
		return fail("plan: " + err.Error())
	}
	if p.Schema != 3 {
		return fail("status requires a schema 3 plan")
	}
	if err := validateLocalDAGPlan(p); err != nil {
		return fail("plan: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), supervisorStatuslineReadBudget)
	defer cancel()
	status, err := supervisor.ReadDAGStatus(ctx, p, hash)
	if err != nil {
		return fail("status: " + err.Error())
	}
	var b strings.Builder
	err = supervisor.RenderDAGStatusline(&b, p, status, supervisor.DAGStatuslineOptions{
		Width: supervisor.DAGStatuslineWidth(os.Getenv("COLUMNS"), host),
		Lines: lines,
		Color: os.Getenv("NO_COLOR") == "",
		NowMS: supervisor.DAGStatuslineNowMS(os.Getenv("REIN_NOW_UNIX_MS"), time.Now()),
	})
	if err != nil {
		return fail("status: " + err.Error())
	}
	return b.String()
}

// supervisorTUILoop drives the full-screen view on a real terminal: it enters
// the alternate screen with a hidden cursor, redraws every tick (spinner) and
// re-reads the journal every interval with at most one reader in flight. The
// terminal is restored on every exit path; a terminal snapshot is printed
// once more on the normal screen. Interrupt (SIGINT/SIGTERM via ctx) returns 130.
func supervisorTUILoop(ctx context.Context, out, stderr io.Writer, interval, tick time.Duration, read func(context.Context) (supervisor.DAGStatus, error), render func(io.Writer, supervisor.DAGStatus, bool) error) int {
	type observation struct {
		view supervisor.DAGStatus
		err  error
	}
	if _, err := io.WriteString(out, "\x1b[?1049h\x1b[?25l"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			_, _ = io.WriteString(out, "\x1b[?25h\x1b[?1049l")
		}
	}
	defer restore()
	start := func() chan observation {
		finished := make(chan observation, 1)
		go func() {
			view, err := read(ctx)
			finished <- observation{view, err}
		}()
		return finished
	}
	fail := func(err error) int {
		restore()
		fmt.Fprintln(stderr, err)
		return 1
	}
	draw := func(view supervisor.DAGStatus) error {
		var frame strings.Builder
		if err := render(&frame, view, false); err != nil {
			return err
		}
		// Home, overwrite each line, clear the rest: no full-screen flash.
		_, err := io.WriteString(out, "\x1b[H"+strings.ReplaceAll(frame.String(), "\n", "\x1b[K\n")+"\x1b[J")
		return err
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	reread := time.NewTimer(interval)
	reread.Stop()
	defer reread.Stop()
	pending := start()
	var next <-chan time.Time
	var view supervisor.DAGStatus
	have := false
	for {
		select {
		case <-ctx.Done():
			restore()
			return 130
		case got := <-pending:
			pending = nil
			if ctx.Err() != nil {
				restore()
				return 130
			}
			if got.err != nil {
				return fail(got.err)
			}
			view, have = got.view, true
			if view.Terminal() {
				restore()
				if err := render(out, view, true); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
				return 0
			}
			if err := draw(view); err != nil {
				return fail(err)
			}
			reread.Reset(interval)
			next = reread.C
		case <-next:
			next = nil
			pending = start()
		case <-ticker.C:
			if have {
				if err := draw(view); err != nil {
					return fail(err)
				}
			}
		}
	}
}
