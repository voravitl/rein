package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Runner manages the lifecycle of an E2E test run against Orca Browser.
type Runner struct {
	client         OrcaClient
	evidenceDir    string
	cachedSnapshot *SnapshotResult
}

// NewRunner initializes a test runner with the given Orca client and evidence directory.
func NewRunner(client OrcaClient, evidenceDir string) *Runner {
	return &Runner{
		client:      client,
		evidenceDir: evidenceDir,
	}
}

// Run executes all steps in the Spec sequentially, capturing evidence and reporting results.
func (r *Runner) Run(spec *Spec) (*TestReport, error) {
	if r.evidenceDir != "" {
		if err := os.MkdirAll(r.evidenceDir, 0o755); err != nil {
			return nil, fmt.Errorf("create evidence directory: %w", err)
		}
	}

	report := &TestReport{
		SpecName:    spec.Name,
		TotalSteps:  len(spec.Steps),
		EvidenceDir: r.evidenceDir,
		Steps:       make([]StepReport, 0, len(spec.Steps)),
	}

	startTime := time.Now()

	// Initial start URL
	startURL := "about:blank"
	if len(spec.Steps) > 0 && spec.Steps[0].Action == "goto" && spec.Steps[0].URL != "" {
		startURL = r.resolveURL(spec.BaseURL, spec.Steps[0].URL)
	}

	pageID, err := r.client.TabCreate(startURL)
	if err != nil {
		report.DurationMS = time.Since(startTime).Milliseconds()
		report.FailureMessage = fmt.Sprintf("failed to create orca browser tab: %v", err)
		r.writeReports(spec, report)
		return report, fmt.Errorf("create browser tab: %w", err)
	}

	// Guarantee cleanup of the browser tab
	defer func() {
		_ = r.client.TabClose(pageID)
	}()

	// If initial URL was loaded, update initial snapshot
	if startURL != "about:blank" {
		r.cachedSnapshot, _ = r.client.Snapshot(pageID)
	}

	allPassed := true
	for i, step := range spec.Steps {
		// If step 0 was already handled via TabCreate URL, record it and continue
		if i == 0 && step.Action == "goto" && startURL != "about:blank" {
			stepRep := StepReport{
				Index:       1,
				Action:      step.Action,
				Description: step.Description,
				DurationMS:  time.Since(startTime).Milliseconds(),
				Success:     true,
			}
			report.Steps = append(report.Steps, stepRep)
			report.PassedSteps++
			continue
		}

		stepStart := time.Now()
		stepErr := r.executeStep(pageID, spec.BaseURL, step, i+1)
		duration := time.Since(stepStart).Milliseconds()

		stepRep := StepReport{
			Index:       i + 1,
			Action:      step.Action,
			Description: step.Description,
			DurationMS:  duration,
			Success:     stepErr == nil,
		}

		if stepErr != nil {
			allPassed = false
			stepRep.Error = stepErr.Error()
			report.Steps = append(report.Steps, stepRep)
			report.FailedSteps++
			report.FailureMessage = fmt.Sprintf("Step %d (%s) failed: %v", i+1, step.Action, stepErr)

			// Capture diagnostic failure evidence
			r.captureFailureDiagnostics(pageID, i+1)
			break
		}

		report.Steps = append(report.Steps, stepRep)
		report.PassedSteps++
	}

	report.Success = allPassed
	report.DurationMS = time.Since(startTime).Milliseconds()

	r.writeReports(spec, report)
	return report, nil
}

