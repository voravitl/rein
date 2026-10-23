package e2e

import (
	"context"
	"encoding/json"
	"errors"
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
	r.cachedSnapshot = nil
	if r.evidenceDir != "" {
		if err := os.MkdirAll(r.evidenceDir, 0o755); err != nil {
			return nil, fmt.Errorf("create evidence directory: %w", err)
		}
	}

	// The scenario deadline bounds step execution; clients that support it have
	// their browser calls bounded too. Cleanup and failure diagnostics keep
	// using r.client so they still run once the deadline expires.
	ctx := context.Background()
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	client := r.client
	if sc, ok := client.(scenarioClient); ok {
		client = sc.WithContext(ctx)
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

	pageID, err := client.TabCreate(startURL)
	if err != nil {
		report.DurationMS = time.Since(startTime).Milliseconds()
		report.FailureMessage = fmt.Sprintf("failed to create orca browser tab: %v", err)
		return report, errors.Join(fmt.Errorf("create browser tab: %w", err), r.writeReports(spec, report))
	}

	defer func() {
		_ = r.client.TabClose(pageID)
	}()

	if startURL != "about:blank" {
		r.cachedSnapshot, _ = client.Snapshot(pageID)
	}

	allPassed := true
	for i, step := range spec.Steps {
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
		stepErr := scenarioErr(ctx)
		if stepErr == nil {
			stepErr = r.executeStep(ctx, client, pageID, spec.BaseURL, step, i+1)
		}
		if stepErr == nil {
			stepErr = scenarioErr(ctx)
		}
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

	return report, r.writeReports(spec, report)
}

func (r *Runner) executeStep(ctx context.Context, client OrcaClient, pageID, baseURL string, step Step, index int) error {
	r.cachedSnapshot = nil
	switch strings.ToLower(step.Action) {
	case "goto":
		url := r.resolveURL(baseURL, step.URL)
		if err := client.Goto(pageID, url); err != nil {
			return fmt.Errorf("goto %s: %w", url, err)
		}
		// Refresh snapshot after navigation
		snap, err := client.Snapshot(pageID)
		if err == nil {
			r.cachedSnapshot = snap
		}
		return nil

	case "snapshot":
		snap, err := client.Snapshot(pageID)
		if err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		r.cachedSnapshot = snap
		return nil

	case "assert_text":
		snap, err := client.Snapshot(pageID)
		if err != nil {
			return fmt.Errorf("snapshot before assert_text: %w", err)
		}
		if snap == nil {
			return fmt.Errorf("empty snapshot before assert_text")
		}
		r.cachedSnapshot = snap
		if !strings.Contains(r.cachedSnapshot.Snapshot, step.Value) {
			return fmt.Errorf("text %q not found in page snapshot", step.Value)
		}
		return nil

	case "assert_title":
		title, err := client.Eval(pageID, "document.title")
		if err != nil {
			return fmt.Errorf("eval document.title: %w", err)
		}
		if !strings.Contains(title, step.Value) {
			return fmt.Errorf("title mismatch: expected to contain %q, got %q", step.Value, title)
		}
		return nil

	case "click":
		ref, err := r.resolveElement(client, pageID, step.Target)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		if err := client.Click(pageID, ref); err != nil {
			return fmt.Errorf("click %s: %w", ref, err)
		}
		if err := waitScenario(ctx, 150*time.Millisecond); err != nil {
			return err
		}
		// Refresh snapshot
		r.cachedSnapshot, _ = client.Snapshot(pageID)
		return nil

	case "fill":
		ref, err := r.resolveElement(client, pageID, step.Target)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		if err := client.Fill(pageID, ref, step.Value); err != nil {
			return fmt.Errorf("fill %s: %w", ref, err)
		}
		if err := waitScenario(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		return nil

	case "keypress":
		key := step.Key
		if key == "" {
			key = step.Value
		}
		if err := client.Keypress(pageID, key); err != nil {
			return fmt.Errorf("keypress %s: %w", key, err)
		}
		if err := waitScenario(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		return nil

	case "eval":
		val, err := client.Eval(pageID, step.Expression)
		if err != nil {
			return fmt.Errorf("eval expression %q: %w", step.Expression, err)
		}
		if step.AssertValue != "" && val != step.AssertValue {
			return fmt.Errorf("eval assert failed: expected %q, got %q", step.AssertValue, val)
		}
		return nil

	case "screenshot":
		raw, err := client.Screenshot(pageID)
		if err != nil {
			return fmt.Errorf("screenshot: %w", err)
		}
		if r.evidenceDir != "" {
			filename := step.Filename
			if filename == "" {
				filename = fmt.Sprintf("step-%02d-screenshot.png", index)
			}
			outPath, err := r.evidencePath(filename)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return fmt.Errorf("create screenshot directory: %w", err)
			}
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
			for {
				if err := scenarioErr(ctx); err != nil {
					return err
				}
				snap, err := client.Snapshot(pageID)
				if err == nil {
					r.cachedSnapshot = snap
					if strings.Contains(snap.Snapshot, step.Value) {
						return nil
					}
				}
				if !time.Now().Before(deadline) {
					return fmt.Errorf("timed out waiting for text %q after %v", step.Value, timeout)
				}
				if err := waitScenario(ctx, 200*time.Millisecond); err != nil {
					return err
				}
			}
		}
		// Pure sleep timeout
		sleepDuration := time.Duration(step.TimeoutMS) * time.Millisecond
		if sleepDuration <= 0 {
			sleepDuration = 500 * time.Millisecond
		}
		return waitScenario(ctx, sleepDuration)

	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
}

// scenarioErr reports an expired scenario deadline as a step error.
func scenarioErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("scenario timed out: %w", err)
	}
	return nil
}

// waitScenario sleeps for d unless the scenario deadline expires first.
func waitScenario(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return scenarioErr(ctx)
	}
}

// evidencePath resolves filename inside the evidence directory, rejecting
// traversal and symlinked destinations that would escape it.
func (r *Runner) evidencePath(filename string) (string, error) {
	root, err := filepath.Abs(r.evidenceDir)
	if err != nil {
		return "", fmt.Errorf("resolve evidence directory: %w", err)
	}
	dest := filepath.Join(root, filename)
	if !pathWithin(root, dest) {
		return "", fmt.Errorf("screenshot filename %q escapes evidence directory", filename)
	}
	// os.WriteFile follows a symlink at dest, and a dangling link would create
	// its target outside the evidence directory, so refuse destinations outright.
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("screenshot filename %q is a symlink", filename)
	}

	// Symlinked parents are invisible to the lexical check above, so compare
	// resolved paths before creating anything.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve evidence directory symlinks: %w", err)
	}
	resolvedDest, err := resolveExistingPrefix(dest)
	if err != nil {
		return "", fmt.Errorf("resolve screenshot path: %w", err)
	}
	if !pathWithin(resolvedRoot, resolvedDest) {
		return "", fmt.Errorf("screenshot filename %q escapes evidence directory via symlink", filename)
	}
	return dest, nil
}

