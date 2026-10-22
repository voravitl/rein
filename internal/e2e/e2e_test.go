package e2e

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type mockOrcaClient struct {
	statusErr      error
	tabCreatePage  string
	tabCreateErr   error
	closedPages    []string
	visitedURLs    []string
	clicks         []string
	fills          map[string]string
	evalResponses  map[string]string
	snapshotResult *SnapshotResult
	screenshotRaw  []byte
	ctx            context.Context
	deadlineTrace  *deadlineTrace
}

// deadlineTrace records whether context-bound client calls carried a deadline.
type deadlineTrace struct {
	hasDeadline      bool
	snapshotDeadline time.Time
}

// WithContext records the scenario context; the mock ignores it beyond tracing.
func (m *mockOrcaClient) WithContext(ctx context.Context) OrcaClient {
	m.ctx = ctx
	return m
}

func newMockClient() *mockOrcaClient {
	return &mockOrcaClient{
		tabCreatePage: "mock-page-123",
		fills:         make(map[string]string),
		evalResponses: make(map[string]string),
		screenshotRaw: []byte{0x89, 'P', 'N', 'G'},
		snapshotResult: &SnapshotResult{
			Origin: "http://localhost:8080",
			Refs: map[string]ElementRef{
				"e1": {Name: "Username", Role: "textbox"},
				"e2": {Name: "Submit Button", Role: "button"},
			},
			Snapshot: "- textbox \"Username\" [ref=e1]\n- button \"Submit Button\" [ref=e2]\n- StaticText \"Welcome to Dashboard\"",
		},
	}
}

func (m *mockOrcaClient) CheckStatus() error { return m.statusErr }
func (m *mockOrcaClient) TabCreate(url string) (string, error) {
	if m.tabCreateErr != nil {
		return "", m.tabCreateErr
	}
	m.visitedURLs = append(m.visitedURLs, url)
	return m.tabCreatePage, nil
}
func (m *mockOrcaClient) TabClose(pageID string) error {
	m.closedPages = append(m.closedPages, pageID)
	return nil
}
func (m *mockOrcaClient) Goto(pageID, url string) error {
	m.visitedURLs = append(m.visitedURLs, url)
	return nil
}
func (m *mockOrcaClient) Snapshot(pageID string) (*SnapshotResult, error) {
	if m.ctx != nil && m.deadlineTrace != nil {
		if d, ok := m.ctx.Deadline(); ok {
			m.deadlineTrace.hasDeadline = true
			m.deadlineTrace.snapshotDeadline = d
		}
	}
	return m.snapshotResult, nil
}
func (m *mockOrcaClient) Click(pageID, element string) error {
	m.clicks = append(m.clicks, element)
	return nil
}
func (m *mockOrcaClient) Fill(pageID, element, value string) error {
	m.fills[element] = value
	return nil
}
func (m *mockOrcaClient) Keypress(pageID, key string) error { return nil }
func (m *mockOrcaClient) Eval(pageID, expression string) (string, error) {
	if val, ok := m.evalResponses[expression]; ok {
		return val, nil
	}
	return "mock-eval-result", nil
}
func (m *mockOrcaClient) Screenshot(pageID string) ([]byte, error) {
	return m.screenshotRaw, nil
}

