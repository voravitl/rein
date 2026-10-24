package verdict

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/tier"
)

// RecordVerdict saves a verdict record to the appropriate file.
func RecordVerdict(storageDir string, mr int, sha string, verdict Verdict, reviewerModel, workerModel, repoPath string) error {
	if err := EnsureDir(storageDir); err != nil {
		return fmt.Errorf("failed to create storage dir: %w", err)
	}

	// Compute patch ID from repo path (default to current directory if empty)
	if repoPath == "" {
		repoPath = "."
	}
	patchID, err := ComputeRevisionID(repoPath, "origin/main", sha)
	if err != nil {
		// Log warning but don't fail - patch ID is optional
		patchID = ""
	}

	record := VerdictRecord{
		MR:            mr,
		SHA:           sha,
		PatchID:       patchID,
		Verdict:       verdict,
		ReviewerModel: reviewerModel,
		WorkerModel:   workerModel,
		Timestamp:     time.Now(),
	}

	path := VerdictPath(storageDir, mr)
	unlock, err := run.LockFile(path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to lock verdicts: %w", err)
	}
	defer unlock()

	var records []VerdictRecord
	records, err = loadVerdictRecords(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load existing verdicts: %w", err)
	}
	records = append(records, record)

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal verdicts: %w", err)
	}

	if err := writeFileAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write verdicts: %w", err)
	}

	return nil
}

// RecordApproval saves a human approval record. via and sessionID carry the
// provenance when the approval was human-directed from inside an AI session;
// both are empty for a directly typed approval.
func RecordApproval(storageDir string, mr int, sha, patchID, reason, via, sessionID string) error {
	if err := EnsureDir(storageDir); err != nil {
		return fmt.Errorf("failed to create storage dir: %w", err)
	}

	record := ApprovalRecord{
		MR:        mr,
		SHA:       sha,
		PatchID:   patchID,
		Reason:    reason,
		Via:       via,
		SessionID: sessionID,
		Timestamp: time.Now(),
	}

	path := ApprovalPath(storageDir, mr)
	unlock, err := run.LockFile(path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to lock approvals: %w", err)
	}
	defer unlock()

	var records []ApprovalRecord
	records, err = loadApprovalRecords(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load existing approvals: %w", err)
	}
	records = append(records, record)

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal approvals: %w", err)
	}

	if err := writeFileAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write approvals: %w", err)
	}

	return nil
}

// writeFileAtomic replaces path through a temp file in the same directory, so a
// crash or full disk never leaves a truncated verdict or approval log.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".rein-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CheckResult represents the result of a verdict check.
type CheckResult struct {
	Passed            bool             `json:"passed"`
	Tier              tier.Tier        `json:"tier"`
	ReviewerCount     int              `json:"reviewer_count"`
	HasApproval       bool             `json:"has_approval"`
	MissingReasons    []string         `json:"missing_reasons,omitempty"`
	MatchingVerdicts  []VerdictRecord  `json:"matching_verdicts,omitempty"`
	MatchingApprovals []ApprovalRecord `json:"matching_approvals,omitempty"`
}

// RequiredReviewers returns the number of distinct cross-model reviewers
// needed for the tier.
func (r *CheckResult) RequiredReviewers() int {
	if r.Tier == tier.T3 {
		return 2
	}
	return 1
}

