package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/supervisor"
)

// readDAGTerminals replays the supervisor journal; unit tests of the
// integration steps substitute recorded terminal states.
var readDAGTerminals = supervisor.ReadDAGTerminals

type integrateOpts struct {
	PlanPath string
	Branch   string
	Worktree string
}

func cmdDagIntegrate(args []string) int {
	fs := flag.NewFlagSet("dag integrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	plan := fs.String("plan", "", "path to compiled supervisor plan.json")
	branch := fs.String("branch", "", "name of target integration branch to create")
	worktree := fs.String("worktree", "", "optional directory path for integration worktree")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *plan == "" || *branch == "" {
		fmt.Fprintln(os.Stderr, dagUsage)
		return 2
	}

	opts := integrateOpts{
		PlanPath: *plan,
		Branch:   *branch,
		Worktree: *worktree,
	}

	if err := runDagIntegrate(opts); err != nil {
		fmt.Fprintf(os.Stderr, "[dag integrate] error: %v\n", err)
		return 1
	}
	return 0
}

func runDagIntegrate(opts integrateOpts) error {
	// 1. Load and validate plan
	p, planHash, err := supervisor.Load(opts.PlanPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}
	if p.Schema != 3 {
		return fmt.Errorf("plan must be schema 3 (got %d)", p.Schema)
	}
	if p.Repo == "" {
		return fmt.Errorf("plan repo path is empty")
	}

	// 2. Validate branch name syntax
	if err := validateBranchName(p.Repo, opts.Branch); err != nil {
		return fmt.Errorf("invalid branch name %q: %w", opts.Branch, err)
	}

	// 3. Replay the supervisor journal with the supervisor's own validation
	taskOutcomes, err := readDAGTerminals(context.Background(), p, planHash)
	if err != nil {
		return fmt.Errorf("cannot read supervisor journal: %w", err)
	}

	// 4. Verify each task is done and validate checkpoints
	taskBase := ""
	for _, t := range p.Tasks {
		if t.Base != "" {
			taskBase = t.Base
		}
		term, ok := taskOutcomes[t.Task]
		if !ok || term.State == "ready" || term.State == "pending" {
			return fmt.Errorf("task %q has not completed (no terminal result in journal)", t.Task)
		}
		if term.State != "done" {
			return fmt.Errorf("task %q is not done (state: %s, reason: %s)", t.Task, term.State, term.Outcome.Reason)
		}

		if t.Kind == "worker" {
			if term.Outcome.CheckpointDigest == "" {
				return fmt.Errorf("task %q completed without checkpoint digest", t.Task)
			}

			// Load and verify checkpoint
			attempt := term.Attempt
			cp, err := routing.LoadCheckpoint(t.Task, attempt)
			if err != nil {
				return fmt.Errorf("failed to load checkpoint for task %q: %w", t.Task, err)
			}

			// Checkpoint digest must match journal
			if cp.Digest != term.Outcome.CheckpointDigest {
				return fmt.Errorf("checkpoint digest mismatch for task %q: journal has %s, checkpoint has %s", t.Task, term.Outcome.CheckpointDigest, cp.Digest)
			}

			// Verify worktree is clean
			ct, err := contract.Load(t.Task)
			if err != nil {
				return fmt.Errorf("cannot load contract for task %q: %w", t.Task, err)
			}
			if err := verifyCleanWorktree(ct.Worktree); err != nil {
				return fmt.Errorf("task %q worktree is dirty: %w", t.Task, err)
			}

			// Verify live worktree matches checkpoint
			if err := routing.VerifyCheckpoint(t.Task, cp.Attempt); err != nil {
				return fmt.Errorf("checkpoint verification failed for task %q: %w", t.Task, err)
			}

			// Verify head descends from base
			base := t.Base
			if base == "" {
				base = cp.Base
			}
			if base != "" {
				if err := verifyHeadDescendsFromBase(ct.Worktree, base); err != nil {
					return fmt.Errorf("task %q HEAD does not descend from base %s: %w", t.Task, base, err)
				}
			}
		}
	}

	if taskBase == "" {
		return fmt.Errorf("no base commit found across plan tasks")
	}

	// 5. Check target branch does not already exist
	if branchExists(p.Repo, opts.Branch) {
		return fmt.Errorf("target branch %q already exists in repository %s", opts.Branch, p.Repo)
	}

	// 6. Setup integration worktree
	wtDir := opts.Worktree
	if wtDir == "" {
		wtDir = filepath.Join(os.TempDir(), fmt.Sprintf("rein-integrate-%s-%s-%d", p.Run, opts.Branch, os.Getpid()))
	}
	wtDir, err = filepath.Abs(wtDir)
	if err != nil {
		return err
	}

	// Create worktree with target branch at base
	cmd := exec.Command("git", "-C", p.Repo, "worktree", "add", "-b", opts.Branch, wtDir, taskBase)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create integration worktree at %s: %v (%s)", wtDir, err, strings.TrimSpace(string(out)))
	}

	// 7. Topological ordering and cherry-pick
	sortedTasks, err := topologicalSortTasks(p.Tasks)
	if err != nil {
		return fmt.Errorf("topological sort failed: %w", err)
	}

	appliedCommits := 0
	for _, t := range sortedTasks {
		if t.Kind != "worker" {
			continue
		}
		ct, err := contract.Load(t.Task)
		if err != nil {
			return fmt.Errorf("cannot load contract for task %q: %w", t.Task, err)
		}

		base := t.Base
		if base == "" {
			base = taskBase
		}

		headCmd := exec.Command("git", "-C", ct.Worktree, "rev-parse", "HEAD")
		headOut, err := headCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get HEAD for task %q: %w", t.Task, err)
		}
		headSHA := strings.TrimSpace(string(headOut))

		// List commits to cherry-pick
		revListCmd := exec.Command("git", "-C", ct.Worktree, "rev-list", "--reverse", fmt.Sprintf("%s..%s", base, headSHA))
		revOut, err := revListCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to list commits for task %q: %w", t.Task, err)
		}
		commits := strings.Fields(strings.TrimSpace(string(revOut)))

		for _, commit := range commits {
			// The committer is rein, as for the supervisor's result commits; the
			// author of each commit is preserved. A fresh machine may have no git
			// identity at all, and integration must not depend on one.
			cpCmd := exec.Command("git", "-C", wtDir, "cherry-pick", commit)
			cpCmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME="+dagCommitAuthor, "GIT_COMMITTER_EMAIL="+dagCommitEmail)
			if cpOut, err := cpCmd.CombinedOutput(); err != nil {
				// Non-zero exit: leave state for inspection, do NOT commit or push
				return fmt.Errorf("cherry-pick conflict applying commit %s from task %q: %s", commit, t.Task, strings.TrimSpace(string(cpOut)))
			}
			appliedCommits++
		}
	}

	// 8. Run goal verification command(s)
	// Find the verification node (node depending on all other tasks or final node with Verify)
	verificationRan := false
	for _, t := range sortedTasks {
		if len(t.Verify) > 0 {
			for _, vcmd := range t.Verify {
				if len(vcmd) == 0 {
					continue
				}
				binPath := vcmd[0]
				if lp, err := exec.LookPath(binPath); err == nil {
					binPath = lp
				} else if lp, err := exec.LookPath(filepath.Base(binPath)); err == nil {
					binPath = lp
				}
				vExec := exec.Command(binPath, vcmd[1:]...)
				vExec.Dir = wtDir
				vExec.Env = os.Environ()
				if vOut, err := vExec.CombinedOutput(); err != nil {
					return fmt.Errorf("goal verification command %v failed in integration worktree %s: %v\n%s", vcmd, wtDir, err, strings.TrimSpace(string(vOut)))
				}
				verificationRan = true
			}
		}
	}

	fmt.Printf("integrated %d task(s) (%d commits) into branch %s at %s\n", len(sortedTasks), appliedCommits, opts.Branch, wtDir)
	if verificationRan {
		fmt.Println("goal verification passed")
	}
	return nil
}

