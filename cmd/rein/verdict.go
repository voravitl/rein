package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/tier"
	"github.com/voravitl/rein/internal/verdict"
)

func cmdVerdict(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rein verdict <record|check>")
		return 2
	}

	switch args[0] {
	case "record":
		return cmdVerdictRecord(args[1:])
	case "check":
		return cmdVerdictCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown verdict subcommand: %s\n", args[0])
		return 2
	}
}

func cmdVerdictRecord(args []string) int {
	fs := flag.NewFlagSet("verdict record", flag.ExitOnError)
	mr := fs.Int("mr", 0, "MR number")
	sha := fs.String("sha", "", "commit SHA")
	verdictStr := fs.String("verdict", "", "APPROVE or REQUEST_CHANGES")
	reviewer := fs.String("reviewer", "", "reviewer model")
	worker := fs.String("worker", "", "worker model")
	runDir := fs.String("run-dir", "", "run directory (optional)")
	repoPath := fs.String("repo", ".", "repository path")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mr == 0 {
		fmt.Fprintln(os.Stderr, "error: --mr is required")
		return 2
	}
	if *sha == "" {
		fmt.Fprintln(os.Stderr, "error: --sha is required")
		return 2
	}
	if *verdictStr == "" {
		fmt.Fprintln(os.Stderr, "error: --verdict is required")
		return 2
	}
	if *reviewer == "" {
		fmt.Fprintln(os.Stderr, "error: --reviewer is required")
		return 2
	}
	if *worker == "" {
		fmt.Fprintln(os.Stderr, "error: --worker is required")
		return 2
	}

	var v verdict.Verdict
	switch strings.ToUpper(*verdictStr) {
	case "APPROVE":
		v = verdict.Approve
	case "REQUEST_CHANGES":
		v = verdict.RequestChanges
	default:
		fmt.Fprintf(os.Stderr, "error: invalid verdict %q, must be APPROVE or REQUEST_CHANGES\n", *verdictStr)
		return 2
	}

	storageDir := verdict.StorageDir(*runDir, *repoPath)
	if err := verdict.RecordVerdict(storageDir, *mr, *sha, v, *reviewer, *worker, *repoPath); err != nil {
		fmt.Fprintf(os.Stderr, "error recording verdict: %v\n", err)
		return 1
	}

	fmt.Printf("Verdict recorded: MR %d, SHA %s, %s (reviewer: %s, worker: %s)\n",
		*mr, *sha, v, *reviewer, *worker)
	return 0
}

func cmdVerdictCheck(args []string) int {
	fs := flag.NewFlagSet("verdict check", flag.ExitOnError)
	mr := fs.Int("mr", 0, "MR number")
	sha := fs.String("sha", "", "commit SHA")
	tierStr := fs.String("tier", "T1", "task tier (T1, T2, T3)")
	runDir := fs.String("run-dir", "", "run directory (optional)")
	repoPath := fs.String("repo", ".", "repository path")
	jsonOutput := fs.Bool("json", false, "output as JSON")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mr == 0 {
		fmt.Fprintln(os.Stderr, "error: --mr is required")
		return 2
	}
	if *sha == "" {
		fmt.Fprintln(os.Stderr, "error: --sha is required")
		return 2
	}

	var taskTier tier.Tier
	switch strings.ToUpper(*tierStr) {
	case "T1":
		taskTier = tier.T1
	case "T2":
		taskTier = tier.T2
	case "T3":
		taskTier = tier.T3
	default:
		fmt.Fprintf(os.Stderr, "error: invalid tier %q, must be T1, T2, or T3\n", *tierStr)
		return 2
	}

	// Compute patch ID
	patchID, err := verdict.ComputeRevisionID(*repoPath, "origin/main", "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not compute patch ID: %v\n", err)
		patchID = ""
	}

	storageDir := verdict.StorageDir(*runDir, *repoPath)
	result, err := verdict.CheckVerdicts(storageDir, *mr, *sha, patchID, taskTier, *repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error checking verdicts: %v\n", err)
		return 1
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("Verdict check for MR %d (SHA %s, tier %s):\n", *mr, *sha, taskTier)
		fmt.Printf("  Maker count: %d\n", result.MakerCount)
		fmt.Printf("  Has approval: %v\n", result.HasApproval)
		if result.Passed {
			fmt.Println("  ✓ All conditions met")
		} else {
			fmt.Println("  ✗ Missing requirements:")
			for _, reason := range result.MissingReasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
	}

	if result.Passed {
		return 0
	}
	return 1
}