// FormatEvidence returns human-readable evidence of passing review and approvals.
func (r *CheckResult) FormatEvidence() string {
	if !r.Passed {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[APPROVAL & REVIEW EVIDENCE]\n")
	sb.WriteString("  Status: PASSED (Verified Review & Approval Gate)\n")
	sb.WriteString(fmt.Sprintf("  Tier:   %s | Required Reviewers: %d | Distinct Reviewers: %d\n", r.Tier, r.RequiredReviewers(), r.ReviewerCount))
	sb.WriteString("  Reviewer Verdicts:\n")
	if len(r.MatchingVerdicts) == 0 {
		sb.WriteString("    (none)\n")
	} else {
		for _, v := range r.MatchingVerdicts {
			escalatedNote := ""
			if IsEscalatedReviewer(v.WorkerModel, v.ReviewerModel) {
				escalatedNote = " [escalated reviewer]"
			}
			patchInfo := ""
			if v.PatchID != "" {
				patchInfo = fmt.Sprintf(" | Patch: %s", v.PatchID)
			}
			sb.WriteString(fmt.Sprintf("    ✓ [%s] %s%s (Maker: %s)\n", v.Verdict, v.ReviewerModel, escalatedNote, NormalizeModelMaker(v.ReviewerModel)))
			sb.WriteString(fmt.Sprintf("      Worker: %s | Time: %s%s\n", v.WorkerModel, v.Timestamp.UTC().Format(time.RFC3339), patchInfo))
		}
	}
	sb.WriteString("  Human Approvals:\n")
	if len(r.MatchingApprovals) == 0 {
		sb.WriteString("    (none)\n")
	} else {
		for _, a := range r.MatchingApprovals {
			reason := a.Reason
			if reason == "" {
				reason = "approved"
			}
			patchInfo := ""
			if a.PatchID != "" {
				patchInfo = fmt.Sprintf(" | Patch: %s", a.PatchID)
			}
			viaInfo := ""
			if a.Via != "" {
				viaInfo = fmt.Sprintf(" | Via: human-directed in %s session %s", a.Via, a.SessionID)
			}
			sb.WriteString(fmt.Sprintf("    ✓ Reason: %s\n", reason))
			sb.WriteString(fmt.Sprintf("      SHA: %s | Time: %s%s%s\n", a.SHA, a.Timestamp.UTC().Format(time.RFC3339), patchInfo, viaInfo))
		}
	}
	return sb.String()
}

// CheckVerdicts validates all conditions for merge approval.
// Fails closed: missing or corrupt gate files result in failure.
func CheckVerdicts(storageDir string, mr int, sha, currentPatchID string, taskTier tier.Tier, repoPath string) (*CheckResult, error) {
	result := &CheckResult{
		Tier:           taskTier,
		MissingReasons: []string{},
	}

	// Load verdicts
	verdictPath := VerdictPath(storageDir, mr)
	verdicts, err := loadVerdictRecords(verdictPath)
	if err != nil {
		if os.IsNotExist(err) {
			result.MissingReasons = append(result.MissingReasons, "no verdicts found")
			return result, nil
		}
		return nil, fmt.Errorf("failed to load verdicts (fail closed): %w", err)
	}

	// Load approvals
	approvalPath := ApprovalPath(storageDir, mr)
	approvals, err := loadApprovalRecords(approvalPath)
	if err != nil {
		if os.IsNotExist(err) {
			result.MissingReasons = append(result.MissingReasons, "no approvals found")
		} else {
			return nil, fmt.Errorf("failed to load approvals (fail closed): %w", err)
		}
	}

	// Bound records to the current revision, then reduce to each reviewer's
	// effective latest verdict: a later REQUEST_CHANGES retracts an earlier
	// APPROVE from the same reviewer. On equal timestamps the later record in
	// file order wins.
	var current []VerdictRecord
	for _, v := range verdicts {
		// Use RevisionIdentitiesMatch for comprehensive revision binding (ADR B2.2)
		if RecordMatchesRevision(v.SHA, v.PatchID, sha, currentPatchID, repoPath) {
			current = append(current, v)
		}
	}
	effective := make(map[string]int)
	for i, v := range current {
		key := NormalizeModelName(v.ReviewerModel)
		prev, ok := effective[key]
		if !ok || !v.Timestamp.Before(current[prev].Timestamp) {
			effective[key] = i
		}
	}

	// Check verdicts for cross-model review
	reviewers := make(map[string]bool)
	var matchingVerdicts []VerdictRecord

	for i, v := range current {
		if effective[NormalizeModelName(v.ReviewerModel)] != i {
			continue // superseded by a later verdict from the same reviewer
		}
		if v.Verdict != Approve {
			continue
		}

		// Cross-mode review: only a reviewer model distinct from the worker
		// model it reviewed counts as an independent review. The full model
		// name is compared, so a different model from the same maker (e.g.
		// GLM-5.3-Flash reviewing GLM-5.3 work) qualifies.
		if NormalizeModelName(v.ReviewerModel) == NormalizeModelName(v.WorkerModel) {
			continue
		}

		reviewers[NormalizeModelName(v.ReviewerModel)] = true
		matchingVerdicts = append(matchingVerdicts, v)
	}

	result.MatchingVerdicts = matchingVerdicts
	result.ReviewerCount = len(reviewers)

	// Check reviewer count requirement
	requiredReviewers := result.RequiredReviewers()

	if result.ReviewerCount < requiredReviewers {
		result.MissingReasons = append(result.MissingReasons,
			fmt.Sprintf("need cross-model review verdicts from %d distinct reviewer model(s), have %d", requiredReviewers, result.ReviewerCount))
	}

	// Check for human approval bound to the current revision
	var matchingApprovals []ApprovalRecord
	for _, a := range approvals {
		if RecordMatchesRevision(a.SHA, a.PatchID, sha, currentPatchID, repoPath) {
			result.HasApproval = true
			matchingApprovals = append(matchingApprovals, a)
		}
	}
	result.MatchingApprovals = matchingApprovals

	if !result.HasApproval {
		result.MissingReasons = append(result.MissingReasons, "no human approval for this revision")
	}

	result.Passed = len(result.MissingReasons) == 0
	return result, nil
}

// RecordMatchesRevision binds a stored record to the current revision: an
// exact SHA match, or a matching combined revision identity (ADR B2.2) where
// differing patch IDs are accepted only for a proven unchanged rebase. It is
// the single matcher shared by `verdict check` and the approve-prompt summary.
func RecordMatchesRevision(recordSHA, recordPatchID, currentSHA, currentPatchID, repoPath string) bool {
	// Legacy symbolic refs are not evidence of the commit reviewed at record time.
	if repoPath != "" && (!immutableCommitID(recordSHA) || !immutableCommitID(currentSHA)) {
		return false
	}
	// Exact SHA match
	if recordSHA == currentSHA {
		return true
	}

	// Patch-ID based matching with proven unchanged rebases
	if recordPatchID != "" && currentPatchID != "" {
		return RevisionIdentitiesMatch(recordPatchID, currentPatchID, repoPath, recordSHA, currentSHA)
	}

	return false
}

func loadVerdictRecords(path string) ([]VerdictRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var records []VerdictRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}

	return records, nil
}

func loadApprovalRecords(path string) ([]ApprovalRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var records []ApprovalRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}

	return records, nil
}