func TestLoadSpec(t *testing.T) {
	tmp := t.TempDir()

	// Plain JSON spec
	p1 := filepath.Join(tmp, "spec.json")
	if err := os.WriteFile(p1, []byte(`{
		"name": "login-test",
		"base_url": "http://localhost:3000",
		"steps": [
			{"action": "goto", "url": "/login"}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s1, err := LoadSpec(p1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s1.Name != "login-test" || len(s1.Steps) != 1 {
		t.Fatalf("unexpected spec: %+v", s1)
	}

	// Markdown embedded JSON spec
	p2 := filepath.Join(tmp, "spec.md")
	if err := os.WriteFile(p2, []byte(`# Test Plan
Some notes...

`+"```json"+`
{
	"name": "embedded-spec",
	"steps": [
		{"action": "snapshot"}
	]
}
`+"```"+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	s2, err := LoadSpec(p2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s2.Name != "embedded-spec" || len(s2.Steps) != 1 {
		t.Fatalf("unexpected embedded spec: %+v", s2)
	}

	// Empty steps error
	p3 := filepath.Join(tmp, "empty.json")
	_ = os.WriteFile(p3, []byte(`{"name": "empty", "steps": []}`), 0o644)
	if _, err := LoadSpec(p3); err == nil {
		t.Fatalf("expected error for empty steps, got nil")
	}
}

func TestRunner_FullSuccessScenario(t *testing.T) {
	tmp := t.TempDir()
	evidenceDir := filepath.Join(tmp, "evidence")

	mock := newMockClient()
	mock.evalResponses["document.title"] = "Dashboard - My App"
	mock.evalResponses["window.location.pathname"] = "/dashboard"

	runner := NewRunner(mock, evidenceDir)
	spec := &Spec{
		Name:    "e2e-success",
		BaseURL: "http://localhost:8080",
		Steps: []Step{
			{Action: "goto", URL: "/dashboard", Description: "Navigate to dashboard"},
			{Action: "assert_title", Value: "Dashboard", Description: "Verify title"},
			{Action: "assert_text", Value: "Welcome to Dashboard", Description: "Verify welcome banner"},
			{Action: "fill", Target: "Username", Value: "admin", Description: "Fill username"},
			{Action: "click", Target: "Submit Button", Description: "Click submit"},
			{Action: "eval", Expression: "window.location.pathname", AssertValue: "/dashboard"},
			{Action: "screenshot", Filename: "dash.png", Description: "Capture proof"},
			{Action: "wait", Value: "Welcome", TimeoutMS: 1000},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}

	if !report.Success {
		t.Fatalf("expected test success, got failure: %s", report.FailureMessage)
	}
	if report.PassedSteps != len(spec.Steps) {
		t.Fatalf("expected %d passed steps, got %d", len(spec.Steps), report.PassedSteps)
	}

	// Verify tab closed
	if len(mock.closedPages) != 1 || mock.closedPages[0] != "mock-page-123" {
		t.Fatalf("expected tab closed, got %v", mock.closedPages)
	}

	// Verify fills & clicks
	if mock.fills["e1"] != "admin" {
		t.Errorf("expected fill e1 to be admin, got %v", mock.fills)
	}
	if len(mock.clicks) != 1 || mock.clicks[0] != "e2" {
		t.Errorf("expected click e2, got %v", mock.clicks)
	}

	// Verify evidence created
	if _, err := os.Stat(filepath.Join(evidenceDir, "dash.png")); err != nil {
		t.Errorf("screenshot dash.png missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, "report.json")); err != nil {
		t.Errorf("report.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, "report.md")); err != nil {
		t.Errorf("report.md missing: %v", err)
	}
}

func TestRunner_AssertionFailureAndDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	evidenceDir := filepath.Join(tmp, "evidence-fail")

	mock := newMockClient()
	runner := NewRunner(mock, evidenceDir)

	spec := &Spec{
		Name: "e2e-fail",
		Steps: []Step{
			{Action: "goto", URL: "http://localhost:8080/test"},
			{Action: "assert_text", Value: "NonExistentString12345"},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}

	if report.Success {
		t.Fatalf("expected test to fail on missing text")
	}
	if report.FailedSteps != 1 {
		t.Fatalf("expected 1 failed step, got %d", report.FailedSteps)
	}

	// Verify failure diagnostics written
	if _, err := os.Stat(filepath.Join(evidenceDir, "failure-step-02.png")); err != nil {
		t.Errorf("expected failure screenshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, "failure-step-02.txt")); err != nil {
		t.Errorf("expected failure snapshot: %v", err)
	}

	// Verify tab still cleanly closed
	if len(mock.closedPages) != 1 {
		t.Fatalf("expected tab closed on failure, got %v", mock.closedPages)
	}
}

func TestRunner_ElementRefResolution(t *testing.T) {
	mock := newMockClient()
	runner := NewRunner(mock, "")
	runner.cachedSnapshot = mock.snapshotResult

	// Direct ref @e1
	r1, err := runner.resolveElement(mock, "p1", "@e1")
	if err != nil || r1 != "e1" {
		t.Fatalf("expected e1, got %s (err: %v)", r1, err)
	}

	// Plain ref e2
	r2, err := runner.resolveElement(mock, "p1", "e2")
	if err != nil || r2 != "e2" {
		t.Fatalf("expected e2, got %s (err: %v)", r2, err)
	}

	// Name match "Username" -> e1
	r3, err := runner.resolveElement(mock, "p1", "Username")
	if err != nil || r3 != "e1" {
		t.Fatalf("expected e1, got %s (err: %v)", r3, err)
	}

	// Substring match "Submit" -> e2
	r4, err := runner.resolveElement(mock, "p1", "submit")
	if err != nil || r4 != "e2" {
		t.Fatalf("expected e2, got %s (err: %v)", r4, err)
	}

	// Not found
	if _, err := runner.resolveElement(mock, "p1", "MissingButton"); err == nil {
		t.Fatalf("expected error for non-existent button, got nil")
	}
}

func TestRunner_ScreenshotRejectsTraversalFilename(t *testing.T) {
	base := t.TempDir()
	evidenceDir := filepath.Join(base, "evidence")

	runner := NewRunner(newMockClient(), evidenceDir)
	spec := &Spec{
		Name: "escape-traversal",
		Steps: []Step{
			{Action: "screenshot", Filename: "../outside.png"},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(base, "outside.png")); statErr == nil {
		t.Errorf("screenshot escaped the evidence directory: %s was created", filepath.Join(base, "outside.png"))
	}
	if report.Success {
		t.Errorf("expected traversal filename to fail the step")
	}
	if last := report.Steps[len(report.Steps)-1]; !strings.Contains(last.Error, "escapes evidence directory") {
		t.Errorf("expected rejection error, got %q", last.Error)
	}
}

func TestRunner_ScreenshotRejectsSymlinkedParent(t *testing.T) {
	base := t.TempDir()
	evidenceDir := filepath.Join(base, "evidence")
	outsideDir := filepath.Join(base, "outside")

	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(evidenceDir, "sub")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	runner := NewRunner(newMockClient(), evidenceDir)
	spec := &Spec{
		Name: "escape-symlink",
		Steps: []Step{
			{Action: "screenshot", Filename: "sub/escape.png"},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(outsideDir, "escape.png")); statErr == nil {
		t.Errorf("screenshot escaped through symlinked parent: %s was created", filepath.Join(outsideDir, "escape.png"))
	}
	if report.Success {
		t.Errorf("expected symlinked parent to fail the step")
	}
}

func TestRunner_ScreenshotRejectsSymlinkedDestination(t *testing.T) {
	base := t.TempDir()
	evidenceDir := filepath.Join(base, "evidence")
	outsideDir := filepath.Join(base, "outside")

	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Dangling symlink: the target does not exist yet, so writes would create it.
	if err := os.Symlink(filepath.Join(outsideDir, "escape.png"), filepath.Join(evidenceDir, "link.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	runner := NewRunner(newMockClient(), evidenceDir)
	spec := &Spec{
		Name: "escape-dest-symlink",
		Steps: []Step{
			{Action: "screenshot", Filename: "link.png"},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(outsideDir, "escape.png")); statErr == nil {
		t.Errorf("screenshot escaped through symlinked destination: %s was created", filepath.Join(outsideDir, "escape.png"))
	}
	if report.Success {
		t.Errorf("expected symlinked destination to fail the step")
	}
}

func TestRunner_ScreenshotWritesNestedFilename(t *testing.T) {
	base := t.TempDir()
	evidenceDir := filepath.Join(base, "evidence")

	runner := NewRunner(newMockClient(), evidenceDir)
	spec := &Spec{
		Name: "nested",
		Steps: []Step{
			{Action: "screenshot", Filename: "shots/ok.png"},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}
	if !report.Success {
		t.Fatalf("expected nested screenshot to succeed: %s", report.FailureMessage)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, "shots", "ok.png")); err != nil {
		t.Errorf("nested screenshot missing: %v", err)
	}
}

func TestRunner_ScenarioTimeoutFailsLateWait(t *testing.T) {
	cases := []struct {
		name string
		step Step
	}{
		{name: "sleep wait", step: Step{Action: "wait", TimeoutMS: 5000}},
		{name: "text wait", step: Step{Action: "wait", Value: "NEVER-APPEARS-42", TimeoutMS: 5000}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := &deadlineTrace{}
			mock := newMockClient()
			mock.deadlineTrace = trace
			runner := NewRunner(mock, filepath.Join(t.TempDir(), "evidence"))

			spec := &Spec{
				Name:           "scenario-timeout",
				TimeoutSeconds: 1,
				Steps:          []Step{tc.step},
			}

			start := time.Now()
			report, err := runner.Run(spec)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("unexpected runner error: %v", err)
			}
			if report.Success {
				t.Fatalf("expected scenario timeout to fail the run (elapsed %v)", elapsed)
			}
			last := report.Steps[len(report.Steps)-1]
			stepDuration := time.Duration(last.DurationMS) * time.Millisecond
			if stepDuration >= 4*time.Second {
				t.Errorf("scenario deadline not enforced: step took %v", stepDuration)
			}
			if !strings.Contains(last.Error, "timed out") {
				t.Errorf("expected timeout error, got %q", last.Error)
			}
			if !trace.hasDeadline {
				t.Errorf("client calls did not observe the scenario deadline")
			} else if d := trace.snapshotDeadline; d.Before(start.Add(900*time.Millisecond)) || d.After(start.Add(1100*time.Millisecond)) {
				t.Errorf("scenario deadline not derived from spec timeout: %v", d)
			}
		})
	}
}

func TestRunner_ScenarioTimeoutAllowsFastRun(t *testing.T) {
	base := t.TempDir()
	runner := NewRunner(newMockClient(), filepath.Join(base, "evidence"))

	spec := &Spec{
		Name:           "fast-run",
		TimeoutSeconds: 5,
		Steps: []Step{
			{Action: "snapshot"},
			{Action: "wait", TimeoutMS: 50},
			{Action: "screenshot", Filename: "fast.png"},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}
	if !report.Success {
		t.Fatalf("expected fast run to pass: %s", report.FailureMessage)
	}
	if _, err := os.Stat(filepath.Join(base, "evidence", "fast.png")); err != nil {
		t.Errorf("screenshot missing: %v", err)
	}
}

func TestRunner_NoScenarioTimeoutKeepsClientDefaults(t *testing.T) {
	trace := &deadlineTrace{}
	mock := newMockClient()
	mock.deadlineTrace = trace
	runner := NewRunner(mock, filepath.Join(t.TempDir(), "evidence"))

	// TimeoutSeconds unset (spec built directly, as before LoadSpec defaults).
	spec := &Spec{
		Name: "no-scenario-timeout",
		Steps: []Step{
			{Action: "snapshot"},
			{Action: "wait", Value: "Welcome", TimeoutMS: 1000},
		},
	}

	report, err := runner.Run(spec)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}
	if !report.Success {
		t.Fatalf("expected run without scenario timeout to pass: %s", report.FailureMessage)
	}
	if trace.hasDeadline {
		t.Errorf("client calls unexpectedly received a scenario deadline")
	}
}

func TestRealOrcaClient_ScenarioDeadlineAbortsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell fixture")
	}

	bin := filepath.Join(t.TempDir(), "orca")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client := (&RealOrcaClient{BinaryPath: bin, Timeout: 30 * time.Second}).WithContext(ctx)

	start := time.Now()
	if _, err := client.Snapshot("p1"); err == nil {
		t.Fatal("expected scenario deadline to abort the orca command")
	} else if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("orca command was not bounded by the scenario deadline: %v", elapsed)
	} else if !strings.Contains(err.Error(), "scenario deadline") {
		t.Errorf("expected scenario deadline error, got %v", err)
	}
}
