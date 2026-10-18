package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/voravitl/rein/internal/orca"
)

func cmdOrca(args []string) int {
	usage := func() {
		fmt.Fprintln(os.Stderr, `usage: rein orca <call|launch|wait|drain|close|tidy|schema> [options]
  call   [--action A] [--input JSON|file] [--stdin] [--schema] [<action>] [flags]
         Execute deterministic Orca tool call (status, launch, wait, drain, close, tidy, send, read, terminals, worktrees)
  schema Emit official JSON schema for agent/tool-use integration
  launch <name> <base> <spec-file> <agent> [--model M] [--effort E] [--run R] [--coord H] [--repo ID] [--repo-dir D]
         Launch a supervised worker inside Orca on a clean worktree
  wait   [--terminal <coord>] [--timeout-ms <ms>] [--rounds <n>] [--json]
         Wait for worker_done, escalation, or question from active workers
  drain  [--terminal <coord>] [--no-ack] [--json]
         Read and acknowledge pending deliveries (heartbeats counted, questions/reports shown)
  close  --worktree <path> [--terminal <handle>] [--dispatch <id>] [--check-only] [--json]
         Safely release dispatch, close terminal and delete worktree (refuses dirty/unique commits)
  tidy   [--apply] [--workspaces <dir>] [--coord-wt <name>] [--json]
         Audit Orca terminals and report shell candidates without closing tabs
  census record --pid <N> --out <file>   Pin a process tree's identities from OS tables (opt-in, same host)
  census verify --in <file>              Prove every pinned identity is gone: exit 0 gone, 1 still present,
                                         2 unsupported platform or foreign-host boot snapshot`)
	}

	if len(args) == 0 {
		usage()
		return 2
	}

	switch args[0] {
	case "call":
		return cmdOrcaCall(args[1:])
	case "schema":
		fmt.Println(string(orca.ToolSchemaJSON()))
		return 0
	case "launch":
		return cmdOrcaLaunch(args[1:])
	case "wait":
		return cmdOrcaWait(args[1:])
	case "drain":
		return cmdOrcaDrain(args[1:])
	case "close":
		return cmdOrcaClose(args[1:])
	case "tidy":
		return cmdOrcaTidy(args[1:])
	case "census":
		return cmdOrcaCensus(args[1:])
	case "--help", "-h", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "rein orca: unknown subcommand %q\n", args[0])
		usage()
		return 2
	}
}

// Orca launch accepts options after operands, unlike flag.FlagSet.Parse.
func parseOrcaFlags(fs *flag.FlagSet, args []string) error {
	var flags, operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := fs.Lookup(name)
		if hasValue || option == nil {
			continue
		}
		boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	return fs.Parse(append([]string{"--"}, operands...))
}

func cmdOrcaLaunch(args []string) int {
	fs := flag.NewFlagSet("orca launch", flag.ContinueOnError)
	modelFlag := fs.String("model", "", "model name override")
	effortFlag := fs.String("effort", "high", "reasoning effort")
	runFlag := fs.String("run", os.Getenv("ORCA_RUN"), "run container ID")
	coordFlag := fs.String("coord", os.Getenv("ORCA_COORD"), "coordinator terminal handle")
	repoIDFlag := fs.String("repo", os.Getenv("ORCA_REPO_ID"), "orca repo id")
	repoDirFlag := fs.String("repo-dir", os.Getenv("ORCA_REPO_DIR"), "repository root directory")
	jsonFlag := fs.Bool("json", false, "emit json")

	if err := parseOrcaFlags(fs, args); err != nil {
		return 2
	}
	pos := fs.Args()
	if len(pos) < 4 {
		fmt.Fprintln(os.Stderr, "usage: rein orca launch <name> <base-branch> <spec-file> <agent> [flags]")
		return 2
	}
	name, base, specPath, agent := pos[0], pos[1], pos[2], pos[3]

	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein orca launch: read spec file: %v\n", err)
		return 2
	}
	if len(strings.TrimSpace(string(specBytes))) == 0 {
		fmt.Fprintln(os.Stderr, "rein orca launch: spec file is empty (nothing created)")
		return 2
	}

	repoDir := *repoDirFlag
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}

	res, err := orca.Launch(context.Background(), orca.LaunchOptions{
		Name:        name,
		BaseBranch:  base,
		SpecContent: string(specBytes),
		Agent:       agent,
		Model:       *modelFlag,
		Effort:      *effortFlag,
		RepoID:      *repoIDFlag,
		RepoDir:     repoDir,
		RunID:       *runFlag,
		CoordHandle: *coordFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein orca launch error: %v\n", err)
		return 1
	}

	if *jsonFlag {
		fmt.Println(string(orca.PrettyJSON(res)))
	} else {
		fmt.Printf("LAUNCHED name=%s wt=%s worker=%s terminal=%s task=%s dispatch=%s model=%s effort=%s\n",
			res.Name, res.WorktreePath, res.WorkerHandle, res.TerminalHandle, res.TaskID, res.DispatchID, res.Model, res.Effort)
	}
	return 0
}

