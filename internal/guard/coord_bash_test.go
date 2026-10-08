package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// TestCoordWorkerStart_SpecLintValid verifies that worker start with a valid spec is allowed.
func TestCoordWorkerStart_SpecLintValid(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid spec file
	specContent := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify behavior

## Ownership (machine-checked; the guard hook and the coordinator's drift check enforce it)
- Edit ONLY files matching: src/**
- Never edit: 
- Scope ids: your report needs one heading per id (S1) with status done / partly / not done
- Report path (write it before you finish): ` + tmpDir + `/reports/test-task.md

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`
	specPath := filepath.Join(tmpDir, "spec.md")
	if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
		t.Fatalf("failed to create spec file: %v", err)
	}

	// Create a contract
	contractsDir := filepath.Join(tmpDir, "contracts")
	if err := os.MkdirAll(contractsDir, 0755); err != nil {
		t.Fatalf("failed to create contracts dir: %v", err)
	}

	// Temporarily override the contracts dir before Save
	t.Setenv("PIPELINE_CONTRACTS", contractsDir)

	worktreePath := filepath.Join(tmpDir, "worktrees/test-task")
	c := &contract.Contract{
		Name:       "test-task",
		Worktree:   worktreePath,
		Allow:      []string{"src/**"},
		Scope:      []string{"S1"},
		ReportPath: filepath.Join(tmpDir, "reports/test-task.md"),
		Profile:    contract.Profile{},
	}
	if _, err := c.Save(); err != nil {
		t.Fatalf("failed to save contract: %v", err)
	}

	// Create worktree directory and link it
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("failed to create worktree dir: %v", err)
	}
	linkWorktree(t, tmpDir, worktreePath, "test-task")

	// Create git common dir and write a recent tick file
	commonDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatalf("failed to create common dir: %v", err)
	}
	tickPath := run.TickPath(commonDir)
	if err := os.MkdirAll(filepath.Dir(tickPath), 0755); err != nil {
		t.Fatalf("failed to create tick dir: %v", err)
	}
	tickContent := `{"timestamp":"` + time.Now().Format(time.RFC3339) + `","pid":12345,"status":"ok"}`
	if err := os.WriteFile(tickPath, []byte(tickContent), 0644); err != nil {
		t.Fatalf("failed to write tick: %v", err)
	}

	// Create coordPolicy
	loc := run.Loc{Top: tmpDir, Common: commonDir}
	p := &coordPolicy{
		m:   &run.Marker{},
		loc: loc,
	}

	// Test worker start with valid spec
	args := []string{
		"orchestration", "worker-start",
		"--spec", specPath,
		"--task", "test-task",
		"--worktree", "path:" + worktreePath,
	}

	x := &ctx{c: &contract.Contract{}, top: tmpDir, cwd: tmpDir, vars: map[string]string{}, coord: p}
	reason := x.coordWorkerStart(args[2:]) // Skip "orchestration worker-start"

	if reason != "" {
		t.Errorf("expected worker start with valid spec to be allowed, got denial: %s", reason)
	}
}

// TestCoordWorkerStart_SpecLintInvalid verifies that worker start with invalid spec is denied.
func TestCoordWorkerStart_SpecLintInvalid(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an invalid spec file (missing "Not in scope")
	specContent := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify behavior

## Ownership (machine-checked; the guard hook and the coordinator's drift check enforce it)
- Edit ONLY files matching: src/**
- Never edit: 
- Scope ids: your report needs one heading per id (S1) with status done / partly / not done
- Report path (write it before you finish): ` + tmpDir + `/reports/test-task.md

Gates: make test
Timebox: 2h
`
	specPath := filepath.Join(tmpDir, "spec.md")
	if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
		t.Fatalf("failed to create spec file: %v", err)
	}

	// Create a contract
	contractsDir := filepath.Join(tmpDir, "contracts")
	if err := os.MkdirAll(contractsDir, 0755); err != nil {
		t.Fatalf("failed to create contracts dir: %v", err)
	}

	oldContracts := os.Getenv("PIPELINE_CONTRACTS")
	os.Setenv("PIPELINE_CONTRACTS", contractsDir)
	defer os.Setenv("PIPELINE_CONTRACTS", oldContracts)

	c := &contract.Contract{
		Name:       "test-task",
		Worktree:   filepath.Join(tmpDir, "worktrees/test-task"),
		Allow:      []string{"src/**"},
		Scope:      []string{"S1"},
		ReportPath: filepath.Join(tmpDir, "reports/test-task.md"),
		Profile:    contract.Profile{},
	}
	if _, err := c.Save(); err != nil {
		t.Fatalf("failed to save contract: %v", err)
	}

	// Create git common dir and write a recent tick file
	commonDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatalf("failed to create common dir: %v", err)
	}
	tickPath := run.TickPath(commonDir)
	if err := os.MkdirAll(filepath.Dir(tickPath), 0755); err != nil {
		t.Fatalf("failed to create tick dir: %v", err)
	}
	tickContent := `{"timestamp":"` + time.Now().Format(time.RFC3339) + `","pid":12345,"status":"ok"}`
	if err := os.WriteFile(tickPath, []byte(tickContent), 0644); err != nil {
		t.Fatalf("failed to write tick: %v", err)
	}

	// Create coordPolicy
	loc := run.Loc{Top: tmpDir, Common: commonDir}
	p := &coordPolicy{
		m:   &run.Marker{},
		loc: loc,
	}

	// Test worker start with invalid spec
	args := []string{
		"orchestration", "worker-start",
		"--spec", specPath,
		"--task", "test-task",
		"--worktree", "path:" + filepath.Join(tmpDir, "worktrees/test-task"),
	}

	x := &ctx{c: &contract.Contract{}, top: tmpDir, cwd: tmpDir, vars: map[string]string{}, coord: p}
	reason := x.coordWorkerStart(args[2:])

	if reason == "" {
		t.Error("expected worker start with invalid spec to be denied, but was allowed")
	}

	if !strings.Contains(reason, "SPEC_LINT_FAILED") {
		t.Errorf("expected denial to mention SPEC_LINT_FAILED, got: %s", reason)
	}

	if !strings.Contains(strings.ToLower(reason), "not in scope") {
		t.Errorf("expected denial to mention missing 'Not in scope', got: %s", reason)
	}
}

// TestCoordWorkerStart_SpecLintMissingRedCheck verifies denial when red-check is missing.
func TestCoordWorkerStart_SpecLintMissingRedCheck(t *testing.T) {
	tmpDir := t.TempDir()

	// Create spec without red-check
	specContent := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - some test here

## Ownership (machine-checked; the guard hook and the coordinator's drift check enforce it)
- Edit ONLY files matching: src/**
- Never edit: 
- Scope ids: your report needs one heading per id (S1) with status done / partly / not done
- Report path (write it before you finish): ` + tmpDir + `/reports/test-task.md

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`
	specPath := filepath.Join(tmpDir, "spec.md")
	if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
		t.Fatalf("failed to create spec file: %v", err)
	}

	contractsDir := filepath.Join(tmpDir, "contracts")
	if err := os.MkdirAll(contractsDir, 0755); err != nil {
		t.Fatalf("failed to create contracts dir: %v", err)
	}

	oldContracts := os.Getenv("PIPELINE_CONTRACTS")
	os.Setenv("PIPELINE_CONTRACTS", contractsDir)
	defer os.Setenv("PIPELINE_CONTRACTS", oldContracts)

	c := &contract.Contract{
		Name:       "test-task",
		Worktree:   filepath.Join(tmpDir, "worktrees/test-task"),
		Allow:      []string{"src/**"},
		Scope:      []string{"S1"},
		ReportPath: filepath.Join(tmpDir, "reports/test-task.md"),
		Profile:    contract.Profile{},
	}
	if _, err := c.Save(); err != nil {
		t.Fatalf("failed to save contract: %v", err)
	}

	// Create git common dir and write a recent tick file
	commonDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatalf("failed to create common dir: %v", err)
	}
	tickPath := run.TickPath(commonDir)
	if err := os.MkdirAll(filepath.Dir(tickPath), 0755); err != nil {
		t.Fatalf("failed to create tick dir: %v", err)
	}
	tickContent := `{"timestamp":"` + time.Now().Format(time.RFC3339) + `","pid":12345,"status":"ok"}`
	if err := os.WriteFile(tickPath, []byte(tickContent), 0644); err != nil {
		t.Fatalf("failed to write tick: %v", err)
	}

	loc := run.Loc{Top: tmpDir, Common: commonDir}
	p := &coordPolicy{
		m:   &run.Marker{},
		loc: loc,
	}

	args := []string{
		"orchestration", "worker-start",
		"--spec", specPath,
		"--task", "test-task",
		"--worktree", "path:" + filepath.Join(tmpDir, "worktrees/test-task"),
	}

	x := &ctx{c: &contract.Contract{}, top: tmpDir, cwd: tmpDir, vars: map[string]string{}, coord: p}
	reason := x.coordWorkerStart(args[2:])

	if reason == "" {
		t.Error("expected worker start with missing red-check to be denied, but was allowed")
	}

	if !strings.Contains(reason, "red-check") {
		t.Errorf("expected denial to mention missing red-check, got: %s", reason)
	}
}

// TestCoordWorkerStart_NoSpecFlag verifies that worker start without --spec flag is allowed.
func TestCoordWorkerStart_NoSpecFlag(t *testing.T) {
	tmpDir := t.TempDir()

	// Create git common dir and write a recent tick file
	commonDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatalf("failed to create common dir: %v", err)
	}
	tickPath := run.TickPath(commonDir)
	if err := os.MkdirAll(filepath.Dir(tickPath), 0755); err != nil {
		t.Fatalf("failed to create tick dir: %v", err)
	}
	tickContent := `{"timestamp":"` + time.Now().Format(time.RFC3339) + `","pid":12345,"status":"ok"}`
	if err := os.WriteFile(tickPath, []byte(tickContent), 0644); err != nil {
		t.Fatalf("failed to write tick: %v", err)
	}

	loc := run.Loc{Top: tmpDir, Common: commonDir}
	p := &coordPolicy{
		m:   &run.Marker{},
		loc: loc,
	}

	// Worker start without --spec flag should not trigger spec lint
	args := []string{
		"orchestration", "worker-start",
		"--worktree", "new-worker",
	}

	x := &ctx{c: &contract.Contract{}, top: tmpDir, cwd: tmpDir, vars: map[string]string{}, coord: p}
	reason := x.coordWorkerStart(args[2:])

	// Should pass (new worktree, no spec check)
	if reason != "" {
		t.Errorf("expected worker start without --spec to be allowed, got: %s", reason)
	}
}
