package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/orca"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

var stopGraphQuotaProcess = stopGraphProcess

// cmdRouteLaunch runs an argv, never shell text, after validating its prepared routing decision.
func cmdRouteLaunch(args []string) int { return routeLaunch(context.Background(), args, nil, nil, nil) }

// Only an in-process controller capability may bypass the legacy ownership gate.
func routeLaunch(ctx context.Context, args []string, controller *run.Controller, result *graphExecution, expected *routing.Decision) int {
	for _, name := range []string{"--task", "--run"} {
		if _, err := launchFlag(args, name); err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 2
		}
	}
	fs := flag.NewFlagSet("route launch", flag.ContinueOnError)
	task := fs.String("task", "", "rein contract name")
	runID := fs.String("run", "", "run identity")
	if fs.Parse(args) != nil || *task == "" || *runID == "" || len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "[route] launch requires --task T --run R -- <provider argv>")
		return 2
	}
	c, err := contract.Load(*task)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	if controller != nil {
		if err := graphLaunchAuthority(controller, c, *runID); err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
	}
	// A supervised run has one controller; legacy launch must stop before consuming a receipt.
	loc, found := run.Find(c.Worktree)
	var locateErr error
	if !found {
		loc, locateErr = run.Locate(c.Worktree)
	}
	if locateErr == nil && controller == nil {
		unlock, err := run.AcquireLegacyController(loc.Top, *runID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
		defer unlock()
	}
	argv := fs.Args()
	if controller != nil && isOrcaLaunch(argv) {
		fmt.Fprintln(os.Stderr, "[route] supervised graph requires a local process group; remote worker death cannot be proven")
		return 1
	}
	agent, model, err := launchIdentity(argv, c, *runID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	var endpointArgs []string
	if isOrcaLaunch(argv) {
		endpointArgs, err = orcaObservationSelectors(argv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
	}
	d, err := routing.Validate(c, *runID, agent, model)
	if err == nil {
		err = validateRouteQuotaScope(d, argv)
	}
	if err == nil && controller != nil && (expected == nil || d.Attempt != expected.Attempt || d.DecisionID != expected.DecisionID || d.Hold != expected.Hold) {
		err = fmt.Errorf("supervised receipt changed after graph preparation")
	}
	if err == nil && !strings.HasPrefix(d.Chain, "worker:") {
		err = fmt.Errorf("implementation launch requires a worker route; use rein advise for read-only review")
	}
	if err == nil && isOrcaLaunch(argv) && d.Launch != "orca" {
		err = fmt.Errorf("selected provider requires the shell route; Orca does not forward its model/guard flags")
	}
	if err == nil && isOrcaLaunch(argv) && (agent == "codex" || agent == "kiro" || agent == "opencode" || agent == "opencode2") {
		err = fmt.Errorf("this harness requires the direct shell route for model and hook flags")
	}
	if err == nil {
		err = routeBudget(c, *runID)
	}
	if err == nil && d.Attempt != "" {
		// the prepared effort is part of the evidence the model was chosen on: launch exactly it, or nothing
		var effort string
		expectedEffort := strings.ToLower(d.Effort)
		if name := filepath.Base(argv[0]); !isOrcaLaunch(argv) && (name == "opencode" || name == "opencode2") {
			expectedEffort = d.Effort
		}
		if effort, err = launchEffort(argv); err == nil && effort != expectedEffort {
			err = fmt.Errorf("launch effort %q differs from the prepared effort %q", effort, d.Effort)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	var graphInput io.Reader
	if name := filepath.Base(argv[0]); controller != nil && (name == "opencode" || name == "opencode2") {
		slot, err := graphPromptSlot(argv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
		// V2 quotes positional prompts containing spaces; stdin preserves the approved bytes.
		graphInput = strings.NewReader(argv[slot])
		start := slot
		if argv[slot-1] == "--" {
			start--
		}
		argv = append(append([]string(nil), argv[:start]...), argv[slot+1:]...)
	}
	// one live writer per checkout: proven by process identity, released only on proof of exit
	var lease *routing.Lease
	if d.Attempt != "" {
		if lease, err = routing.AcquireLease(c, d); err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
		if controller == nil {
			defer lease.Release()
		}
	}
	if isOrcaLaunch(argv) && lease == nil {
		fmt.Fprintln(os.Stderr, "[route] Orca dispatch requires an attributable task lease")
		return 1
	}
	if !isOrcaLaunch(argv) && agent == "codex" {
		bin, binErr := os.Executable()
		if binErr != nil {
			fmt.Fprintln(os.Stderr, "[route]", binErr)
			return 1
		}
		argv = append([]string{argv[0], "exec"}, append(hooks.CodexArgs(bin, c.Name), argv[2:]...)...)
	}
	if !isOrcaLaunch(argv) && agent == "kiro" {
		selected, _ := launchFlag(argv[1:], "--agent")
		if selected == "" {
			argv = append([]string{argv[0], "chat", "--agent", "rein"}, argv[2:]...)
		}
	}
	// Record before starting: a missing ledger must never leave an untracked model call.
	launch := "shell"
	if isOrcaLaunch(argv) {
		launch = "orca"
	}
	launchNote := map[string]any{"provider": d.Provider, "chain": d.Chain, "launch": launch, "configured_launch": d.Launch}
	if controller != nil && result != nil {
		launchNote["input_digest"] = result.inputDigest
	}
	note, _ := json.Marshal(launchNote)
	row := ledger.Row{Kind: "routing_launch", Task: c.Name, Run: *runID, Provider: d.Provider,
		Model: model, Role: d.Chain, Notes: string(note)}
	if d.Attempt != "" { // attributable evidence: the denominator of this model's record, with who launched it
		pid := os.Getpid()
		start, _ := run.ProcStart(pid)
		row.Attempt, row.Decision, row.Worker = d.Attempt, d.DecisionID, &ledger.AgentModel{Agent: agent, Model: model}
		row.Type, row.Effort, row.Config, row.Suite, row.Tier, row.Pid, row.PidStart = d.Kind, d.Effort, d.ConfigFingerprint, d.Suite, d.Tier, &pid, &start
	}
	if err := ledger.Append(row); err != nil {
		fmt.Fprintln(os.Stderr, "[route] cannot record launch:", err)
		return 1
	}
	if controller != nil {
		if err := graphLaunchAuthority(controller, c, *runID); err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.Worktree
	cmd.Env = append(os.Environ(), "ADVISE_RUN="+*runID, "ADVISE_TASK="+c.Name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var captured dispatchReceiptBuffer
	var agyStderr *dispatchReceiptBuffer
	var quota *graphQuotaWriter
	if controller != nil {
		cfg, err := providers.Load(d.ConfigPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		provider, exists := cfg.Providers[d.Provider]
		if !exists {
			return 1
		}
		quota = &graphQuotaWriter{capture: &captured, matched: make(chan string, 1)}
		for _, signal := range cfg.QuotaSignals[provider.Agent] {
			signal = strings.ToLower(signal)
			if strings.TrimSpace(signal) == "" {
				continue
			}
			if len(signal) > 4096 || len(quota.signals) >= 64 {
				fmt.Fprintln(os.Stderr, "quota signal configuration exceeds bounds")
				return 1
			}
			quota.signals = append(quota.signals, signal)
		}
		cmd.Env, err = graphHandoffEnv(cmd.Env, result)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := configureGraphProcess(cmd); err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
		cmd.Stdin = graphInput
		cmd.Stdout = quota
		cmd.Stderr = quota
		if filepath.Base(argv[0]) == "agy" {
			agyStderr = &dispatchReceiptBuffer{}
			cmd.Stderr = io.MultiWriter(quota, agyStderr)
		}
	}
	var workerReceipt dispatchReceiptBuffer
	if isOrcaLaunch(argv) {
		cmd.Stdout = io.MultiWriter(os.Stdout, &workerReceipt)
	}
	// Orca can dispatch a worker before returning an acknowledgement, including a failed one.
	if lease != nil && isOrcaLaunch(argv) {
		if err := lease.MarkDispatchPending(); err != nil {
			fmt.Fprintln(os.Stderr, "[route] cannot fence the Orca dispatch:", err)
			return 1
		}
	}
	started := time.Now()
	err = cmd.Start()
	if err == nil && lease != nil {
		setChild := lease.SetChild
		if controller != nil {
			setChild = lease.SetChildGroup
		}
		if lerr := setChild(cmd.Process.Pid); lerr != nil { // an unrecorded provider process could hide a writer after a crash
			if controller != nil {
				stopGraphProcess(cmd)
			} else {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			err = fmt.Errorf("cannot record the provider process in the task lease: %w", lerr)
		}
	}
	quotaSignal := ""
	stopRecorded := true
	if err == nil {
		if quota == nil {
			err = cmd.Wait()
		} else {
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			select {
			case err = <-waited:
				select {
				case quotaSignal = <-quota.matched:
				default:
				}
			case quotaSignal = <-quota.matched:
				stopRow := row
				stopRow.Kind = "routing_stop_requested"
				stopNote, _ := json.Marshal(map[string]any{"signal": quotaSignal, "pgid": cmd.Process.Pid, "child_start": lease.ChildStart})
				stopRow.Notes = string(stopNote)
				if lerr := ledger.Append(stopRow); lerr != nil {
					stopRecorded = false
					fmt.Fprintln(os.Stderr, "[route] cannot persist quota stop intent:", lerr)
				}
				childStart, identityErr := run.ProcStart(cmd.Process.Pid)
				if errors.Is(identityErr, run.ErrNoProc) {
					err = <-waited
					break
				}
				if identityErr != nil || childStart != lease.ChildStart {
					fmt.Fprintln(os.Stderr, "[route] quota stop process identity unknown")
					return 1
				}
				if stopErr := stopGraphQuotaProcess(cmd); stopErr != nil {
					fmt.Fprintln(os.Stderr, "[route] quota stop failed:", stopErr)
					return 1
				}
				err = <-waited
			}
		}
	}
	// Native agy can exit zero with an unfinished turn; only its stderr diagnostic is authoritative.
	if err == nil && agyStderr != nil && (agyStderr.overflow || agyPrintTimedOut(agyStderr.String())) {
		err = errors.New("Antigravity print completion unverified (partial timeout or stderr capture overflow)")
	}
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "[route] provider launch:", err)
	}
	minutes := time.Since(started).Minutes()
	row.Kind, row.Minutes = "routing_exit", &minutes
	if orca := isOrcaLaunch(argv); orca {
		// worker-start returns once the worker STARTED, not when it ended: the time is not the worker's, and nothing here can
		// vouch for the worker's process. Settlement remains blocked without proven remote termination.
		row.Minutes = nil
		if lease != nil {
			var holdErr error
			if taskID, dispatchID, handle, ready := workerReceipt.workerIdentity(); code == 0 && ready {
				holdErr = lease.MarkDispatchIdentity(taskID, dispatchID)
				if holdErr == nil {
					holdErr = observeOrcaWorker(ctx, cmd, endpointArgs, *runID, taskID, dispatchID, handle, d.Agent, d.Model, d.Effort)
					if holdErr == nil {
						launchNote["worker_observation"] = "exact_live_ready"
						note, _ := json.Marshal(launchNote)
						row.Notes = string(note)
					}
				}
			} else {
				holdErr = lease.MarkDispatched()
				if holdErr == nil && code == 0 {
					holdErr = errors.New("worker-start did not confirm ready task/dispatch/handle")
				}
			}
			if err := holdErr; err != nil {
				fmt.Fprintln(os.Stderr, "[route] worker dispatch remains fenced; confirmation failed:", err)
				code = 1
			}
		}
	}
	ok := code == 0
	row.OK = &ok
	if err := ledger.Append(row); err != nil {
		fmt.Fprintln(os.Stderr, "[route] cannot record provider exit:", err)
		return 1
	}
	if controller != nil && result != nil {
		if cmd.Process == nil || lease == nil || !stopRecorded {
			return 1
		}
		if err := lease.CompleteLocal(); err != nil {
			fmt.Fprintln(os.Stderr, "[route] local writer state unknown:", err)
			return 1
		}
		if !captured.overflow {
			result.QuotaSignal = quotaSignal
		}
		result.Run = *runID
		result.Task = c.Name
		result.Attempt = d.Attempt
		result.Decision = d.DecisionID
		result.Worktree = c.Worktree
		result.Pool = d.Pool
		result.PID = cmd.Process.Pid
		result.PIDStart = lease.ChildStart
		result.PIDStartUnknown = lease.ChildStartUnknown
		result.ExitCode = code
		result.Output = captured.String()
		result.Provider = d.Provider
		result.CompletedAt = time.Now().UTC()
		result.Gone = true
		result.Truncated = captured.overflow
	}
	return code
}

func observeOrcaWorker(ctx context.Context, launch *exec.Cmd, selectors []string, runID, taskID, dispatchID, handle, agent, model, effort string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := append([]string{"orchestration", "worker-show", "--dispatch", dispatchID, "--json"}, selectors...)
	show := exec.CommandContext(ctx, launch.Path, args...)
	show.Dir, show.Env, show.WaitDelay = launch.Dir, append([]string(nil), launch.Env...), time.Second
	var output dispatchReceiptBuffer
	// MultiWriter keeps io.Copy on the bounded Write path rather than embedded Buffer.ReadFrom.
	show.Stdout, show.Stderr = io.MultiWriter(&output), io.Discard
	if err := show.Run(); err != nil {
		return fmt.Errorf("worker dispatch observation unconfirmed: %w", err)
	}
	if output.overflow {
		return errors.New("worker dispatch observation exceeds output bounds")
	}
	_, err := orca.ValidateWorkerObservation(output.Bytes(), runID, taskID, dispatchID, handle)
	if err != nil {
		return err
	}
	return orca.ValidateWorkerLaunchIdentity(output.Bytes(), agent, model, effort)
}

func orcaObservationSelectors(argv []string) ([]string, error) {
	names := []string{"--environment", "--pairing-code"}
	for _, name := range names {
		if _, err := launchFlag(argv[1:], name); err != nil {
			return nil, err
		}
	}
	var selectors []string
	for i := 1; i < len(argv) && argv[i] != "--"; i++ {
		for _, name := range names {
			if argv[i] == name {
				selectors = append(selectors, argv[i], argv[i+1])
				i++
				break
			}
			if strings.HasPrefix(argv[i], name+"=") {
				selectors = append(selectors, argv[i])
				break
			}
		}
	}
	return selectors, nil
}

func agyPrintTimedOut(stderr string) bool {
	const prefix = "[agy] print timeout after "
	const suffix = " with turn in progress; returning partial output"
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, suffix) {
			if _, err := time.ParseDuration(strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)); err == nil {
				return true
			}
		}
	}
	return false
}

func isOrcaLaunch(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	n := filepath.Base(argv[0])
	return n == "orca" || n == "orca-dev" || n == "orca-ide"
}

func routeBudget(c *contract.Contract, runID string) error {
	p, err := contract.LoadProfile("")
	if err != nil {
		return err
	}
	if p.Budget == nil {
		p = &c.Profile
	}
	loc, found := run.Find(c.Worktree)
	if !found {
		if p.Budget != nil {
			return fmt.Errorf("configured budget requires a current Run marker")
		}
		return nil
	}
	m, err := run.Load(loc.Marker)
	if err != nil || m.Run != runID {
		return fmt.Errorf("routing Run differs from current Run marker")
	}
	if !m.Allows("task", c.Name) {
		return fmt.Errorf("routing task is not allowed in current Run")
	}
	if p.Budget == nil {
		return nil
	}
	for _, task := range []string{"", c.Name} {
		result, err := budget.Check(loc.Marker, p, task)
		if err != nil {
			return err
		}
		if result.Code == budget.ExitHard {
			return fmt.Errorf("routing budget denied: %s", result.Message)
		}
	}
	return nil
}

// One explicit value avoids CLI last-value/first-value disagreement at the routing boundary.
func launchFlag(args []string, names ...string) (string, error) {
	value, count := "", 0
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		for _, name := range names {
			if args[i] == name {
				if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
					return "", fmt.Errorf("missing %s value", name)
				}
				count++
				value = args[i+1]
				i++
				break
			}
			if v, found := strings.CutPrefix(args[i], name+"="); found {
				if v == "" {
					return "", fmt.Errorf("missing %s value", name)
				}
				count++
				value = v
			}
		}
	}
	if count > 1 {
		return "", fmt.Errorf("duplicate %s", names[0])
	}
	return value, nil
}

func launchIdentity(argv []string, c *contract.Contract, runID string) (string, string, error) {
	name, args := filepath.Base(argv[0]), argv[1:]
	agent := map[string]string{"claude": "claude", "codex": "codex", "agy": "antigravity", "kiro-cli": "kiro", "opencode": "opencode", "opencode2": "opencode2"}[name]
	if isOrcaLaunch(argv) {
		if _, err := orcaObservationSelectors(argv); err != nil {
			return "", "", err
		}
		if len(args) < 2 || args[0] != "orchestration" || args[1] != "worker-start" {
			return "", "", fmt.Errorf("route launch only supports Orca orchestration worker-start or a provider CLI")
		}
		var err error
		agent, err = launchFlag(args, "--agent")
		if err != nil {
			return "", "", err
		}
		r, err := launchFlag(args, "--run")
		if err != nil || r != runID {
			return "", "", fmt.Errorf("Orca --run must match prepared routing Run")
		}
		wt, err := launchFlag(args, "--worktree")
		if err != nil || !strings.HasPrefix(wt, "path:") || !filepath.IsAbs(strings.TrimPrefix(wt, "path:")) || contract.Real(strings.TrimPrefix(wt, "path:")) != contract.Real(c.Worktree) {
			return "", "", fmt.Errorf("Orca --worktree must name the exact contracted path")
		}
	} else {
		if agent == "" {
			return "", "", fmt.Errorf("unsupported provider executable %q", name)
		}
		if (name == "codex" && (len(args) == 0 || args[0] != "exec")) || (name == "kiro-cli" && (len(args) == 0 || args[0] != "chat")) || ((name == "opencode" || name == "opencode2") && (len(args) == 0 || args[0] != "run")) {
			return "", "", fmt.Errorf("unsupported %s launch subcommand", name)
		}
		for _, arg := range args {
			if arg == "--" {
				break
			}
			if arg == "--worktree" || strings.HasPrefix(arg, "--worktree=") || (name == "claude" && strings.HasPrefix(arg, "-w")) || (strings.HasPrefix(arg, "-C") && arg != "-C") {
				return "", "", fmt.Errorf("provider worktree override is outside prepared routing")
			}
		}
		for _, flag := range []string{"-C", "--cd", "--cwd", "--directory"} {
			if dir, err := launchFlag(args, flag); err != nil || (dir != "" && contract.Real(dir) != contract.Real(c.Worktree)) {
				return "", "", fmt.Errorf("provider directory override differs from contracted worktree")
			}
		}
		if name == "codex" {
			for i, arg := range args {
				if arg == "--profile" || arg == "-p" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "-p") || arg == "--oss" || strings.HasPrefix(arg, "--local-provider") {
					return "", "", fmt.Errorf("Codex profile override is outside prepared routing")
				}
				value := ""
				if arg == "-c" || arg == "--config" {
					if i+1 < len(args) {
						value = args[i+1]
					}
				} else if v, ok := strings.CutPrefix(arg, "--config="); ok {
					value = v
				} else if strings.HasPrefix(arg, "-c") && len(arg) > 2 {
					value = arg[2:]
				}
				if value != "" {
					key, _, has := strings.Cut(value, "=")
					key = strings.TrimSpace(key)
					if !has || key == "model" || key == "model_provider" || key == "profile" || strings.HasPrefix(key, "profiles.") || key == "hooks" || strings.HasPrefix(key, "hooks.") {
						return "", "", fmt.Errorf("Codex configuration changes prepared model identity")
					}
				}
			}
		}
		if name == "kiro-cli" {
			selected, err := launchFlag(args, "--agent")
			if err != nil || (selected != "" && selected != "rein") {
				return "", "", fmt.Errorf("Kiro worker agent must be rein")
			}
		}
	}
	model, err := launchFlag(args, "--model", "-m")
	if err != nil || agent == "" || model == "" {
		return "", "", fmt.Errorf("launch requires one explicit agent and model: %v", err)
	}
	if !isOrcaLaunch(argv) && (name == "opencode" || name == "opencode2") {
		model, _, err = openCodeModelVariant(argv)
		if err != nil {
			return "", "", err
		}
	}
	return agent, model, nil
}

// OpenCode V2 puts the variant in --model provider/model#variant, not a separate flag.
func openCodeModelVariant(argv []string) (string, string, error) {
	for _, arg := range argv[1:] {
		if arg == "--" {
			break
		}
		if arg == "--effort" || strings.HasPrefix(arg, "--effort=") || arg == "--variant" || strings.HasPrefix(arg, "--variant=") || arg == "--config" || strings.HasPrefix(arg, "--config=") {
			return "", "", fmt.Errorf("OpenCode V2 requires --model provider/model#variant; unsupported option %q", arg)
		}
	}
	selector, err := launchFlag(argv[1:], "--model", "-m")
	if err != nil {
		return "", "", err
	}
	model, variant, hasVariant := strings.Cut(selector, "#")
	if !hasVariant {
		return selector, "", nil
	}
	provider, id, slash := strings.Cut(model, "/")
	if !slash || provider == "" || id == "" || variant == "" || strings.ContainsRune(variant, '#') || strings.IndexFunc(selector, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", "", fmt.Errorf("invalid OpenCode V2 model variant selector %q", selector)
	}
	return model, variant, nil
}

// launchEffort returns the reasoning effort an argv asks for: --effort or --variant (claude, agy, Orca worker-start), OpenCode V2's model suffix,
// or Codex's model_reasoning_effort configuration. "" means it names none; two different values are an error.
func launchEffort(argv []string) (string, error) {
	if name := filepath.Base(argv[0]); !isOrcaLaunch(argv) && (name == "opencode" || name == "opencode2") {
		_, variant, err := openCodeModelVariant(argv)
		return variant, err
	}
	norm := func(v string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)) }
	args := argv[1:]
	flagVal, err := launchFlag(args, "--effort", "--variant")
	if err != nil {
		return "", err
	}
	flagVal, cfgVal := norm(flagVal), ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		var kv string
		switch {
		case (args[i] == "-c" || args[i] == "--config") && i+1 < len(args):
			kv = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			kv = strings.TrimPrefix(args[i], "--config=")
		case strings.HasPrefix(args[i], "-c") && len(args[i]) > 2:
			kv = args[i][2:]
		}
		if k, v, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) == "model_reasoning_effort" {
			if v = norm(v); cfgVal != "" && cfgVal != v {
				return "", fmt.Errorf("conflicting model_reasoning_effort values")
			}
			cfgVal = norm(v)
		}
	}
	switch {
	case flagVal != "" && cfgVal != "" && flagVal != cfgVal:
		return "", fmt.Errorf("conflicting effort values %q and %q", flagVal, cfgVal)
	case cfgVal != "":
		return cfgVal, nil
	}
	return flagVal, nil
}

