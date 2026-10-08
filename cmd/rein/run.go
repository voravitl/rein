package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

const userOnlyMsg = "[run] %s is for the user: run it in your own terminal, not from a Claude Code session (the coordinator never authorizes itself)\n"

// splitRepo takes the optional leading <repo> argument (default "."), so both `run start <repo> --run x` and
// `run start --run x <repo>` work.
func splitRepo(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return ".", args
}

func csv(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// cmdRun is `rein run start|end|resume|allow|audit` (ADR 0002 B0). allow and end --abandon are user-only: they
// refuse when this process runs inside a Claude Code tool call.
func cmdRun(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	sub := args[0]
	repo, rest := splitRepo(args[1:])
	fs := flag.NewFlagSet("run "+sub, flag.ContinueOnError)
	name := fs.String("run", "", "run name (start)")
	profile := fs.String("profile", "", "project profile JSON (start; default $REIN_PROFILE)")
	session := fs.String("session", "", "owner session id (default $CLAUDE_CODE_SESSION_ID)")
	pid := fs.Int("pid", 0, "owner process id (default $CLAUDE_PID)")
	abandon := fs.Bool("abandon", false, "end: close the run even with drift (user-only)")
	reason := fs.String("reason", "", "allow/end --abandon: why (recorded)")
	task := fs.String("task", "", "allow: task whose Agent call may run (prompt names rein-task: <t>)")
	commit := fs.String("commit", "", "allow: commit audit accepts")
	pinned := fs.String("pinned", "", "end/audit: comma shas the drift check approved")
	asJSON := fs.Bool("json", false, "audit/end: JSON output")
	if fs.Parse(rest) != nil {
		return 2
	}
	if fs.NArg() > 0 && repo == "." {
		repo = fs.Arg(0)
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "[run]", err)
		return 2
	}
	switch sub {
	case "start":
		if *name == "" {
			fmt.Fprintln(os.Stderr, "[run] --run is required")
			return 2
		}
		prof, err := contract.LoadProfile(*profile)
		if err != nil {
			return fail(fmt.Errorf("profile: %w", err))
		}
		who := run.Owner{SessionID: *session, PID: *pid}
		if who.SessionID == "" || who.PID == 0 {
			env, err := run.OwnerFromEnv()
			if err != nil {
				return fail(err)
			}
			if who.SessionID == "" {
				who.SessionID = env.SessionID
			}
			if who.PID == 0 {
				who.PID = env.PID
			}
		}
		m, err := run.Start(repo, *name, prof, who)
		if err != nil {
			return fail(err)
		}
		// Set RunDir in the marker (ADR 0002 B4)
		if loc, ok := run.Find(repo); ok {
			runDir := run.RunDir(*name)
			if err := run.SetRunDir(loc.Marker, runDir); err != nil {
				fmt.Fprintf(os.Stderr, "[run] warning: cannot set run dir in marker: %v\n", err)
			}
		}
		fmt.Printf("[run] %s started in %s at %.12s; owner session %s, pid %d\n[run] the coordinator guard is active from the next tool call: writes only to %s\n",
			m.Run, m.Root, m.StartSHA, m.SessionID, m.PID, strings.Join(m.CoordinatorWritable, ", "))
	case "tick":
		if err := run.Tick(repo); err != nil {
			return fail(err)
		}
		// Silent success (single-flight under flock, may skip if another is running)
	case "resume":
		sid, p := *session, *pid
		if sid == "" {
			sid = os.Getenv("CLAUDE_CODE_SESSION_ID")
		}
		if p == 0 {
			p, _ = strconv.Atoi(os.Getenv("CLAUDE_PID"))
		}
		m, err := run.Resume(repo, sid, p)
		if err != nil {
			return fail(err)
		}
		fmt.Printf("[run] %s now follows session %s\n", m.Run, m.SessionID)
	case "allow":
		if run.UnderClaude() {
			fmt.Fprintf(os.Stderr, userOnlyMsg, "rein run allow")
			return 2
		}
		var m *run.Marker
		var err error
		switch {
		case *task != "" && *commit == "":
			m, err = run.Allow(repo, *task, *reason)
		case *commit != "" && *task == "":
			m, err = run.AllowCommit(repo, *commit, *reason)
		default:
			return fail(errors.New("give exactly one of --task and --commit"))
		}
		if err != nil {
			return fail(err)
		}
		fmt.Printf("[run] %s: %d allowance(s) recorded\n", m.Run, len(m.Allowed))
	case "audit", "end":
		if sub == "end" && *abandon && run.UnderClaude() {
			fmt.Fprintf(os.Stderr, userOnlyMsg, "rein run end --abandon")
			return 2
		}
		o := run.AuditOptions{Pinned: csv(*pinned)}
		var (
			findings []run.Finding
			closed   bool
			runName  string
		)
		if sub == "audit" {
			f, err := run.Audit(repo, o)
			if err != nil {
				return fail(err)
			}
			findings = f
		} else {
			res, err := run.End(repo, *abandon, *reason, o)
			if err != nil {
				return fail(err)
			}
			findings, closed, runName = res.Findings, res.Closed, res.Run
		}
		if *asJSON {
			b, _ := json.MarshalIndent(map[string]any{"closed": closed, "findings": findings}, "", "  ")
			fmt.Println(string(b))
		} else {
			for _, f := range findings {
				fmt.Printf("DRIFT %-17s %s\n", f.Kind, f.Detail)
			}
			switch {
			case closed && *abandon:
				fmt.Printf("[run] %s abandoned: %s\n", runName, *reason)
			case closed:
				fmt.Printf("[run] %s ended: no coordinator drift\n", runName)
			case sub == "end":
				fmt.Printf("[run] %s stays open: %d drift finding(s); fix or record them (the user: `rein run allow --commit <sha> --reason <text>`, or `rein run end --abandon --reason <text>`)\n", runName, len(findings))
			default:
				fmt.Printf("[run] audit: %d drift finding(s)\n", len(findings))
			}
		}
		if len(findings) > 0 {
			return 1
		}
	default:
		usage()
		return 2
	}
	return 0
}
