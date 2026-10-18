package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/hunter"
)

func cmdHunter(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rein hunter report|scan-report|get|list|reproduce|triage|authorize-fix|start-fix|verify|review|approve")
		return 2
	}
	fs := flag.NewFlagSet("hunter "+args[0], flag.ContinueOnError)
	var result any
	var err error
	switch args[0] {
	case "report":
		input := fs.String("input", "", "finding JSON file")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *input == "" {
			return 2
		}
		f, e := os.Open(*input)
		if e != nil {
			err = e
			break
		}
		defer f.Close()
		var finding hunter.Finding
		d := json.NewDecoder(f)
		d.DisallowUnknownFields()
		if err = d.Decode(&finding); err != nil {
			break
		}
		if e = d.Decode(new(any)); e != io.EOF {
			err = fmt.Errorf("expected exactly one finding JSON object")
			break
		}
		var id string
		var dup bool
		id, dup, err = hunter.Report(finding)
		result = map[string]any{"id": id, "duplicate": dup}
	case "scan-report":
		task := fs.String("task", "", "scanned task")
		baseline := fs.String("baseline", "", "exact scanned commit SHA")
		covered := fs.String("covered-json", "", "JSON array of inspected scope")
		skipped := fs.String("skipped-json", "", "JSON array of skipped scope (explicit [] allowed)")
		ids := fs.String("findings-json", "[]", "JSON array of finding IDs")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *task == "" || *baseline == "" || *covered == "" || *skipped == "" {
			return 2
		}
		var c, s, f []string
		if json.Unmarshal([]byte(*covered), &c) != nil || json.Unmarshal([]byte(*skipped), &s) != nil || json.Unmarshal([]byte(*ids), &f) != nil {
			return 2
		}
		result, err = hunter.ScanReport(*task, *baseline, c, s, f)
	case "get":
		id := fs.String("id", "", "finding ID")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *id == "" {
			return 2
		}
		result, err = hunter.Get(*id)
	case "list":
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
			return 2
		}
		result, err = hunter.List()
	case "triage":
		id := fs.String("id", "", "finding ID")
		controller := fs.String("controller", "", "triaging controller identity")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *id == "" || *controller == "" {
			return 2
		}
		err = hunter.Move(*id, hunter.Triaged, "controller:"+*controller)
	case "authorize-fix":
		id := fs.String("id", "", "finding ID")
		owner := fs.String("owner", "", "authorizing owner identity")
		task := fs.String("task", "", "separate approved fix task")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *id == "" || *owner == "" || *task == "" {
			return 2
		}
		err = hunter.Move(*id, hunter.ApprovedForFX, "owner:"+*owner+" fix-task:"+*task)
	case "start-fix":
		id := fs.String("id", "", "finding ID")
		worker := fs.String("worker", "", "fix worker identity")
		task := fs.String("task", "", "approved fix task being started")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *id == "" || *worker == "" || *task == "" {
			return 2
		}
		err = hunter.Move(*id, hunter.FixInProgress, "worker:"+*worker+" fix-task:"+*task)
	case "review", "approve":
		id := fs.String("id", "", "finding ID")
		repo := fs.String("repo", "", "clean fixed repository")
		digest := fs.String("verification-digest", "", "exact runtime evidence SHA256")
		attempt := fs.String("attempt", "", "runtime verification attempt (review)")
		worker := fs.String("worker", "", "fix worker identity (review)")
		reviewer := fs.String("reviewer", "", "independent reviewer identity (review)")
		report := fs.String("report", "", "independent review report path (review)")
		reviewDigest := fs.String("review-digest", "", "exact independent review SHA256 (approve)")
		owner := fs.String("owner", "", "owner identity (approve)")
		reason := fs.String("reason", "", "owner acceptance reason (approve)")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *id == "" || *repo == "" || *digest == "" {
			return 2
		}
		if args[0] == "review" {
			if *attempt == "" || *worker == "" || *reviewer == "" || *report == "" {
				return 2
			}
			result, err = hunter.ReviewFix(context.Background(), *id, *repo, *attempt, *digest, *worker, *reviewer, *report)
		} else {
			if *reviewDigest == "" || *owner == "" || *reason == "" {
				return 2
			}
			if !ownerTerminal() {
				fmt.Fprintln(os.Stderr, "hunter approve requires the owner's terminal; agents cannot approve findings")
				return 1
			}
			expected := "Approve " + *id + " " + *reviewDigest
			fmt.Fprintln(os.Stderr, "Owner acceptance closes the exact independently reviewed finding. Type:", expected)
			line, e := bufio.NewReader(os.Stdin).ReadString('\n')
			if e != nil || strings.TrimSpace(line) != expected {
				fmt.Fprintln(os.Stderr, "owner acceptance not confirmed")
				return 1
			}
			result, err = hunter.ApproveFix(context.Background(), *id, *repo, *digest, *reviewDigest, *owner, *reason)
		}
	case "verify":
		id := fs.String("id", "", "finding ID")
		repo := fs.String("repo", "", "local fixed revision repository")
		attempt := fs.String("attempt", "", "unique verification attempt")
		fixed := fs.String("fixed", "", "exact fixed commit SHA")
		red := fs.String("red-attempt", "", "observed baseline reproduction attempt")
		paths := fs.String("test-paths-json", "", "JSON array of unchanged tracked regression test paths")
		timeout := fs.Duration("timeout", 5*time.Minute, "total verification timeout (max 1h)")
		if fs.Parse(args[1:]) != nil || *id == "" || *repo == "" || *attempt == "" || *fixed == "" || *red == "" || fs.NArg() == 0 {
			return 2
		}
		var tests []string
		if json.Unmarshal([]byte(*paths), &tests) != nil || len(tests) == 0 {
			return 2
		}
		result, err = hunter.VerifyRuntime(context.Background(), *id, *repo, *attempt, *fixed, *red, tests, fs.Args(), *timeout)
	case "reproduce":
		id := fs.String("id", "", "finding ID")
		repo := fs.String("repo", "", "local baseline repository")
		attempt := fs.String("attempt", "", "unique reproduction attempt")
		timeout := fs.Duration("timeout", 5*time.Minute, "total setup and execution timeout (max 1h)")
		expected := fs.Int("expect-exit", 0, "required expected failure exit (1..255)")
		if fs.Parse(args[1:]) != nil || *id == "" || *repo == "" || *attempt == "" || fs.NArg() == 0 || *expected <= 0 || *expected > 255 || *timeout <= 0 || *timeout > time.Hour {
			return 2
		}
		a, e := hunter.Reproduce(context.Background(), *id, *repo, *attempt, *timeout, *expected, fs.Args())
		if e == nil && a.Outcome == "reproduced" {
			// The reproduce runner is the check the REPRODUCED gate trusts:
			// it has just recorded the red proof. Advance a fresh candidate,
			// never a finding already past this step.
			if f, g := hunter.Get(*id); g != nil {
				e = g
			} else if f.State == hunter.Candidate {
				e = hunter.Move(*id, hunter.Reproduced, "runner:"+a.Attempt)
			}
		}
		result, err = a, e
	default:
		fmt.Fprintln(os.Stderr, "unknown hunter action:", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hunter:", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// ownerTerminal is the owner-acceptance gate: only the owner's interactive
// terminal may type the confirmation. Tests present the confirmation through a
// pipe; the production value never treats a pipe or an agent session as the
// owner.
var ownerTerminal = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && !inClaudeCode() && os.Getenv("CODEX_THREAD_ID") == ""
}
