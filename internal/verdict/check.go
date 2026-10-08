package verdict

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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
	patchID, err := ComputeRevisionID(repoPath, "origin/main", "HEAD")
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
	records, err := loadVerdictRecords(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load existing verdicts: %w", err)
	}

	records = append(records, record)

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal verdicts: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write verdicts: %w", err)
	}

	return nil
}

// RecordApproval saves a human approval record.
func RecordApproval(storageDir string, mr int, sha, patchID, reason string) error {
	if err := EnsureDir(storageDir); err != nil {
		return fmt.Errorf("failed to create storage dir: %w", err)
	}

	record := ApprovalRecord{
		MR:        mr,
		SHA:       sha,
		PatchID:   patchID,
		Reason:    reason,
		Timestamp: time.Now(),
	}

	path := ApprovalPath(storageDir, mr)
	records, err := loadApprovalRecords(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load existing approvals: %w", err)
	}

	records = append(records, record)

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal approvals: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write approvals: %w", err)
	}

	return nil
}

// CheckResult represents the result of a verdict check.
type CheckResult struct {
	Passed            bool             `json:"passed"`
	Tier              tier.Tier        `json:"tier"`
	MakerCount        int              `json:"maker_count"`
	HasApproval       bool             `json:"has_approval"`
	MissingReasons    []string         `json:"missing_reasons,omitempty"`
	MatchingVerdicts  []VerdictRecord  `json:"matching_verdicts,omitempty"`
	MatchingApprovals []ApprovalRecord `json:"matching_approvals,omitempty"`
}

// RequiredMakers returns the number of distinct makers needed for the tier.
func (r *CheckResult) RequiredMakers() int {
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
	sb.WriteString(fmt.Sprintf("  Tier:   %s | Required Makers: %d | Actual Makers: %d\n", r.Tier, r.RequiredMakers(), r.MakerCount))
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
			sb.WriteString(fmt.Sprintf("    ✓ Reason: %s\n", reason))
			sb.WriteString(fmt.Sprintf("      SHA: %s | Time: %s%s\n", a.SHA, a.Timestamp.UTC().Format(time.RFC3339), patchInfo))
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

	// Get MR files for post-rebase matching
	mrFiles, err := getFilesChangedBetween(repoPath, "origin/main", "HEAD")
	if err != nil {
		mrFiles = []string{} // Continue without files for post-rebase check
	}

	// Check verdicts for maker diversity
	makers := make(map[string]bool)
	workerMaker := ""
	var matchingVerdicts []VerdictRecord

	for _, v := range verdicts {
		// Use RevisionIdentitiesMatch for comprehensive revision binding (ADR B2.2)
		if !revisionMatches(v.SHA, v.PatchID, sha, currentPatchID, repoPath, mrFiles) {
			continue
		}
		if v.Verdict != Approve {
			continue
		}

		reviewerMaker := NormalizeModelMaker(v.ReviewerModel)
		if workerMaker == "" {
			workerMaker = NormalizeModelMaker(v.WorkerModel)
		}

		// Reviewer maker must not equal worker maker, except when an escalated model
		// (e.g. Claude Opus) reviews a lower-tier worker (Claude Sonnet or Haiku).
		if reviewerMaker == workerMaker {
			if IsEscalatedReviewer(v.WorkerModel, v.ReviewerModel) {
				reviewerMaker = reviewerMaker + ":escalated"
			} else {
				continue
			}
		}

		makers[reviewerMaker] = true
		matchingVerdicts = append(matchingVerdicts, v)
	}

	result.MatchingVerdicts = matchingVerdicts
	result.MakerCount = len(makers)

	// Check maker count requirement
	requiredMakers := result.RequiredMakers()

	if result.MakerCount < requiredMakers {
		result.MissingReasons = append(result.MissingReasons,
			fmt.Sprintf("need verdicts from %d distinct model maker(s), have %d", requiredMakers, result.MakerCount))
	}

	// Check for human approval using RevisionIdentitiesMatch
	var matchingApprovals []ApprovalRecord
	for _, a := range approvals {
		if revisionMatches(a.SHA, a.PatchID, sha, currentPatchID, repoPath, mrFiles) {
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

// revisionMatches uses RevisionIdentitiesMatch for comprehensive revision binding.
func revisionMatches(recordSHA, recordPatchID, currentSHA, currentPatchID, repoPath string, mrFiles []string) bool {
	// Exact SHA match
	if recordSHA == currentSHA {
		return true
	}

	// Patch-ID based matching with post-rebase support
	if recordPatchID != "" && currentPatchID != "" {
		return RevisionIdentitiesMatch(recordPatchID, currentPatchID, repoPath, recordSHA, currentSHA, mrFiles)
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