func (r *Runner) executeStep(pageID, baseURL string, step Step, index int) error {
	switch strings.ToLower(step.Action) {
	case "goto":
		url := r.resolveURL(baseURL, step.URL)
		if err := r.client.Goto(pageID, url); err != nil {
			return fmt.Errorf("goto %s: %w", url, err)
		}
		// Refresh snapshot after navigation
		snap, err := r.client.Snapshot(pageID)
		if err == nil {
			r.cachedSnapshot = snap
		}
		return nil

	case "snapshot":
		snap, err := r.client.Snapshot(pageID)
		if err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		r.cachedSnapshot = snap
		return nil

	case "assert_text":
		if r.cachedSnapshot == nil {
			snap, err := r.client.Snapshot(pageID)
			if err != nil {
				return fmt.Errorf("snapshot before assert_text: %w", err)
			}
			r.cachedSnapshot = snap
		}
		if !strings.Contains(r.cachedSnapshot.Snapshot, step.Value) {
			return fmt.Errorf("text %q not found in page snapshot", step.Value)
		}
		return nil

	case "assert_title":
		title, err := r.client.Eval(pageID, "document.title")
		if err != nil {
			return fmt.Errorf("eval document.title: %w", err)
		}
		if !strings.Contains(title, step.Value) {
			return fmt.Errorf("title mismatch: expected to contain %q, got %q", step.Value, title)
		}
		return nil

	case "click":
		ref, err := r.resolveElement(pageID, step.Target)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		if err := r.client.Click(pageID, ref); err != nil {
			return fmt.Errorf("click %s: %w", ref, err)
		}
		time.Sleep(150 * time.Millisecond)
		// Refresh snapshot
		r.cachedSnapshot, _ = r.client.Snapshot(pageID)
		return nil

	case "fill":
		ref, err := r.resolveElement(pageID, step.Target)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		if err := r.client.Fill(pageID, ref, step.Value); err != nil {
			return fmt.Errorf("fill %s: %w", ref, err)
		}
		time.Sleep(100 * time.Millisecond)
		return nil

	case "keypress":
		key := step.Key
		if key == "" {
			key = step.Value
		}
		if err := r.client.Keypress(pageID, key); err != nil {
			return fmt.Errorf("keypress %s: %w", key, err)
		}
		time.Sleep(100 * time.Millisecond)
		return nil

	case "eval":
		val, err := r.client.Eval(pageID, step.Expression)
		if err != nil {
			return fmt.Errorf("eval expression %q: %w", step.Expression, err)
		}
		if step.AssertValue != "" && val != step.AssertValue {
			return fmt.Errorf("eval assert failed: expected %q, got %q", step.AssertValue, val)
		}
		return nil

	case "screenshot":
		raw, err := r.client.Screenshot(pageID)
		if err != nil {
			return fmt.Errorf("screenshot: %w", err)
		}
		if r.evidenceDir != "" {
			filename := step.Filename
			if filename == "" {
				filename = fmt.Sprintf("step-%02d-screenshot.png", index)
			}
			outPath := filepath.Join(r.evidenceDir, filename)
			if err := os.WriteFile(outPath, raw, 0o644); err != nil {
				return fmt.Errorf("save screenshot: %w", err)
			}
		}
		return nil

	case "wait":
		if step.Value != "" {
			// Poll until text appears in snapshot or timeout
			timeout := 5 * time.Second
			if step.TimeoutMS > 0 {
				timeout = time.Duration(step.TimeoutMS) * time.Millisecond
			}
			deadline := time.Now().Add(timeout)
			for time.Now().Before(deadline) {
				snap, err := r.client.Snapshot(pageID)
				if err == nil {
					r.cachedSnapshot = snap
					if strings.Contains(snap.Snapshot, step.Value) {
						return nil
					}
				}
				time.Sleep(200 * time.Millisecond)
			}
			return fmt.Errorf("timed out waiting for text %q after %v", step.Value, timeout)
		}
		// Pure sleep timeout
		sleepDuration := time.Duration(step.TimeoutMS) * time.Millisecond
		if sleepDuration <= 0 {
			sleepDuration = 500 * time.Millisecond
		}
		time.Sleep(sleepDuration)
		return nil

	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
}

func (r *Runner) resolveURL(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "about:") {
		return path
	}
	base = strings.TrimSuffix(base, "/")
	path = strings.TrimPrefix(path, "/")
	if base == "" {
		return path
	}
	return base + "/" + path
}

