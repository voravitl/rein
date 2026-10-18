// Package verdict implements revision-bound verdicts and approvals (ADR 0002 B2).
package verdict

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Verdict represents a review verdict.
type Verdict string

const (
	Approve        Verdict = "APPROVE"
	RequestChanges Verdict = "REQUEST_CHANGES"
)

// VerdictRecord stores a single verdict.
type VerdictRecord struct {
	MR            int       `json:"mr"`
	SHA           string    `json:"sha"`
	PatchID       string    `json:"patch_id"`
	Verdict       Verdict   `json:"verdict"`
	ReviewerModel string    `json:"reviewer_model"`
	WorkerModel   string    `json:"worker_model"`
	Timestamp     time.Time `json:"timestamp"`
}

// ApprovalRecord stores a human approval.
type ApprovalRecord struct {
	MR        int       `json:"mr"`
	SHA       string    `json:"sha"`
	PatchID   string    `json:"patch_id"`
	Reason    string    `json:"reason,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ComputeRevisionID computes the combined revision identity using git patch-id --verbatim.
// Returns empty string if the diff is empty or on error.
func ComputeRevisionID(repoPath, base, head string) (string, error) {
	if base == "" {
		base = "origin/main"
	}
	if head == "" {
		head = "HEAD"
	}

	// Compute merge-base
	mergeBaseCmd := exec.Command("git", "merge-base", base, head)
	mergeBaseCmd.Dir = repoPath
	mbOut, err := mergeBaseCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base failed: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mbOut))

	// Generate diff
	diffCmd := exec.Command("git", "diff",
		"--no-ext-diff", "--no-textconv", "--no-renames", "--no-color",
		mergeBase+"..."+head)
	diffCmd.Dir = repoPath
	diffOut, err := diffCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}

	// Empty diff never matches
	if len(bytes.TrimSpace(diffOut)) == 0 {
		return "", nil
	}

	// Compute patch-id
	patchIDCmd := exec.Command("git", "patch-id", "--verbatim")
	patchIDCmd.Dir = repoPath
	patchIDCmd.Stdin = bytes.NewReader(diffOut)
	pidOut, err := patchIDCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git patch-id failed: %w", err)
	}

	// patch-id output format: "<id> <commitsha>"
	fields := strings.Fields(string(pidOut))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// RevisionIdentitiesMatch reports whether two revision identities denote the
// same change. Equal patch IDs match. Distinct IDs match only for a proven
// unchanged rebase: both revisions must produce byte-identical diffs against
// their own merge-base with origin/main once base-dependent lines (@@ hunk
// headers and index lines) are dropped. This is stricter than ADR 0002 B2.2's
// file-intersection rule, which accepted any differing patch ID while main's
// changes since the approved base stayed disjoint from the MR files.
func RevisionIdentitiesMatch(idA, idB string, repoPath, approvedBase, currentHead string) bool {
	if idA == "" || idB == "" {
		return false
	}
	if idA == idB {
		return true
	}
	if approvedBase == "" || currentHead == "" {
		return false
	}

	bodyA, err := unchangedPatchBody(repoPath, approvedBase)
	if err != nil {
		return false
	}
	bodyB, err := unchangedPatchBody(repoPath, currentHead)
	if err != nil {
		return false
	}
	return bodyA != "" && bodyA == bodyB
}

// unchangedPatchBody returns the revision's diff against its merge-base with
// origin/main with only base-dependent lines removed. Hunk headers (@@) and
// index lines carry line numbers and blob IDs that legitimately shift when the
// base moves; every other line (paths, mode lines, content) must be identical
// for the patch to count as unchanged.
func unchangedPatchBody(repoPath, sha string) (string, error) {
	mergeBaseCmd := exec.Command("git", "merge-base", "origin/main", sha)
	mergeBaseCmd.Dir = repoPath
	mbOut, err := mergeBaseCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base failed: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mbOut))
	if mergeBase == "" {
		return "", fmt.Errorf("no merge-base for origin/main and %s", sha)
	}

	diffCmd := exec.Command("git", "diff",
		"--no-ext-diff", "--no-textconv", "--no-renames", "--no-color",
		mergeBase+"..."+sha)
	diffCmd.Dir = repoPath
	diffOut, err := diffCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}

	var body strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(diffOut))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "index ") {
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading diff failed: %w", err)
	}
	return body.String(), nil
}

// NormalizeModelMaker extracts the model maker from a model string.
// Examples: "claude-sonnet-4.5" -> "anthropic", "gpt-5.6" -> "openai"
func NormalizeModelMaker(model string) string {
	model = strings.ToLower(model)
	if strings.HasPrefix(model, "claude") {
		return "anthropic"
	}
	if strings.HasPrefix(model, "gpt") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") {
		return "openai"
	}
	if strings.HasPrefix(model, "gemini") {
		return "google"
	}
	if strings.HasPrefix(model, "glm") {
		return "glm"
	}
	if strings.HasPrefix(model, "deepseek") {
		return "deepseek"
	}
	if strings.HasPrefix(model, "qwen") {
		return "qwen"
	}
	if strings.HasPrefix(model, "minimax") {
		return "minimax"
	}
	return "unknown"
}

// IsEscalatedReviewer reports whether a reviewer within the same provider family
// has a distinctly higher capability tier (e.g. Claude Opus reviewing Claude Sonnet or Haiku).
func IsEscalatedReviewer(workerModel, reviewerModel string) bool {
	w := strings.ToLower(workerModel)
	r := strings.ToLower(reviewerModel)

	// Anthropic: Opus reviewing Sonnet or Haiku
	if strings.Contains(r, "opus") && (strings.Contains(w, "sonnet") || strings.Contains(w, "haiku")) {
		return true
	}

	// OpenAI: o-series / reasoning models reviewing gpt-4o / mini
	if (strings.HasPrefix(r, "o1") || strings.HasPrefix(r, "o3") || strings.HasPrefix(r, "o4")) &&
		(strings.Contains(w, "mini") || strings.Contains(w, "gpt-4") || strings.Contains(w, "gpt-3")) {
		return true
	}

	return false
}

// StorageDir returns the directory where verdicts and approvals are stored.
// Uses run dir if provided, otherwise falls back to repo gate directory.
func StorageDir(runDir, repoPath string) string {
	if runDir != "" {
		return filepath.Join(runDir, "verdicts")
	}
	return filepath.Join(repoPath, ".rein", "verdicts")
}

// VerdictPath returns the file path for verdicts for a given MR.
func VerdictPath(storageDir string, mr int) string {
	return filepath.Join(storageDir, fmt.Sprintf("mr-%d-verdicts.json", mr))
}

// ApprovalPath returns the file path for approvals for a given MR.
func ApprovalPath(storageDir string, mr int) string {
	return filepath.Join(storageDir, fmt.Sprintf("mr-%d-approvals.json", mr))
}

// HashString computes SHA256 hash of a string for stable IDs.
func HashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// EnsureDir creates a directory if it doesn't exist.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}
