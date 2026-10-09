package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Step defines an atomic action in an E2E test sequence.
type Step struct {
	Action      string `json:"action"`                 // goto, wait, snapshot, assert_text, assert_title, click, fill, keypress, eval, screenshot
	URL         string `json:"url,omitempty"`          // for goto
	Target      string `json:"target,omitempty"`       // element ref (@e1, e1) or element name/text for click/fill
	Value       string `json:"value,omitempty"`        // for fill, assert_text, assert_title, or wait text
	Key         string `json:"key,omitempty"`          // for keypress (Enter, Tab, Escape, etc.)
	Expression  string `json:"expression,omitempty"`   // for eval
	AssertValue string `json:"assert_value,omitempty"` // for eval assertion
	Filename    string `json:"filename,omitempty"`     // for screenshot
	TimeoutMS   int    `json:"timeout_ms,omitempty"`   // for wait or per-step timeout
	Description string `json:"description,omitempty"`  // human-readable note
}

// Spec defines an E2E scenario executed against Orca Browser.
type Spec struct {
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"` // default 30
	Steps          []Step `json:"steps"`
}

// StepReport captures the execution details and evidence for a single step.
type StepReport struct {
	Index       int    `json:"index"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	Evidence    string `json:"evidence,omitempty"` // relative path to screenshot or snapshot excerpt
}

// TestReport is the final aggregated outcome of an E2E scenario run.
type TestReport struct {
	SpecName       string       `json:"spec_name"`
	TotalSteps     int          `json:"total_steps"`
	PassedSteps    int          `json:"passed_steps"`
	FailedSteps    int          `json:"failed_steps"`
	Success        bool         `json:"success"`
	DurationMS     int64        `json:"duration_ms"`
	FailureMessage string       `json:"failure_message,omitempty"`
	EvidenceDir    string       `json:"evidence_dir,omitempty"`
	Steps          []StepReport `json:"steps"`
}

var jsonBlockRx = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// LoadSpec reads and parses an E2E test spec from JSON or markdown file containing a JSON block.
func LoadSpec(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec file: %w", err)
	}

	content := strings.TrimSpace(string(data))
	if m := jsonBlockRx.FindStringSubmatch(content); len(m) > 1 {
		content = m[1]
	}

	var s Spec
	if err := json.Unmarshal([]byte(content), &s); err != nil {
		return nil, fmt.Errorf("parse spec json: %w", err)
	}

	if s.Name == "" {
		s.Name = "unnamed-e2e-spec"
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 30
	}
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("spec has no steps defined")
	}

	return &s, nil
}