func (r *Runner) resolveElement(pageID, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("target element selector is empty")
	}

	// Direct ref format (@e1, e1)
	clean := strings.TrimPrefix(target, "@")
	if strings.HasPrefix(clean, "e") && len(clean) > 1 {
		isNumeric := true
		for _, ch := range clean[1:] {
			if ch < '0' || ch > '9' {
				isNumeric = false
				break
			}
		}
		if isNumeric {
			return clean, nil
		}
	}

	// Lookup by name in current or fresh snapshot
	if r.cachedSnapshot == nil {
		snap, err := r.client.Snapshot(pageID)
		if err != nil {
			return "", fmt.Errorf("snapshot for element lookup: %w", err)
		}
		r.cachedSnapshot = snap
	}

	// Exact match
	for ref, el := range r.cachedSnapshot.Refs {
		if el.Name == target {
			return ref, nil
		}
	}

	// Case-insensitive match
	for ref, el := range r.cachedSnapshot.Refs {
		if strings.EqualFold(el.Name, target) {
			return ref, nil
		}
	}

	// Substring match
	targetLower := strings.ToLower(target)
	for ref, el := range r.cachedSnapshot.Refs {
		if strings.Contains(strings.ToLower(el.Name), targetLower) {
			return ref, nil
		}
	}

	return "", fmt.Errorf("element %q not found in snapshot refs (%d refs found)", target, len(r.cachedSnapshot.Refs))
}

func (r *Runner) captureFailureDiagnostics(pageID string, stepIndex int) {
	if r.evidenceDir == "" {
		return
	}
	// Try saving failure screenshot
	if raw, err := r.client.Screenshot(pageID); err == nil {
		_ = os.WriteFile(filepath.Join(r.evidenceDir, fmt.Sprintf("failure-step-%02d.png", stepIndex)), raw, 0o644)
	}
	// Try saving failure snapshot
	if snap, err := r.client.Snapshot(pageID); err == nil {
		_ = os.WriteFile(filepath.Join(r.evidenceDir, fmt.Sprintf("failure-step-%02d.txt", stepIndex)), []byte(snap.Snapshot), 0o644)
	}
}

func (r *Runner) writeReports(spec *Spec, report *TestReport) {
	if r.evidenceDir == "" {
		return
	}
	// Write report.json
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(r.evidenceDir, "report.json"), data, 0o644)
	}

	// Write report.md
	var sb strings.Builder
	statusBadge := "PASSED"
	if !report.Success {
		statusBadge = "FAILED"
	}

	sb.WriteString(fmt.Sprintf("# E2E Test Report: %s\n\n", report.SpecName))
	sb.WriteString(fmt.Sprintf("- **Verdict**: **%s**\n", statusBadge))
	sb.WriteString(fmt.Sprintf("- **Total Steps**: %d\n", report.TotalSteps))
	sb.WriteString(fmt.Sprintf("- **Passed**: %d\n", report.PassedSteps))
	sb.WriteString(fmt.Sprintf("- **Failed**: %d\n", report.FailedSteps))
	sb.WriteString(fmt.Sprintf("- **Duration**: %d ms\n", report.DurationMS))
	if report.FailureMessage != "" {
		sb.WriteString(fmt.Sprintf("- **Failure**: `%s`\n", report.FailureMessage))
	}
	sb.WriteString("\n## Step Execution Summary\n\n")
	sb.WriteString("| # | Action | Description | Result | Duration |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, st := range report.Steps {
		res := "PASS"
		if !st.Success {
			res = "FAIL"
		}
		desc := st.Description
		if desc == "" {
			desc = "-"
		}
		sb.WriteString(fmt.Sprintf("| %d | `%s` | %s | %s | %d ms |\n", st.Index, st.Action, desc, res, st.DurationMS))
	}
	_ = os.WriteFile(filepath.Join(r.evidenceDir, "report.md"), []byte(sb.String()), 0o644)

	// Automatically build Board-PDF report
	pdfPath, err := BuildBoardPDF(spec, report, r.evidenceDir)
	if err == nil && pdfPath != "" {
		report.PDFPath = pdfPath
		if data, err := json.MarshalIndent(report, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(r.evidenceDir, "report.json"), data, 0o644)
		}
	}
}
