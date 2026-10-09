package main

// Automatic selection commands (docs/ROUTING_SELECTION_DESIGN.md): one prepare entry point, and the commands around it.
//
//	rein route auto      --task T --run R --profile-file F [--config F] [--timeout 90s] [--skip-claude] [--worker-model M] [--base REF]
//	rein route settle    --attempt A
//	rein route reconcile --hold H --reason TEXT [--used pool=amount ...]        user-only
//	rein route calibrate --task T --run R --provider P --amount N [--calls 1]
//	rein route discover  [--config F] [--timeout 60s] [--dir D] [--json]

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

const maxProfileBytes = 1 << 20

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

// routeScope resolves, the way routeBudget does, the owner's profile, the profile whose budget applies and the run marker.
func routeScope(c *contract.Contract, runID string) (owner, budgetProf *contract.Profile, marker string, err error) {
	if owner, err = contract.LoadProfile(""); err != nil {
		return
	}
	if budgetProf = owner; budgetProf.Budget == nil {
		budgetProf = &c.Profile
	}
	if loc, found := run.Find(c.Worktree); found {
		m, e := run.Load(loc.Marker)
		if e != nil || m.Run != runID {
			err = fmt.Errorf("routing Run differs from current Run marker")
			return
		}
		marker = loc.Marker
	}
	return
}

