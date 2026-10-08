// Package spec implements spec lint checks for task specifications (ADR 0002 B3).
package spec

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/tier"
)

// Result holds the outcome of spec lint checks.
type Result struct {
	Tier       tier.Tier // Pre-start tier computed from allow globs
	Violations []string  // List of all violations found
}

// Check lints a spec file or content against a contract and optional standing orders.
// Returns Result with computed tier and any violations found.
// standingPath is optional; if empty, standing orders check is skipped.
func Check(specContentOrPath string, c *contract.Contract, standingPath string) (*Result, []string) {
	result := &Result{
		Tier:       tier.T1,
		Violations: []string{},
	}
	warnings := []string{}

	// Read spec content
	specContent, err := readSpecContent(specContentOrPath)
	if err != nil {
		result.Violations = append(result.Violations, fmt.Sprintf("cannot read spec: %v", err))
		return result, warnings
	}

	// Compute pre-start tier from allow globs and sensitive paths
	sensitivePaths := c.Profile.SensitivePaths
	result.Tier = tier.EvaluateFromAllowGlobs(c.Allow, sensitivePaths)

	// Run all lint checks
	checkScopeIDs(specContent, c, result)
	checkNotInScope(specContent, result)
	checkContractSnippet(specContent, c, result)
	checkRequiredMetadata(specContent, c, result)
	checkStandingOrders(specContent, standingPath, result)

	return result, warnings
}

// readSpecContent reads spec from file path or treats input as direct content.
func readSpecContent(specContentOrPath string) (string, error) {
	// Try to read as file first
	if data, err := os.ReadFile(specContentOrPath); err == nil {
		return string(data), nil
	}

	// Otherwise treat as direct content
	return specContentOrPath, nil
}

// checkScopeIDs verifies that every scope id has both a section/heading and a red-check test line.
func checkScopeIDs(specContent string, c *contract.Contract, result *Result) {
	for _, scopeID := range c.Scope {
		// Check for section/heading (in the Scope section, not anywhere)
		hasSection := hasScopeSection(specContent, scopeID)
		if !hasSection {
			result.Violations = append(result.Violations,
				fmt.Sprintf("scope id %q missing: no heading or bullet point found in Scope section", scopeID))
		}

		// Check for red-check test line
		hasRedCheck := hasRedCheckInScope(specContent, scopeID)
		if !hasRedCheck {
			result.Violations = append(result.Violations,
				fmt.Sprintf("scope id %q missing red-check: no 'red check' or 'red-check' line found in Scope section", scopeID))
		}
	}
}

// hasScopeSection checks if scopeID appears as a bullet point in the Scope section specifically.
func hasScopeSection(content, scopeID string) bool {
	lines := strings.Split(content, "\n")
	inScopeSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(line)

		// Detect "## Scope" heading
		if strings.HasPrefix(trimmed, "#") && strings.Contains(upper, "SCOPE") {
			inScopeSection = true
			continue
		}

		// Leave Scope section when we hit another heading
		if inScopeSection && strings.HasPrefix(trimmed, "#") {
			inScopeSection = false
		}

		// Look for scope ID in a bullet within the Scope section
		if inScopeSection {
			bulletRx := regexp.MustCompile(`(?i)^\s*[-*]\s+\*\*` + regexp.QuoteMeta(scopeID) + `\*\*`)
			if bulletRx.MatchString(line) {
				return true
			}
		}
	}

	return false
}

// hasRedCheckInScope checks if there's a red-check test line in the scope item's content.
func hasRedCheckInScope(content, scopeID string) bool {
	lines := strings.Split(content, "\n")
	redCheckRx := regexp.MustCompile(`(?i)red[\s-]check`)
	scopeHeadingRx := regexp.MustCompile(`(?i)^#+\s+scope\s*$`)
	bulletRx := regexp.MustCompile(`^(\s*)[-*]\s+\*\*` + regexp.QuoteMeta(scopeID) + `\*\*`)

	inScopeSection := false
	foundScopeItem := false
	scopeItemIndent := -1

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Enter Scope section
		if !inScopeSection && scopeHeadingRx.MatchString(trimmed) {
			inScopeSection = true
			continue
		}

		// Exit Scope section when we hit another heading
		if inScopeSection && strings.HasPrefix(trimmed, "#") {
			// If we found the scope item but are now leaving the section, return false
			if foundScopeItem {
				return false
			}
			inScopeSection = false
			continue
		}

		if !inScopeSection {
			continue
		}

		// Look for our scope item bullet
		matches := bulletRx.FindStringSubmatch(line)
		if len(matches) > 1 {
			foundScopeItem = true
			scopeItemIndent = len(matches[1])
			// Check this line for red-check too
			if redCheckRx.MatchString(line) {
				return true
			}
			continue
		}

		// If we've found our scope item, check subsequent lines
		if foundScopeItem {
			// Check for another scope item at the same or less indent level
			otherBulletRx := regexp.MustCompile(`^(\s*)[-*]\s+\*\*`)
			otherMatches := otherBulletRx.FindStringSubmatch(line)
			if len(otherMatches) > 1 && len(otherMatches[1]) <= scopeItemIndent {
				// Moved to another scope item, stop here
				return false
			}

			// Look for red-check in this line
			if redCheckRx.MatchString(line) {
				return true
			}
		}
	}

	// If we found the scope item but never found red-check, return false
	return false
}

