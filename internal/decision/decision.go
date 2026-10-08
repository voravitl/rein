// Package decision records AskUserQuestion decisions to a TSV file in the run directory.
package decision

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Secret patterns to scan for and redact
var secretPatterns = []*regexp.Regexp{
	// AWS keys
	regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),
	// GitHub tokens (more specific patterns)
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`gho_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`ghu_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`ghr_[A-Za-z0-9]{36,}`),
	// GitLab tokens
	regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`),
	// Bearer tokens
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]+=*`),
	// OpenAI-style API keys
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	// Generic password assignments
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*["']?[^\s"']+["']?`),
	// Generic API key assignments
	regexp.MustCompile(`(?i)(api[_-]?key|apikey|access[_-]?token)\s*[:=]\s*["']?[^\s"']+["']?`),
}

// ScanSecrets scans text for secrets and redacts them with [REDACTED].
func ScanSecrets(text string) string {
	result := text
	for _, pattern := range secretPatterns {
		result = pattern.ReplaceAllString(result, "[REDACTED]")
	}
	return result
}

// sanitizeTSVField cleans a field for TSV format: removes tabs and newlines so each decision is one line.
func sanitizeTSVField(s string) string {
	// Replace tabs with spaces
	s = strings.ReplaceAll(s, "\t", " ")
	// Replace newlines with spaces
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	// Collapse multiple spaces
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// Append appends a decision to <runDir>/decisions.tsv.
// Fields: timestamp (RFC3339), question text, chosen option label, free text / notes.
// Free text is scanned for secrets and redacted. Even if empty, all 4 fields are written.
func Append(runDir, question, chosenOption, freeText string) error {
	// Scan free text for secrets (even if empty, still process)
	freeText = ScanSecrets(freeText)

	// Sanitize all fields for TSV
	question = sanitizeTSVField(question)
	chosenOption = sanitizeTSVField(chosenOption)
	freeText = sanitizeTSVField(freeText)

	// Build TSV line - all 4 fields always present
	timestamp := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n", timestamp, question, chosenOption, freeText)

	// Ensure run directory exists
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}

	// Append to decisions.tsv
	path := filepath.Join(runDir, "decisions.tsv")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open decisions.tsv: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write decision: %w", err)
	}

	return nil
}