func cmdRouteAuto(args []string) int {
	fs := flag.NewFlagSet("route auto", flag.ContinueOnError)
	task := fs.String("task", "", "contract name")
	runID := fs.String("run", "", "explicit run ID")
	profile := fs.String("profile-file", "", "the coordinator's task profile (JSON); it cannot name a model, provider, harness or chain")
	cfg := fs.String("config", "", "fallback-chain.json")
	worker := fs.String("worker-model", "", "review only: the worker model you believe ran; refused unless the ledger records the same")
	base := fs.String("base", "", "review only: the git ref the checkout's actual changes are taken against (default origin/main)")
	skip := fs.Bool("skip-claude", false, "skip the Claude CLI pool")
	timeout := fs.Duration("timeout", 90*time.Second, "per-probe and per-harness refresh timeout")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *task == "" || *runID == "" || *profile == "" {
		fmt.Fprintln(os.Stderr, "[route] auto requires --task T --run R --profile-file F")
		return 2
	}
	f, err := os.Open(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	f.Close()
	if err != nil || len(raw) > maxProfileBytes {
		fmt.Fprintln(os.Stderr, "[route] task profile unreadable or larger than 1 MiB")
		return 1
	}
	c, err := contract.Load(*task)
	if err == nil {
		err = routeBudget(c, *runID)
	}
	var owner, budgetProf *contract.Profile
	var marker string
	if err == nil {
		owner, budgetProf, marker, err = routeScope(c, *runID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	// Ctrl-C and SIGTERM cancel the refresh; every process a discovery adapter started is gone before this returns
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := routing.PrepareAuto(ctx, routing.AutoInput{Contract: c, Run: *runID, TaskProfile: raw, ConfigPath: *cfg, Owner: owner, Budget: budgetProf,
		MarkerPath: marker, WorkerModel: *worker, Base: *base, SkipClaude: *skip, Timeout: *timeout})
	if res != nil {
		printJSON(res)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	return 0
}

func cmdRouteSettle(args []string) int {
	fs := flag.NewFlagSet("route settle", flag.ContinueOnError)
	attempt := fs.String("attempt", "", "attempt ID from route auto")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *attempt == "" {
		fmt.Fprintln(os.Stderr, "[route] settle requires --attempt A")
		return 2
	}
	holds, err := routing.SettleAttempt(*attempt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	return printJSON(map[string]any{"attempt": *attempt, "settled": holds})
}

// usedFlag collects repeated --used pool=amount values.
type usedFlag map[string]float64

func (u usedFlag) String() string { return "" }
func (u usedFlag) Set(s string) error {
	pool, v, ok := strings.Cut(s, "=")
	amount, err := strconv.ParseFloat(v, 64)
	if !ok || pool == "" || err != nil || amount < 0 {
		return fmt.Errorf("%q: want pool=amount with amount >= 0", s)
	}
	u[pool] = amount
	return nil
}

func cmdRouteReconcile(args []string) int {
	if run.UnderClaude() {
		fmt.Fprintln(os.Stderr, "[route reconcile] this command releases reserved funds: the user runs it in their own terminal, not Claude Code")
		return 2
	}
	fs := flag.NewFlagSet("route reconcile", flag.ContinueOnError)
	hold := fs.String("hold", "", "hold ID (see `rein route status` holds)")
	reason := fs.String("reason", "", "evidence for the amounts (required)")
	used := usedFlag{}
	fs.Var(used, "used", "pool=amount actually used; pools not named are charged at their reserved bound (repeatable)")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *hold == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, "[route] reconcile requires --hold H --reason TEXT [--used pool=amount ...]")
		return 2
	}
	// the hold's owner is the already-exited `route auto`, so "owner gone" proves nothing about the worker: the task's checkout
	// must have no writer that cannot be proven gone either
	if holds, err := budget.Outstanding(); err == nil {
		for _, h := range holds {
			if h.ID == *hold && h.Task != "" {
				if why := routing.TaskBusy(h.Task); why != "" {
					fmt.Fprintf(os.Stderr, "[route] task %s still has a writer: %s; funds are released only once the worker is gone\n", h.Task, why)
					return 1
				}
			}
		}
	}
	h, err := budget.Reconcile(*hold, used, *reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	return printJSON(h)
}

func cmdRouteCalibrate(args []string) int {
	fs := flag.NewFlagSet("route calibrate", flag.ContinueOnError)
	task := fs.String("task", "", "contract name")
	runID := fs.String("run", "", "explicit run ID")
	provider := fs.String("provider", "", "provider to calibrate")
	cfg := fs.String("config", "", "fallback-chain.json")
	amount := fs.Float64("amount", -1, "worst case of the call in the provider's billing unit")
	calls := fs.Int("calls", 1, "worst-case model calls")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *task == "" || *runID == "" || *provider == "" || *amount < 0 {
		fmt.Fprintln(os.Stderr, "[route] calibrate requires --task T --run R --provider P --amount N")
		return 2
	}
	c, err := contract.Load(*task)
	var owner, budgetProf *contract.Profile
	var marker string
	if err == nil {
		owner, budgetProf, marker, err = routeScope(c, *runID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	h, err := routing.ReserveCalibration(routing.CalibrationInput{Contract: c, Run: *runID, ConfigPath: *cfg, Owner: owner, Budget: budgetProf,
		MarkerPath: marker, Provider: *provider, Amount: *amount, Calls: *calls})
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	return printJSON(h)
}

func cmdRouteDiscover(args []string) int {
	fs := flag.NewFlagSet("route discover", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "fallback-chain.json")
	timeout := fs.Duration("timeout", 60*time.Second, "per-harness timeout")
	dir := fs.String("dir", "", "project directory the catalogs are scoped to (default: current)")
	asJSON := fs.Bool("json", false, "full inventories as JSON")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	cfg, err := providers.Load(providers.ConfigPath(*cfgPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	invs := providers.Discover(ctx, cfg, providers.DiscoverOptions{Dir: *dir, Timeout: *timeout})
	if *asJSON {
		return printJSON(invs)
	}
	bad := false
	for _, i := range invs {
		fmt.Printf("%-12s %-11s %-14s v=%-8s models=%-4d limits=%d scope=%s %s\n", i.Harness, i.Status, i.Freshness, i.Version, len(i.Models), len(i.Limits), i.Scope, i.Reason)
		bad = bad || i.Status != providers.InvComplete
	}
	if bad {
		return 1 // at least one harness would be excluded from automatic selection
	}
	return 0
}