// The full stdout still reaches the caller; oversized receipts retain the uncertain lease.
type dispatchReceiptBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *dispatchReceiptBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func (b *dispatchReceiptBuffer) identity() (string, string, bool) {
	task, dispatch, _, ready := b.workerIdentity()
	return task, dispatch, ready
}

func (b *dispatchReceiptBuffer) workerIdentity() (string, string, string, bool) {
	if b.overflow {
		return "", "", "", false
	}
	var receipt struct {
		Error  json.RawMessage `json:"error"`
		OK     bool            `json:"ok"`
		Result struct {
			State    string `json:"state"`
			Task     string `json:"taskId"`
			Dispatch string `json:"dispatchId"`
			Effects  []struct {
				Kind  string `json:"kind"`
				Role  string `json:"role"`
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"effects"`
		} `json:"result"`
	}
	if json.Unmarshal(b.Bytes(), &receipt) != nil || (len(receipt.Error) != 0 && string(bytes.TrimSpace(receipt.Error)) != "null") || !receipt.OK || receipt.Result.State != "ready" || strings.TrimSpace(receipt.Result.Task) == "" || strings.TrimSpace(receipt.Result.Dispatch) == "" {
		return "", "", "", false
	}
	handle := ""
	for _, effect := range receipt.Result.Effects {
		if effect.Kind == "dispatch_input" && effect.Role == "agent" && effect.State == "accepted" && strings.TrimSpace(effect.ID) != "" {
			if strings.TrimSpace(effect.ID) != effect.ID || (handle != "" && handle != effect.ID) {
				return "", "", "", false
			}
			handle = effect.ID
		}
	}
	return receipt.Result.Task, receipt.Result.Dispatch, handle, handle != ""
}

func validateRouteQuotaScope(d *routing.Decision, argv []string) error {
	if !isOrcaLaunch(argv) {
		return nil
	}
	cfg, err := providers.Load(d.ConfigPath)
	if err != nil {
		return err
	}
	if !cfg.RequireOrcaQuota {
		return nil
	}
	// The account-list snapshot is scoped locally, not to worker-start selectors.
	for _, name := range []string{"--environment", "--pairing-code", "--on"} {
		target, err := launchFlag(argv[1:], name)
		if err != nil {
			return errors.New("quota precheck: invalid Orca endpoint selector")
		}
		if target != "" {
			return errors.New("quota precheck: explicit Orca endpoint is not bound to the local quota snapshot")
		}
	}
	return nil
}