func validateBranchName(repo, branch string) error {
	cmd := exec.Command("git", "-C", repo, "check-ref-format", "--branch", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func branchExists(repo, branch string) bool {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func verifyCleanWorktree(wt string) error {
	cmd := exec.Command("git", "-C", wt, "status", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	s := strings.TrimSpace(string(out))
	if len(s) > 0 {
		return fmt.Errorf("uncommitted changes present:\n%s", s)
	}
	return nil
}

func verifyHeadDescendsFromBase(wt, base string) error {
	headCmd := exec.Command("git", "-C", wt, "rev-parse", "HEAD")
	out, err := headCmd.Output()
	if err != nil {
		return err
	}
	head := strings.TrimSpace(string(out))
	cmd := exec.Command("git", "-C", wt, "merge-base", "--is-ancestor", base, head)
	return cmd.Run()
}

func topologicalSortTasks(tasks []supervisor.Task) ([]supervisor.Task, error) {
	taskMap := make(map[string]supervisor.Task)
	inDegree := make(map[string]int)
	adj := make(map[string][]string)

	for _, t := range tasks {
		taskMap[t.Task] = t
		inDegree[t.Task] = 0
	}

	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, exists := taskMap[dep]; !exists {
				return nil, fmt.Errorf("unknown dependency %q for task %q", dep, t.Task)
			}
			adj[dep] = append(adj[dep], t.Task)
			inDegree[t.Task]++
		}
	}

	var queue []string
	for _, t := range tasks {
		if inDegree[t.Task] == 0 {
			queue = append(queue, t.Task)
		}
	}

	var sorted []supervisor.Task
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		sorted = append(sorted, taskMap[curr])

		for _, nxt := range adj[curr] {
			inDegree[nxt]--
			if inDegree[nxt] == 0 {
				queue = append(queue, nxt)
			}
		}
	}

	if len(sorted) != len(tasks) {
		return nil, fmt.Errorf("cycle detected in plan tasks")
	}
	return sorted, nil
}