func cmdApprove(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rein approve <prompt|--mr N ...>")
		return 2
	}

	if args[0] == "prompt" {
		return cmdApprovePrompt(args[1:])
	}

	// Direct approval command (user-only)
	if inClaudeCode() {
		fmt.Fprintln(os.Stderr, "error: rein approve is user-only and cannot be run inside Claude Code")
		fmt.Fprintln(os.Stderr, "Run this command in your own terminal, or use the AskUserQuestion flow")
		return 1
	}

	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	mr := fs.Int("mr", 0, "MR number")
	sha := fs.String("sha", "", "commit SHA")
	reason := fs.String("reason", "", "approval reason")
	runDir := fs.String("run-dir", "", "run directory (optional)")
	repoPath := fs.String("repo", ".", "repository path")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mr == 0 {
		fmt.Fprintln(os.Stderr, "error: --mr is required")
		return 2
	}

	// Resolve SHA if not provided
	actualSHA := *sha
	if actualSHA == "" {
		out, err := execOutput(*repoPath, "git", "rev-parse", "HEAD")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: could not resolve HEAD: %v\n", err)
			return 1
		}
		actualSHA = strings.TrimSpace(string(out))
	}

	// Compute patch ID
	patchID, err := verdict.ComputeRevisionID(*repoPath, "origin/main", "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not compute patch ID: %v\n", err)
		patchID = ""
	}

	storageDir := verdict.StorageDir(*runDir, *repoPath)
	if err := verdict.RecordApproval(storageDir, *mr, actualSHA, patchID, *reason); err != nil {
		fmt.Fprintf(os.Stderr, "error recording approval: %v\n", err)
		return 1
	}

	fmt.Printf("Approval recorded: MR %d, SHA %s\n", *mr, actualSHA)
	if patchID != "" {
		fmt.Printf("  Patch ID: %s\n", patchID)
	}
	if *reason != "" {
		fmt.Printf("  Reason: %s\n", *reason)
	}
	return 0
}