func cmdOrcaWait(args []string) int {
	fs := flag.NewFlagSet("orca wait", flag.ContinueOnError)
	coordFlag := fs.String("terminal", os.Getenv("ORCA_COORD"), "coordinator terminal handle")
	timeoutMsFlag := fs.Int("timeout-ms", 600000, "timeout per check round")
	roundsFlag := fs.Int("rounds", 60, "maximum check rounds")
	jsonFlag := fs.Bool("json", false, "emit json")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *coordFlag == "" {
		fmt.Fprintln(os.Stderr, "rein orca wait: missing --terminal (or export ORCA_COORD)")
		return 2
	}

	res, err := orca.Wait(context.Background(), orca.WaitOptions{
		CoordHandle: *coordFlag,
		TimeoutMs:   *timeoutMsFlag,
		MaxRounds:   *roundsFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein orca wait: %v\n", err)
		return 3
	}

	if *jsonFlag {
		fmt.Println(string(orca.PrettyJSON(res)))
	} else {
		fmt.Printf("SIGNAL after round %d: %s\n", res.SignalRound, res.MessageType)
		if res.Raw != "" {
			fmt.Println(res.Raw)
		}
	}
	return 0
}

func cmdOrcaDrain(args []string) int {
	fs := flag.NewFlagSet("orca drain", flag.ContinueOnError)
	coordFlag := fs.String("terminal", os.Getenv("ORCA_COORD"), "coordinator terminal handle")
	noAckFlag := fs.Bool("no-ack", false, "do not acknowledge deliveries")
	jsonFlag := fs.Bool("json", false, "emit json")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *coordFlag == "" {
		fmt.Fprintln(os.Stderr, "rein orca drain: missing --terminal (or export ORCA_COORD)")
		return 2
	}

	res, err := orca.Drain(context.Background(), *coordFlag, !*noAckFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein orca drain: %v\n", err)
		return 1
	}

	if *jsonFlag {
		fmt.Println(string(orca.PrettyJSON(res)))
		return 0
	}

	if res.Heartbeats > 0 {
		fmt.Printf("(%d heartbeat(s))\n", res.Heartbeats)
	}
	if len(res.Messages) == 0 {
		fmt.Println("no pending deliveries left")
		return 0
	}

	for _, m := range res.Messages {
		sender := m.FromHandle
		if len(sender) > 12 {
			sender = sender[len(sender)-12:]
		}
		fmt.Printf("=== %s | %s | from %s\n", m.Type, m.Subject, sender)
		if m.Payload != "" {
			p := m.Payload
			if len(p) > 300 {
				p = p[:300] + "..."
			}
			fmt.Printf("payload: %s\n", p)
		}
		if m.Body != "" {
			b := m.Body
			if len(b) > 800 {
				b = b[:800] + "..."
			}
			fmt.Println(b)
		}
	}
	return 0
}

func cmdOrcaClose(args []string) int {
	fs := flag.NewFlagSet("orca close", flag.ContinueOnError)
	wtFlag := fs.String("worktree", "", "worktree path to close")
	termFlag := fs.String("terminal", "", "worker terminal handle")
	dispFlag := fs.String("dispatch", "", "dispatch ID")
	checkOnlyFlag := fs.Bool("check-only", false, "only run safety checks")
	jsonFlag := fs.Bool("json", false, "emit json")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *wtFlag == "" {
		fmt.Fprintln(os.Stderr, "rein orca close: missing --worktree <path>")
		return 2
	}

	res, err := orca.Close(context.Background(), orca.CloseOptions{
		WorktreePath:   *wtFlag,
		TerminalHandle: *termFlag,
		DispatchID:     *dispFlag,
		CheckOnly:      *checkOnlyFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein orca close: %v\n", err)
		if strings.Contains(err.Error(), "uncommitted changes") {
			return 3
		}
		if strings.Contains(err.Error(), "unique commits") {
			return 4
		}
		return 1
	}

	if *jsonFlag {
		fmt.Println(string(orca.PrettyJSON(res)))
		return 0
	}

	if *checkOnlyFlag {
		fmt.Println("safe: clean tree, HEAD reachable in another ref")
		return 0
	}

	fmt.Println("safe: clean tree, HEAD reachable in another ref")
	if res.ReleaseState != "" {
		fmt.Printf("release: %s\n", res.ReleaseState)
	}
	if res.TerminalClosed {
		fmt.Printf("terminal closed: %s\n", *termFlag)
	}
	if res.WorktreeGone {
		fmt.Printf("worktree removed: %s\n", *wtFlag)
	} else {
		fmt.Printf("worktree rm requested for %s\n", *wtFlag)
	}
	return 0
}

func cmdOrcaTidy(args []string) int {
	fs := flag.NewFlagSet("orca tidy", flag.ContinueOnError)
	applyFlag := fs.Bool("apply", false, "audit only; shell candidates require manual inspection")
	workspacesFlag := fs.String("workspaces", os.Getenv("ORCA_WORKSPACES"), "workspaces root directory")
	coordWtFlag := fs.String("coord-wt", os.Getenv("ORCA_COORD_WT"), "coordinator worktree name")
	repoDirFlag := fs.String("repo-dir", os.Getenv("ORCA_REPO_DIR"), "repo root directory")
	jsonFlag := fs.Bool("json", false, "emit json")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	repoDir := *repoDirFlag
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	wsp := *workspacesFlag
	if wsp == "" {
		home, _ := os.UserHomeDir()
		wsp = filepath.Join(home, "orca", "workspaces")
	}

	res, err := orca.Tidy(context.Background(), orca.TidyOptions{
		RepoDir:       repoDir,
		WorkspacesDir: wsp,
		CoordWorktree: *coordWtFlag,
		Apply:         *applyFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein orca tidy: %v\n", err)
		return 1
	}

	if *jsonFlag {
		fmt.Println(string(orca.PrettyJSON(res)))
		return 0
	}

	fmt.Printf("== Orca terminals (workspaces under %s) ==\n", wsp)
	for _, t := range res.Terminals {
		handle := t.Handle
		if len(handle) > 12 {
			handle = handle[len(handle)-12:]
		}
		switch t.Category {
		case "foreign":
			fmt.Printf("  foreign (never touch): %s %s\n", handle, t.Title)
		case "shell_candidate":
			fmt.Printf("  INSPECT (shell candidate; kept): %s %s\n", handle, t.Title)
		case "active_worker":
			fmt.Printf("  KEEP (worker running): %s %s\n", handle, t.Title)
		}
	}

	if len(res.Worktrees) > 0 {
		fmt.Println("== git worktrees ==")
		for _, wt := range res.Worktrees {
			fmt.Printf("  %s\n", wt)
		}
	}

	if len(res.Closed) > 0 {
		fmt.Printf("closed %d idle default shell tab(s)\n", len(res.Closed))
	}
	return 0
}

func parseEffort(e string) string {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "high"
	}
	return e
}

func parseInt(s string, def int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func cmdOrcaCall(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rein orca call [--action <action>] [--input <json|file>] [--stdin] [--schema] [<action>] [flags]")
		return 2
	}

	if args[0] == "--schema" || args[0] == "schema" {
		fmt.Println(string(orca.ToolSchemaJSON()))
		return 0
	}

	trimmedFirst := strings.TrimSpace(args[0])
	if strings.HasPrefix(trimmedFirst, "{") && strings.HasSuffix(trimmedFirst, "}") {
		resp, err := orca.CallFromJSON(context.Background(), []byte(trimmedFirst))
		fmt.Println(string(orca.PrettyJSON(resp)))
		return orcaCallExitCode(resp, err)
	}

	fs := flag.NewFlagSet("orca call", flag.ContinueOnError)
	actionFlag := fs.String("action", "", "action to execute")
	inputFlag := fs.String("input", "", "json string or path to json file")
	stdinFlag := fs.Bool("stdin", false, "read json payload from stdin")
	schemaFlag := fs.Bool("schema", false, "emit json schema")

	termFlag := fs.String("terminal", "", "explicit worker terminal; coordinator for status/wait/drain")
	wtFlag := fs.String("worktree", "", "worktree path or selector")
	textFlag := fs.String("text", "", "text to send")
	enterFlag := fs.Bool("enter", false, "append enter")
	interruptFlag := fs.Bool("interrupt", false, "send interrupt")
	timeoutMsFlag := fs.Int("timeout-ms", 60000, "timeout in ms")
	roundsFlag := fs.Int("rounds", 60, "rounds count")
	cursorFlag := fs.Int("cursor", -1, "cursor offset")
	limitFlag := fs.Int("limit", 0, "limit count")
	ackFlag := fs.Bool("ack", true, "ack deliveries")
	noAckFlag := fs.Bool("no-ack", false, "do not ack deliveries")
	applyFlag := fs.Bool("apply", false, "apply destructive changes")
	checkOnlyFlag := fs.Bool("check-only", false, "check only without mutation")
	dispatchFlag := fs.String("dispatch", "", "dispatch id")
	modelFlag := fs.String("model", "", "model override")
	effortFlag := fs.String("effort", "high", "reasoning effort")
	runFlag := fs.String("run", os.Getenv("ORCA_RUN"), "run container id")
	coordFlag := fs.String("coord", os.Getenv("ORCA_COORD"), "coordinator handle")
	repoFlag := fs.String("repo", os.Getenv("ORCA_REPO_ID"), "repo id")
	repoDirFlag := fs.String("repo-dir", os.Getenv("ORCA_REPO_DIR"), "repo directory")
	specFileFlag := fs.String("spec-file", "", "spec file path")

	var remainingArgs []string
	posAction := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		posAction = strings.ToLower(args[0])
		remainingArgs = args[1:]
	} else {
		remainingArgs = args
	}

	if err := parseOrcaFlags(fs, remainingArgs); err != nil {
		return 2
	}

	if *schemaFlag {
		fmt.Println(string(orca.ToolSchemaJSON()))
		return 0
	}

	if *stdinFlag {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rein orca call: read stdin: %v\n", err)
			return 2
		}
		resp, err := orca.CallFromJSON(context.Background(), data)
		fmt.Println(string(orca.PrettyJSON(resp)))
		return orcaCallExitCode(resp, err)
	}

	if *inputFlag != "" {
		var data []byte
		inputStr := strings.TrimSpace(*inputFlag)
		if strings.HasPrefix(inputStr, "{") {
			data = []byte(inputStr)
		} else {
			var err error
			data, err = os.ReadFile(inputStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "rein orca call: read input file %q: %v\n", inputStr, err)
				return 2
			}
		}
		resp, err := orca.CallFromJSON(context.Background(), data)
		fmt.Println(string(orca.PrettyJSON(resp)))
		return orcaCallExitCode(resp, err)
	}

	action := posAction
	if action == "" {
		action = *actionFlag
	}
	if action == "" {
		fmt.Fprintln(os.Stderr, "rein orca call: missing action (e.g. status, launch, wait, drain, close, tidy, send, read, terminals, worktrees)")
		return 2
	}

	callArgs := make(map[string]interface{})
	if *termFlag != "" {
		callArgs["terminal"] = *termFlag
	}
	if *wtFlag != "" {
		callArgs["worktree"] = *wtFlag
	}
	if *textFlag != "" {
		callArgs["text"] = *textFlag
	}
	if *enterFlag {
		callArgs["enter"] = true
	}
	if *interruptFlag {
		callArgs["interrupt"] = true
	}
	if *timeoutMsFlag != 60000 {
		callArgs["timeout_ms"] = *timeoutMsFlag
	}
	if *roundsFlag != 60 {
		callArgs["rounds"] = *roundsFlag
	}
	if *cursorFlag >= 0 {
		callArgs["cursor"] = *cursorFlag
	}
	if *limitFlag > 0 {
		callArgs["limit"] = *limitFlag
	}
	if *noAckFlag {
		callArgs["ack"] = false
	} else if *ackFlag {
		callArgs["ack"] = true
	}
	if *applyFlag {
		callArgs["apply"] = true
	}
	if *checkOnlyFlag {
		callArgs["check_only"] = true
	}
	if *dispatchFlag != "" {
		callArgs["dispatch"] = *dispatchFlag
	}
	if *modelFlag != "" {
		callArgs["model"] = *modelFlag
	}
	if *effortFlag != "" {
		callArgs["effort"] = *effortFlag
	}
	if *runFlag != "" {
		callArgs["run"] = *runFlag
	}
	if *coordFlag != "" {
		callArgs["coord"] = *coordFlag
	}
	if *repoFlag != "" {
		callArgs["repo"] = *repoFlag
	}
	if *repoDirFlag != "" {
		callArgs["repo_dir"] = *repoDirFlag
	}
	if *specFileFlag != "" {
		callArgs["spec_file"] = *specFileFlag
	}

	rest := fs.Args()
	if action == "launch" && len(rest) >= 4 {
		callArgs["name"] = rest[0]
		callArgs["base"] = rest[1]
		callArgs["spec_file"] = rest[2]
		callArgs["agent"] = rest[3]
	}

	resp, err := orca.Call(context.Background(), orca.CallRequest{
		Action: action,
		Args:   callArgs,
	})
	fmt.Println(string(orca.PrettyJSON(resp)))
	return orcaCallExitCode(resp, err)
}

