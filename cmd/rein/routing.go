package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
)

func cmdRoute(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "route: auto | prepare | check | launch | settle | holds | calibrate | reconcile | discover | cooldown | clear | status")
		return 2
	}
	switch args[0] {
	case "launch":
		return cmdRouteLaunch(args[1:])
	case "auto":
		return cmdRouteAuto(args[1:])
	case "settle":
		return cmdRouteSettle(args[1:])
	case "reconcile":
		return cmdRouteReconcile(args[1:])
	case "calibrate":
		return cmdRouteCalibrate(args[1:])
	case "discover":
		return cmdRouteDiscover(args[1:])
	case "holds":
		holds, err := budget.Outstanding()
		if err != nil {
			fmt.Fprintln(os.Stderr, "[route]", err)
			return 1
		}
		return printJSON(holds)
	}
	fs := flag.NewFlagSet("route "+args[0], flag.ContinueOnError)
	phase := fs.String("phase", "worker", "worker or review launch")
	worktree := fs.String("worktree", "", "exact launch worktree")
	task := fs.String("task", "", "contract name")
	run := fs.String("run", "", "explicit run ID")
	chain := fs.String("chain", "", "worker:<type> or review:<name>")
	cfg := fs.String("config", "", "fallback-chain.json")
	worker := fs.String("worker-model", "", "reviewed worker model")
	agent := fs.String("agent", "", "launch harness")
	model := fs.String("model", "", "explicit launch model")
	effort := fs.String("effort", "", "check: the reasoning effort the launch will run at; an automatic receipt binds it")
	skip := fs.Bool("skip-claude", false, "skip Claude CLI pool")
	timeout := fs.Duration("timeout", 90*time.Second, "per-provider probe timeout")
	provider := fs.String("provider", "", "provider name")
	until := fs.String("until", "", "known reset RFC3339; omitted means manual clear required")
	reason := fs.String("reason", "", "quota evidence")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	var out any
	var err error
	switch args[0] {
	case "prepare", "check":
		var c *contract.Contract
		c, err = contract.Load(*task)
		if err != nil {
			break
		}
		if args[0] == "check" && *phase != "worker" && *phase != "review" {
			err = fmt.Errorf("phase must be worker or review")
			break
		}
		if err = routeBudget(c, *run); err != nil {
			break
		}
		if args[0] == "prepare" {
			if err = routing.ManualPrepareAllowed(c); err != nil {
				break
			}
		}
		var d *routing.Decision
		if args[0] == "prepare" {
			d, err = routing.Prepare(c, *run, *chain, *worker, *cfg, *skip, *timeout)
		} else {
			d, err = routing.Validate(c, *run, *agent, *model)
			if err == nil && !strings.HasPrefix(d.Chain, *phase+":") {
				err = fmt.Errorf("routing phase does not match receipt")
			}
			if err == nil && *worktree != "" {
				actual, e := filepath.EvalSymlinks(*worktree)
				expected, e2 := filepath.EvalSymlinks(d.Worktree)
				if e != nil || e2 != nil || actual != expected {
					err = fmt.Errorf("launch worktree differs from routing receipt")
				}
			}
			if err == nil && d.Attempt != "" {
				// an automatic receipt was chosen on evidence for one effort: run exactly it, once
				if want := strings.ToLower(d.Effort); strings.ToLower(strings.TrimSpace(*effort)) != want {
					err = fmt.Errorf("launch effort %q differs from the prepared effort %q: pass --effort %s", *effort, d.Effort, d.Effort)
				} else if *phase == "review" {
					err = routing.ConsumeReview(c, d) // one receipt, one review: a retry is a new decision, refresh and reservation
				}
			}
			if err == nil {
				err = routing.RecordLaunch(d)
			}
		}
		out = d
	case "status":
		out, err = routing.Status()
	case "cooldown", "clear":
		var reset *time.Time
		if *until != "" {
			var parsed time.Time
			parsed, err = time.Parse(time.RFC3339, *until)
			if err != nil {
				break
			}
			reset = &parsed
		}
		err = routing.SetCooldown(*cfg, *provider, *reason, reset, args[0] == "clear")
		out = map[string]any{"provider": *provider, "cleared": args[0] == "clear", "until": reset}
	default:
		fmt.Fprintln(os.Stderr, "route: unknown subcommand")
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[route]", err)
		return 1
	}
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}