func cmdApprovePrompt(args []string) int {
	fs := flag.NewFlagSet("approve prompt", flag.ExitOnError)
	mr := fs.Int("mr", 0, "MR number")
	sha := fs.String("sha", "", "commit SHA (defaults to HEAD)")
	runDir := fs.String("run-dir", "", "run directory (optional)")
	repoPath := fs.String("repo", ".", "repository path")
	title := fs.String("title", "", "MR title (optional, extracts from git if not provided)")
	contractName := fs.String("contract", "", "contract name to read tier from (optional)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mr == 0 {
		fmt.Fprintln(os.Stderr, "error: --mr is required")
		return 2
	}

	// Resolve SHA if not provided
	actualSHA := *sha
	if actualSHA == "" {
		out, err := execOutput(*repoPath, "git", "rev-parse", "HEAD")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: could not resolve HEAD: %v\n", err)
			return 1
		}
		actualSHA = strings.TrimSpace(string(out))
	}

	// Get MR title
	mrTitle := *title
	if mrTitle == "" {
		// Try to extract from git commit message (first line)
		out, err := execOutput(*repoPath, "git", "log", "-1", "--format=%s", actualSHA)
		if err == nil {
			mrTitle = strings.TrimSpace(string(out))
		}
		if mrTitle == "" {
			mrTitle = fmt.Sprintf("MR %d", *mr)
		}
	}

	// Determine tier - read from contract if available
	var taskTier tier.Tier = tier.T1
	if *contractName != "" {
		c, err := contract.Load(*contractName)
		if err == nil {
			// Evaluate tier from contract
			taskTier = tier.EvaluateFromAllowGlobs(c.Allow, c.Profile.SensitivePaths)

			// Also escalate based on actual changed files
			changedFiles, err := getChangedFiles(*repoPath, "origin/main", "HEAD")
			if err == nil {
				taskTier = tier.EvaluateFromChangedFiles(changedFiles, c.Profile.SensitivePaths, taskTier)
			}
		}
	} else {
		// No contract provided - try to infer from changed files
		changedFiles, err := getChangedFiles(*repoPath, "origin/main", "HEAD")
		if err == nil && len(changedFiles) > 0 {
			// Use simple heuristics for tier without contract
			for _, f := range changedFiles {
				// If any file looks sensitive (auth, security, etc), escalate to T3
				fl := strings.ToLower(f)
				if strings.Contains(fl, "auth") || strings.Contains(fl, "security") ||
					strings.Contains(fl, "credential") || strings.Contains(fl, "secret") {
					taskTier = tier.T3
					break
				}
			}
		}
	}

	// Get verdict summary - read from verdict files
	storageDir := verdict.StorageDir(*runDir, *repoPath)
	verdictSummary := getVerdictSummary(storageDir, *mr, actualSHA)

	// Build AskUserQuestion payload
	question := fmt.Sprintf(`Approve merge for %s?

SHA: %s
Tier: %s
Verdicts: %s

This approval gates the merge. Review the verdicts and changes before approving.`,
		mrTitle, actualSHA[:min(8, len(actualSHA))], taskTier, verdictSummary)

	payload := map[string]interface{}{
		"question": question,
		"options":  []string{"Approve"},
		"metadata": map[string]interface{}{
			"mr":   *mr,
			"sha":  actualSHA,
			"tier": taskTier.String(),
		},
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshaling JSON: %v\n", err)
		return 1
	}

	fmt.Println(string(data))
	return 0
}

func getVerdictSummary(storageDir string, mr int, sha string) string {
	verdictPath := verdict.VerdictPath(storageDir, mr)

	// Try to read verdicts
	data, err := os.ReadFile(verdictPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "No verdicts recorded yet"
		}
		return "Error reading verdicts"
	}

	var records []verdict.VerdictRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return "Error parsing verdicts"
	}

	if len(records) == 0 {
		return "No verdicts recorded yet"
	}

	// Compute current patch ID for matching
	currentPatchID, err := verdict.ComputeRevisionID(".", "origin/main", "HEAD")
	if err != nil {
		currentPatchID = ""
	}

	// Build summary: count approvals and request-changes matching this revision
	var approvals, changes []string
	for _, rec := range records {
		// Match either exact SHA or matching patch-id
		if rec.SHA == sha || (rec.PatchID != "" && currentPatchID != "" && rec.PatchID == currentPatchID) {
			maker := verdict.NormalizeModelMaker(rec.ReviewerModel)
			switch rec.Verdict {
			case verdict.Approve:
				approvals = append(approvals, maker)
			case verdict.RequestChanges:
				changes = append(changes, maker)
			}
		}
	}

	parts := []string{fmt.Sprintf("%d verdict(s)", len(records))}
	if len(approvals) > 0 {
		parts = append(parts, fmt.Sprintf("APPROVE: %s", strings.Join(approvals, ", ")))
	}
	if len(changes) > 0 {
		parts = append(parts, fmt.Sprintf("REQUEST_CHANGES: %s", strings.Join(changes, ", ")))
	}

	return strings.Join(parts, " | ")
}

func getChangedFiles(repoPath, base, head string) ([]string, error) {
	out, err := execOutput(repoPath, "git", "diff", "--name-only", "--no-renames", base+"..."+head)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func inClaudeCode() bool {
	return os.Getenv("CLAUDE_PID") != "" || os.Getenv("CLAUDE_CODE_SESSION_ID") != ""
}

func execOutput(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Output()
}
