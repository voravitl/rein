package verdict

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/voravitl/rein/internal/tier"
)

// RecordVerdict saves a verdict record to the appropriate file.
func RecordVerdict(storageDir string, mr int, sha string, verdict Verdict, reviewerModel, workerModel string) error {
	if err := EnsureDir(storageDir); err != nil {
		return fmt.Errorf("failed to create storage dir: %w", err)
	}

	// Compute patch ID (may be empty if we're not in the repo)
	patchID := ""

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
	Passed         bool
	Tier           tier.Tier
	MakerCount     int
	HasApproval    bool
	MissingReasons []string
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

	// Check verdicts for maker diversity
	makers := make(map[string]bool)
	workerMaker := ""

	for _, v := range verdicts {
		if v.SHA != sha && v.PatchID != currentPatchID {
			continue
		}
		if v.Verdict != Approve {
			continue
		}

		reviewerMaker := NormalizeModelMaker(v.ReviewerModel)
		if workerMaker == "" {
			workerMaker = NormalizeModelMaker(v.WorkerModel)
		}

		// Reviewer maker must not equal worker maker
		if reviewerMaker == workerMaker {
			continue
		}

		makers[reviewerMaker] = true
	}

	result.MakerCount = len(makers)

	// Check maker count requirement
	requiredMakers := 1
	if taskTier == tier.T3 {
		requiredMakers = 2
	}

	if result.MakerCount < requiredMakers {
		result.MissingReasons = append(result.MissingReasons,
			fmt.Sprintf("need verdicts from %d distinct model maker(s), have %d", requiredMakers, result.MakerCount))
	}

	// Check for human approval
	for _, a := range approvals {
		if a.SHA == sha {
			result.HasApproval = true
			break
		}
		if a.PatchID != "" && a.PatchID == currentPatchID {
			result.HasApproval = true
			break
		}
	}

	if !result.HasApproval {
		result.MissingReasons = append(result.MissingReasons, "no human approval for this revision")
	}

	result.Passed = len(result.MissingReasons) == 0
	return result, nil
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
