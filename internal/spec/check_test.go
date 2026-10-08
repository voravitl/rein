package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/tier"
)

func TestCheck_FullyValidSpec(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1", "S2"},
		Allow:      []string{"src/**"},
		Deny:       []string{"**/*.secret"},
		ReportPath: "/reports/test-task.md",
		Profile: contract.Profile{
			SensitivePaths: []string{"secrets/**"},
		},
	}

	validSpec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify behavior
- **S2** Implement feature B
  - red-check for S2

## Ownership (machine-checked; the guard hook and the coordinator's drift check enforce it)
- Edit ONLY files matching: src/**
- Never edit: **/*.secret
- Scope ids: your report needs one heading per id (S1, S2) with status done / partly / not done
- Report path (write it before you finish): /reports/test-task.md

## Not in scope
- Future enhancements
- Other features

## Gates
Gates: make test && go vet

## Timebox
Timebox: 2h
`

	result, warnings := Check(validSpec, c, "")

	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}

	if len(result.Violations) > 0 {
		t.Errorf("expected no violations for valid spec, got: %v", result.Violations)
	}

	if result.Tier != tier.T1 {
		t.Errorf("expected tier T1, got: %v", result.Tier)
	}
}

func TestCheck_MissingScopeSection(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1", "S2"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify behavior

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

	result, _ := Check(spec, c, "")

	// Should find missing S2 section
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v, "S2") && strings.Contains(v, "missing") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for missing S2 section, got: %v", result.Violations)
	}
}

func TestCheck_MissingRedCheck(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - some test here

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

	result, _ := Check(spec, c, "")

	// Should find missing red-check for S1
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v, "S1") && strings.Contains(v, "red-check") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for missing red-check, got: %v", result.Violations)
	}
}

func TestCheck_MissingNotInScope(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

Gates: make test
Timebox: 2h
`

	result, _ := Check(spec, c, "")

	// Should find missing "Not in scope"
	found := false
	for _, v := range result.Violations {
		if strings.Contains(strings.ToLower(v), "not in scope") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for missing 'Not in scope', got: %v", result.Violations)
	}
}