func orcaCallExitCode(resp *orca.CallResponse, err error) int {
	switch {
	case errors.Is(err, orca.ErrInvalidRequest):
		return 2
	case errors.Is(err, orca.ErrDirtyWorktree):
		return 3
	case errors.Is(err, orca.ErrUniqueCommits):
		return 4
	case err != nil || resp == nil || !resp.OK:
		return 1
	default:
		return 0
	}
}

// census is the opt-in same-host verifier: record pins the tree's identities, verify proves them gone.
// Same-host by design — snapshots from another host or boot refuse to verify instead of guessing.
func cmdOrcaCensus(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "rein orca census: need record|verify")
		return 2
	}
	fs := flag.NewFlagSet("rein orca census "+args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pid := fs.Int("pid", 0, "root pid to pin")
	out := fs.String("out", "", "snapshot JSON path (record)")
	in := fs.String("in", "", "snapshot JSON path (verify)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "rein orca census %s: unexpected operand %q\n", args[0], fs.Arg(0))
		return 2
	}
	switch args[0] {
	case "record":
		if *pid <= 0 || *out == "" {
			fmt.Fprintln(os.Stderr, "usage: rein orca census record --pid <N> --out <file>")
			return 2
		}
		snap, err := orca.CensusRecord(*pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rein orca census record: %v\n", err)
			return 2
		}
		if err := orca.SaveCensus(snap, *out); err != nil {
			fmt.Fprintf(os.Stderr, "rein orca census record: %v\n", err)
			return 2
		}
		fmt.Printf("census recorded: root pid %d, %d sampled node(s), marker %s\n", snap.Root.PID, len(snap.Nodes)+1, snap.Marker)
		return 0
	case "verify":
		if *in == "" {
			fmt.Fprintln(os.Stderr, "usage: rein orca census verify --in <file>")
			return 2
		}
		snap, err := orca.LoadCensus(*in)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rein orca census verify: %v\n", err)
			return 2
		}
		allGone, findings, err := orca.CensusVerify(snap)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rein orca census verify: %v\n", err)
			return 2
		}
		for _, f := range findings {
			fmt.Printf("%s: %s (%s)\n", f.Identity, f.Status, f.Detail)
		}
		fmt.Printf("all gone: %v\n", allGone)
		if allGone {
			return 0
		}
		return 1
	default:
		fmt.Fprintf(os.Stderr, "rein orca census: unknown mode %q; want record|verify\n", args[0])
		return 2
	}
}
