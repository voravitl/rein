// rein keeps pipeline worker models on task.
//
//	rein hook [--vendor V]          worker hook (claude|codex|agy|kiro|opencode); reads the event on stdin
//	rein hooks install <task>       write the codex/agy/kiro/opencode (and claude) hook files into the worktree
//	rein contract new|show|path     task contract the hook and the drift check enforce
//	rein drift <task> [...]         judge a worker's result against its contract (exit 0 ok, 1 drift, 2 cannot judge)
//	rein run start|end|resume|allow|audit   coordinator run guard (allow and end --abandon are user-only)
//	rein ledger add|call|report|suggest
//	rein providers [--chain worker:<type>|review:<k>] [--only a,b] [--skip-claude]
//	rein version
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/drift"
	"github.com/voravitl/rein/internal/guard"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/sandbox"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		os.Exit(cmdHook(os.Args[2:]))
	case "hooks":
		os.Exit(cmdHooks(os.Args[2:]))
	case "contract":
		os.Exit(cmdContract(os.Args[2:]))
	case "drift":
		os.Exit(cmdDrift(os.Args[2:]))
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "ledger":
		os.Exit(cmdLedger(os.Args[2:]))
	case "providers":
		os.Exit(cmdProviders(os.Args[2:]))
	case "sandbox":
		os.Exit(cmdSandbox(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println("rein", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: rein <hook|hooks|contract|sandbox|drift|run|ledger|providers|version> [args]
  rein hook [--vendor claude|codex|agy|kiro|opencode]     (default claude; stdin = the vendor's hook event)
  rein hooks install <name> [--vendors codex,agy,kiro,opencode,claude]   hook files in the worktree + launch flags
  rein contract new --name N --run-dir D --allow 'g1,g2' --scope S1,S2 [--profile P] [--issue 169] [--deny g] [--worktree-root R] [--report-path P] [--writable f1,f2] [--max-changed-lines N]
  rein contract show|path <name>
  rein drift <name> [--worktree P] [--base origin/main] [--claimed-files a,b] [--expect-guard codex|agy|kiro|opencode|claude] [--json]
  rein run start [repo] --run NAME [--profile P] [--session S --pid N]    (coordinator guard on; owner from CLAUDE_CODE_SESSION_ID / CLAUDE_PID)
  rein run end [repo] [--pinned sha,..] | rein run audit [repo] [--pinned sha,..] [--json]   (exit 1 = COORDINATOR_DRIFT)
  rein run resume [repo] [--session S --pid N]
  rein run allow [repo] (--task T | --commit SHA) --reason R | rein run end [repo] --abandon --reason R    (user-only: refused inside Claude Code)
  rein ledger add --task T --type backend --worker codex:gpt-6.1-sol --rounds 3 [--approved] ...
  rein ledger call --role critic --provider codex --model gpt-6.1-sol [--tokens N] [--credits X] [--cost-usd X]
  rein ledger report [--type T] [--since ISO] | suggest [--min-n 3]
  rein sandbox <name>     write the OS sandbox settings into the worker's worktree (before the worker starts)
  rein providers [--chain worker:backend] [--only a,b] [--skip-claude] [--timeout 90s] [--config F] [--json]`)
}

// cmdHook never crashes the vendor's session: a panic means "no decision" (exit 0). A bad flag exits 1, which
// no vendor treats as a block.
func cmdHook(args []string) (code int) {
	defer func() {
		if recover() != nil {
			code = 0
		}
	}()
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vendor := fs.String("vendor", "claude", strings.Join(guard.Vendors, "|"))
	task := fs.String("task", "", "bind this hook to one task's contract (set by rein hooks install)")
	if fs.Parse(args) != nil || !oneOf(*vendor, guard.Vendors) {
		fmt.Fprintf(os.Stderr, "[hook] --vendor must be one of %s\n", strings.Join(guard.Vendors, ", "))
		return 1
	}
	return guard.RunTask(*vendor, *task, os.Stdin, os.Stdout, os.Stderr)
}

func cmdHooks(args []string) int {
	if len(args) < 2 || args[0] != "install" || strings.HasPrefix(args[1], "-") {
		usage()
		return 2
	}
	fs := flag.NewFlagSet("hooks install", flag.ContinueOnError)
	vendors := fs.String("vendors", strings.Join(hooks.Default, ","), "comma list of "+strings.Join(hooks.All, "|"))
	if fs.Parse(args[2:]) != nil {
		return 2
	}
	c, err := contract.Load(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "[hooks]", err)
		return 2
	}
	bin, warn, err := hooks.BinaryPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[hooks] cannot resolve the rein binary:", err)
		return 2
	}
	if warn != "" {
		fmt.Fprintln(os.Stderr, "[hooks] warning:", warn)
	}
	var list []string
	for _, v := range strings.Split(*vendors, ",") {
		if v = strings.TrimSpace(v); v != "" {
			list = append(list, v)
		}
	}
	res, saved, err := hooks.Install(c, bin, list)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[hooks]", err)
		return 2
	}
	code := 0
	for _, r := range res {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "[hooks] %s: FAILED: %v\n", r.Vendor, r.Err)
			code = 2
			continue
		}
		fmt.Printf("[hooks] %s: wrote %s (kept out of git status via info/exclude)\n        launch: %s\n", r.Vendor, strings.Join(r.Files, ", "), r.Launch)
	}
	if code == 0 {
		fmt.Printf("[hooks] recorded in the contract (%s): %s. `rein drift %s` now reports GUARD_INACTIVE if no hook call of this install shows up in %s\n",
			strings.Join(saved, " and "), strings.Join(c.HooksInstalled, ","), c.Name, contract.SeenPath(c.Name))
		if len(saved) < 2 {
			fmt.Println("[hooks] note: the durable run-dir copy of the contract was NOT updated (this contract has no run_copy and its run dir could not be derived); only the index copy has the hooks")
		}
	}
	return code
}

func cmdContract(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "new":
		fs := flag.NewFlagSet("contract new", flag.ContinueOnError)
		name := fs.String("name", "", "task / worktree name")
		issue := fs.Int("issue", 0, "issue number")
		run := fs.String("run-dir", "", "durable run dir")
		allow := fs.String("allow", "", "comma globs the task may edit")
		deny := fs.String("deny", "", "extra never-edit globs")
		scope := fs.String("scope", "", "scope ids, e.g. S1,S2")
		root := fs.String("worktree-root", "", "dir that holds the worker worktrees (default: the profile's worktree_root)")
		profile := fs.String("profile", "", "project profile JSON (default $REIN_PROFILE)")
		report := fs.String("report-path", "", "default <run>/reports/<name>.md")
		writable := fs.String("writable", "", "extra exact files outside the worktree the worker may write")
		maxLines := fs.Int("max-changed-lines", 0, "size budget (warning only)")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		if *name == "" || *run == "" {
			fmt.Fprintln(os.Stderr, "[contract] --name and --run-dir are required")
			return 2
		}
		prof, err := contract.LoadProfile(*profile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[contract] profile:", err)
			return 2
		}
		c, err := contract.New(*name, *issue, *run, *allow, *deny, *scope, *root, *report, *writable, *maxLines, prof)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[contract]", err)
			return 2
		}
		fmt.Printf("[contract] written: %s (+ run copy)\n\n%s\n", contract.PathOf(c.Name), c.Snippet())
	case "show", "path":
		if len(args) < 2 {
			usage()
			return 2
		}
		if args[0] == "path" {
			fmt.Println(contract.PathOf(args[1]))
			return 0
		}
		c, err := contract.Load(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "[contract]", err)
			return 2
		}
		fmt.Println(c.Snippet())
	default:
		usage()
		return 2
	}
	return 0
}

func cmdDrift(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		usage()
		return 2
	}
	name := args[0]
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	wt := fs.String("worktree", "", "override the contract's worktree")
	base := fs.String("base", "origin/main", "base ref")
	claimed := fs.String("claimed-files", "", "worker_done filesModified, comma separated")
	asJSON := fs.Bool("json", false, "JSON output")
	expect := fs.String("expect-guard", "", "vendor whose guard hook was expected for this launch: no matching seen-log line is drift")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	c, err := contract.Load(name)
	if err != nil {
		fmt.Printf("[drift] %v: cannot judge\n", err)
		return 2
	}
	tree := c.Worktree
	if *wt != "" {
		tree = *wt
	}
	var cl []string
	for _, f := range strings.Split(*claimed, ",") {
		if f = strings.TrimSpace(f); f != "" {
			cl = append(cl, f)
		}
	}
	r, err := drift.CheckWith(c, tree, *base, cl, drift.Options{ExpectGuard: *expect})
	if err != nil {
		fmt.Printf("[drift] cannot judge: %v\n", err)
		return 2
	}
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
	} else {
		for _, f := range r.Drift {
			fmt.Printf("DRIFT %-17s %s\n", f.Kind, f.Detail)
		}
		for _, f := range r.Warnings {
			fmt.Printf("warn  %-17s %s\n", f.Kind, f.Detail)
		}
		ok := ""
		if len(r.Drift) == 0 {
			ok = " -> OK to gate"
		}
		fmt.Printf("[drift] %s @ %.12s: %d drift, %d warnings; %d commits, %d files%s\n", r.Name, r.Head, len(r.Drift), len(r.Warnings), r.Commits, r.Files, ok)
	}
	if len(r.Drift) > 0 {
		return 1
	}
	return 0
}

// optional numeric flags: only set when given, so the ledger can tell "0" from "unknown".
type optInt struct{ p **int }

func (o optInt) String() string { return "" }
func (o optInt) Set(s string) error {
	var v int
	if _, err := fmt.Sscan(s, &v); err != nil {
		return err
	}
	*o.p = &v
	return nil
}

type optFloat struct{ p **float64 }

func (o optFloat) String() string { return "" }
func (o optFloat) Set(s string) error {
	var v float64
	if _, err := fmt.Sscan(s, &v); err != nil {
		return err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fmt.Errorf("%q is not a finite non-negative number", s)
	}
	*o.p = &v
	return nil
}

func cmdLedger(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "add":
		var r ledger.Row
		r.Kind = "task"
		fs := flag.NewFlagSet("ledger add", flag.ContinueOnError)
		fs.StringVar(&r.Task, "task", "", "")
		fs.StringVar(&r.Type, "type", "", strings.Join(ledger.Types, "|"))
		worker := fs.String("worker", "", "agent:model")
		reviewer := fs.String("reviewer", "", "agent:model")
		fs.Var(optInt{&r.ReviewRounds}, "rounds", "review rounds until APPROVE")
		fs.BoolVar(&r.Approved, "approved", false, "")
		gp := fs.Bool("first-gate-pass", false, "")
		gf := fs.Bool("first-gate-fail", false, "")
		fs.Var(optInt{&r.Blockers}, "blockers", "")
		fs.Var(optInt{&r.Highs}, "highs", "")
		fs.Var(optInt{&r.FalseClaims}, "false-claims", "")
		fs.Var(optInt{&r.Drift}, "drift", "")
		fs.Var(optInt{&r.GuardDenials}, "guard-denials", "")
		fs.Var(optFloat{&r.Minutes}, "minutes", "")
		fs.Var(optInt{&r.WorkerTokens}, "worker-tokens", "")
		fs.Var(optInt{&r.ReviewerTokens}, "reviewer-tokens", "")
		fs.Var(optFloat{&r.CostUSD}, "cost-usd", "")
		fs.Var(optFloat{&r.Credits}, "credits", "")
		fs.Var(optInt{&r.Issue}, "issue", "")
		fs.Var(optInt{&r.MR}, "mr", "")
		fs.StringVar(&r.Fallback, "fallback", "", "")
		risk := fs.String("risk", "", "comma list")
		fs.StringVar(&r.Run, "run", "", "")
		fs.StringVar(&r.Notes, "notes", "", "")
		fs.StringVar(&r.Source, "source", "measured", "measured|memory")
		fs.BoolVar(&r.Approx, "approx", false, "")
		if fs.Parse(args) != nil {
			return 2
		}
		if r.Task == "" || r.ReviewRounds == nil || *worker == "" || !oneOf(r.Type, ledger.Types) {
			fmt.Fprintln(os.Stderr, "[ledger] --task, --type, --worker and --rounds are required")
			return 2
		}
		var err error
		if r.Worker, err = ledger.ParseAgentModel(*worker); err != nil {
			fmt.Fprintln(os.Stderr, "[ledger] --worker:", err)
			return 2
		}
		if *reviewer != "" {
			if r.Reviewer, err = ledger.ParseAgentModel(*reviewer); err != nil {
				fmt.Fprintln(os.Stderr, "[ledger] --reviewer:", err)
				return 2
			}
		}
		if *gp || *gf {
			v := *gp
			r.FirstGatePass = &v
		}
		for _, x := range strings.Split(*risk, ",") {
			if x = strings.TrimSpace(x); x != "" {
				r.Risk = append(r.Risk, x)
			}
		}
		return save(r)
	case "call":
		var r ledger.Row
		r.Kind = "call"
		fs := flag.NewFlagSet("ledger call", flag.ContinueOnError)
		fs.StringVar(&r.Role, "role", "", "")
		fs.StringVar(&r.Provider, "provider", "codex", "")
		fs.StringVar(&r.Model, "model", "", "")
		fs.StringVar(&r.Purpose, "purpose", "", "")
		fs.Var(optInt{&r.Tokens}, "tokens", "")
		fs.Var(optFloat{&r.Credits}, "credits", "")
		fs.Var(optFloat{&r.CostUSD}, "cost-usd", "")
		fs.Var(optFloat{&r.Minutes}, "minutes", "")
		fs.StringVar(&r.Run, "run", "", "")
		failed := fs.Bool("failed", false, "")
		if fs.Parse(args) != nil {
			return 2
		}
		if r.Role == "" || r.Model == "" {
			fmt.Fprintln(os.Stderr, "[ledger] --role and --model are required")
			return 2
		}
		if strings.Contains(r.Model, "fable") {
			fmt.Fprintln(os.Stderr, "[ledger] fable is never used (user rule)")
			return 2
		}
		ok := !*failed
		r.OK = &ok
		return save(r)
	case "report", "suggest":
		fs := flag.NewFlagSet("ledger "+sub, flag.ContinueOnError)
		typ := fs.String("type", "", "")
		since := fs.String("since", "", "ISO date")
		minN := fs.Int("min-n", 3, "")
		if fs.Parse(args) != nil {
			return 2
		}
		rows, err := ledger.Load(*since)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[ledger]", err)
			return 2
		}
		if sub == "report" {
			ledger.Report(os.Stdout, rows, *typ)
		} else {
			ledger.Suggest(os.Stdout, rows, *minN)
		}
	default:
		usage()
		return 2
	}
	return 0
}

func save(r ledger.Row) int {
	if err := ledger.Append(r); err != nil {
		fmt.Fprintln(os.Stderr, "[ledger]", err)
		return 2
	}
	fmt.Printf("[ledger] %s recorded -> %s\n", r.Kind, ledger.Path())
	return 0
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func cmdProviders(args []string) int {
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "fallback-chain.json")
	chain := fs.String("chain", "", "worker:<type> or review:<key>")
	only := fs.String("only", "", "comma provider names")
	skipClaude := fs.Bool("skip-claude", false, "do not spend Claude quota on probes")
	timeout := fs.Duration("timeout", 90*time.Second, "per-probe timeout")
	asJSON := fs.Bool("json", false, "")
	if fs.Parse(args) != nil {
		return 2
	}
	c, err := providers.Load(providers.ConfigPath(*cfgPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "[providers]", err)
		return 2
	}
	var names []string
	if *chain != "" {
		order, ok := c.Chains()[*chain]
		if !ok {
			fmt.Fprintf(os.Stderr, "[providers] unknown chain %s\n", *chain)
			return 2
		}
		names = append(names, order...)
	} else {
		for n := range c.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	if *only != "" {
		want := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			want[strings.TrimSpace(n)] = true
		}
		var keep []string
		for _, n := range names {
			if want[n] {
				keep = append(keep, n)
			}
		}
		names = keep
	}
	if *skipClaude {
		var keep []string
		for _, n := range names {
			if c.Providers[n].Agent != "claude" {
				keep = append(keep, n)
			}
		}
		names = keep
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "[providers] nothing to probe")
		return 2
	}
	res := providers.Check(c, names, *timeout)
	picks := providers.Pick(c, res, *chain)
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"providers": res, "first_available": picks}, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	providers.Print(os.Stdout, c, names, res, picks)
	for _, v := range picks {
		if v == "" {
			return 1 // at least one chain has nothing available
		}
	}
	return 0
}

func cmdSandbox(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		usage()
		return 2
	}
	c, err := contract.Load(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "[sandbox]", err)
		return 2
	}
	path, note, err := sandbox.Apply(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[sandbox]", err)
		return 2
	}
	fmt.Printf("[sandbox] written: %s (kept out of git status via info/exclude)\n", path)
	if note != "" {
		fmt.Println("[sandbox] " + note)
	}
	return 0
}