func TestCheck_MismatchedContractBlock(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		Deny:       []string{"**/*.secret"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

## Ownership
- Edit ONLY files matching: wrong/**
- Never edit: wrong.secret

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

	result, _ := Check(spec, c, "")

	// Should find contract snippet mismatch
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v, "contract snippet mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for contract snippet mismatch, got: %v", result.Violations)
	}
}

func TestCheck_MissingReportPath(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

	// The snippet should contain report path, so this should pass
	// Let's test when snippet is missing
	specWithoutReportPath := strings.Replace(spec, "Report path", "Wrong text", 1)
	result2, _ := Check(specWithoutReportPath, c, "")

	found := false
	for _, v := range result2.Violations {
		if strings.Contains(strings.ToLower(v), "report path") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for missing report path, got: %v", result2.Violations)
	}
}

func TestCheck_MissingGates(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

## Not in scope
- Other features

Timebox: 2h
`

	result, _ := Check(spec, c, "")

	found := false
	for _, v := range result.Violations {
		if strings.Contains(strings.ToLower(v), "gates") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for missing gates, got: %v", result.Violations)
	}
}

func TestCheck_MissingTimebox(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
`

	result, _ := Check(spec, c, "")

	found := false
	for _, v := range result.Violations {
		if strings.Contains(strings.ToLower(v), "timebox") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for missing timebox, got: %v", result.Violations)
	}
}

func TestCheck_MismatchedStandingOrders(t *testing.T) {
	tmpDir := t.TempDir()
	standingPath := filepath.Join(tmpDir, "standing.md")

	standingContent := `## Standing Orders
- Always write tests
- Always document changes
`
	err := os.WriteFile(standingPath, []byte(standingContent), 0644)
	if err != nil {
		t.Fatalf("failed to create standing.md: %v", err)
	}

	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	specWithoutStanding := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

	result, _ := Check(specWithoutStanding, c, standingPath)

	found := false
	for _, v := range result.Violations {
		if strings.Contains(v, "standing orders mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation for standing orders mismatch, got: %v", result.Violations)
	}

	// Test with standing orders included
	specWithStanding := specWithoutStanding + "\n" + standingContent
	result2, _ := Check(specWithStanding, c, standingPath)

	found2 := false
	for _, v := range result2.Violations {
		if strings.Contains(v, "standing orders") {
			found2 = true
			break
		}
	}
	if found2 {
		t.Errorf("expected no standing orders violation when content matches, got: %v", result2.Violations)
	}
}

func TestCheck_TierComputation(t *testing.T) {
	tests := []struct {
		name         string
		allow        []string
		sensitive    []string
		expectedTier tier.Tier
	}{
		{
			name:         "no sensitive paths",
			allow:        []string{"src/**"},
			sensitive:    []string{},
			expectedTier: tier.T1,
		},
		{
			name:         "allow matches sensitive",
			allow:        []string{"secrets/**"},
			sensitive:    []string{"secrets/**"},
			expectedTier: tier.T3,
		},
		{
			name:         "allow does not match sensitive",
			allow:        []string{"src/**"},
			sensitive:    []string{"secrets/**"},
			expectedTier: tier.T1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &contract.Contract{
				Name:       "test-task",
				Scope:      []string{"S1"},
				Allow:      tt.allow,
				ReportPath: "/reports/test-task.md",
				Profile: contract.Profile{
					SensitivePaths: tt.sensitive,
				},
			}

			validSpec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

			result, _ := Check(validSpec, c, "")

			if result.Tier != tt.expectedTier {
				t.Errorf("expected tier %v, got: %v", tt.expectedTier, result.Tier)
			}
		})
	}
}

func TestCheck_ReadFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "spec.md")

	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	validSpec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

## Not in scope
- Other features

Gates: make test
Timebox: 2h
`

	err := os.WriteFile(specPath, []byte(validSpec), 0644)
	if err != nil {
		t.Fatalf("failed to create spec file: %v", err)
	}

	result, warnings := Check(specPath, c, "")

	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}

	if len(result.Violations) > 0 {
		t.Errorf("expected no violations for valid spec file, got: %v", result.Violations)
	}
}

func TestCheck_MultipleViolations(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1", "S2"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	// Spec with multiple violations
	badSpec := `# Task: Test Task

## Scope
- **S1** Implement feature A

Some content here but no S2 and no red checks
`

	result, _ := Check(badSpec, c, "")

	// Should have multiple violations
	if len(result.Violations) < 5 {
		t.Errorf("expected at least 5 violations (missing S2, missing red-checks, missing not in scope, missing contract, missing gates, missing timebox), got %d: %v",
			len(result.Violations), result.Violations)
	}
}

func TestCheck_CaseInsensitiveNotInScope(t *testing.T) {
	c := &contract.Contract{
		Name:       "test-task",
		Scope:      []string{"S1"},
		Allow:      []string{"src/**"},
		ReportPath: "/reports/test-task.md",
		Profile:    contract.Profile{},
	}

	variants := []string{
		"## Not in scope",
		"## NOT IN SCOPE",
		"## not in scope",
		"## Not In Scope",
		"# NOT IN SCOPE",
	}

	for _, variant := range variants {
		spec := `# Task: Test Task

## Scope
- **S1** Implement feature A
  - red check: verify

` + c.Snippet() + `

` + variant + `
- Other features

Gates: make test
Timebox: 2h
`

		result, _ := Check(spec, c, "")

		found := false
		for _, v := range result.Violations {
			if strings.Contains(strings.ToLower(v), "not in scope") {
				found = true
				break
			}
		}
		if found {
			t.Errorf("variant %q should be accepted but got violation: %v", variant, result.Violations)
		}
	}
}