// pathWithin reports whether p stays inside root.
func pathWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExistingPrefix resolves symlinks in the longest existing prefix of p,
// which locates where a not-yet-created file would land.
func resolveExistingPrefix(p string) (string, error) {
	prefix := p
	for {
		resolved, err := filepath.EvalSymlinks(prefix)
		if err == nil {
			rel, relErr := filepath.Rel(prefix, p)
			if relErr != nil {
				return "", relErr
			}
			return filepath.Join(resolved, rel), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return "", err
		}
		prefix = parent
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

func (r *Runner) resolveElement(client OrcaClient, pageID, target string) (string, error) {
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
		snap, err := client.Snapshot(pageID)
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

func (r *Runner) writeReports(spec *Spec, report *TestReport) error {
	if r.evidenceDir == "" {
		if spec.RequirePDF {
			report.Success = false
			return errors.New("required PDF needs an evidence directory")
		}
		return nil
	}

	// Validate destinations before generating or publishing successful evidence.
	for _, name := range []string{"report.json", "report.md"} {
		path, err := r.evidencePath(name)
		if err == nil {
			if info, statErr := os.Stat(path); statErr == nil && !info.Mode().IsRegular() {
				err = fmt.Errorf("%s is not a regular file", name)
			} else if statErr != nil && !os.IsNotExist(statErr) {
				err = statErr
			}
		}
		if err != nil {
			return r.reportFailure(report, err)
		}
	}
	if pdfPath, err := BuildBoardPDF(spec, report, r.evidenceDir); err == nil && pdfPath != "" {
		report.PDFPath = pdfPath
	} else {
		if err == nil {
			err = errors.New("renderer returned no PDF")
		}
		if spec.RequirePDF {
			return r.reportFailure(report, fmt.Errorf("required PDF: %w", err))
		}
		report.EvidenceWarnings = append(report.EvidenceWarnings, "optional PDF: "+err.Error())
	}

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
	for _, warning := range report.EvidenceWarnings {
		sb.WriteString("- **Evidence warning**: " + warning + "\n")
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
	if err := r.writeReportFile("report.md", []byte(sb.String())); err != nil {
		return r.reportFailure(report, err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		err = r.writeReportFile("report.json", data)
	}
	if err != nil {
		return r.reportFailure(report, err)
	}
	return nil
}

func (r *Runner) writeReportFile(name string, data []byte) error {
	path, err := r.evidencePath(name)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.evidenceDir, ".report-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}

func (r *Runner) reportFailure(report *TestReport, cause error) error {
	report.Success = false
	report.FailureMessage = "persist required evidence: " + cause.Error()
	// Remove stale successful artifacts from this output directory. Never remove
	// directories or follow symlinks. Best-effort failure JSON remains useful.
	for _, name := range []string{"report.json", "report.md", "report.pdf"} {
		path := filepath.Join(r.evidenceDir, name)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			_ = os.Remove(path)
		}
	}
	report.PDFPath = ""
	if b, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = r.writeReportFile("report.json", b)
	}
	return errors.New(report.FailureMessage)
}
