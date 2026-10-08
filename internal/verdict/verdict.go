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

// RevisionIdentitiesMatch checks if two revision IDs match.
// Also handles post-rebase matching: IDs match if files changed on main since approved base
// do not intersect with MR files.
func RevisionIdentitiesMatch(idA, idB string, repoPath, approvedBase, currentHead string, mrFiles []string) bool {
	if idA == "" || idB == "" {
		return false
	}
	if idA == idB {
		return true
	}

	// Check post-rebase case
	if approvedBase != "" && currentHead != "" && len(mrFiles) > 0 {
		mainFiles, err := getFilesChangedBetween(repoPath, approvedBase, "origin/main")
		if err != nil {
			return false
		}
		if !filesIntersect(mainFiles, mrFiles) {
			return true
		}
	}

	return false
}

func getFilesChangedBetween(repoPath, base, head string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--no-renames", base+"..."+head)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if f := strings.TrimSpace(scanner.Text()); f != "" {
			files = append(files, f)
		}
	}
	return files, scanner.Err()
}

func filesIntersect(a, b []string) bool {
	m := make(map[string]bool, len(a))
	for _, f := range a {
		m[f] = true
	}
	for _, f := range b {
		if m[f] {
			return true
		}
	}
	return false
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