// checkNotInScope verifies that spec has a "Not in scope" section.
func checkNotInScope(specContent string, result *Result) {
	notInScopeRx := regexp.MustCompile(`(?i)^#+\s+not\s+in\s+scope`)
	lines := strings.Split(specContent, "\n")

	for _, line := range lines {
		if notInScopeRx.MatchString(line) {
			return // Found it
		}
	}

	result.Violations = append(result.Violations,
		`missing "Not in scope" section: spec must have a heading containing "Not in scope" (case-insensitive)`)
}

// checkContractSnippet verifies that the contract block in spec matches c.Snippet().
func checkContractSnippet(specContent string, c *contract.Contract, result *Result) {
	expectedSnippet := c.Snippet()
	snippetLines := strings.Split(expectedSnippet, "\n")

	// Check that all lines from the snippet are present in the spec
	missing := []string{}
	for _, line := range snippetLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(specContent, line) {
			missing = append(missing, line)
		}
	}

	if len(missing) > 0 {
		result.Violations = append(result.Violations,
			fmt.Sprintf("contract snippet mismatch: %d line(s) from `rein contract show` not found in spec: %s",
				len(missing), strings.Join(missing, "; ")))
	}
}

// checkRequiredMetadata verifies presence of Report path, Gates, and Timebox.
func checkRequiredMetadata(specContent string, c *contract.Contract, result *Result) {
	// Check for Report path (either the literal string "Report path:" or the actual path)
	reportRx := regexp.MustCompile(`(?i)report\s+path\s*:`)
	hasReportPathLabel := reportRx.MatchString(specContent)
	hasActualPath := strings.Contains(specContent, c.ReportPath)

	if !hasReportPathLabel && !hasActualPath {
		result.Violations = append(result.Violations,
			`missing "Report path:": spec must contain report path metadata`)
	} else if hasReportPathLabel && !hasActualPath {
		result.Violations = append(result.Violations,
			fmt.Sprintf("report path mismatch: spec contains 'Report path:' but not the contract report path %q", c.ReportPath))
	}

	// Check for Gates
	gatesRx := regexp.MustCompile(`(?i)^gates?\s*:`)
	lines := strings.Split(specContent, "\n")
	hasGates := false
	for _, line := range lines {
		if gatesRx.MatchString(strings.TrimSpace(line)) {
			hasGates = true
			break
		}
	}
	if !hasGates {
		result.Violations = append(result.Violations,
			`missing "Gates:": spec must contain gate commands section`)
	}

	// Check for Timebox
	timeboxRx := regexp.MustCompile(`(?i)timebox\s*:\s*(\d+[hm]|\.\.\.)`)
	if !timeboxRx.MatchString(specContent) {
		result.Violations = append(result.Violations,
			`missing "Timebox:": spec must contain timebox (e.g., "Timebox: 30m" or "Timebox: ...")`)
	}
}

// checkStandingOrders verifies that the standing block in spec matches standing.md if provided.
func checkStandingOrders(specContent, standingPath string, result *Result) {
	if standingPath == "" {
		return // Skip if no standing orders file
	}

	standingContent, err := os.ReadFile(standingPath)
	if err != nil {
		// If file doesn't exist or can't be read, skip the check
		if !os.IsNotExist(err) {
			result.Violations = append(result.Violations,
				fmt.Sprintf("cannot read standing orders file %q: %v", standingPath, err))
		}
		return
	}

	standingText := strings.TrimSpace(string(standingContent))
	if standingText == "" {
		return // Empty standing orders, nothing to check
	}

	// Check if standing content appears in spec
	// The standing block should be embedded in the spec
	if !strings.Contains(specContent, standingText) {
		result.Violations = append(result.Violations,
			"standing orders mismatch: content from standing.md not found in spec")
	}
}
