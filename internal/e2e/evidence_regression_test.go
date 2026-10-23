package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type changingPage struct {
	*mockOrcaClient
	text         string
	failSnapshot bool
}

func (m *changingPage) WithContext(context.Context) OrcaClient { return m }
func (m *changingPage) Snapshot(string) (*SnapshotResult, error) {
	if m.failSnapshot {
		return nil, errors.New("snapshot unavailable")
	}
	return &SnapshotResult{Snapshot: m.text, Refs: map[string]ElementRef{"e1": {Name: "Field"}}}, nil
}
func (m *changingPage) Fill(string, string, string) error   { m.text = "new"; return nil }
func (m *changingPage) Keypress(string, string) error       { m.text = "new"; return nil }
func (m *changingPage) Eval(string, string) (string, error) { m.text = "new"; return "", nil }

func TestAssertionsRefreshAfterPageMutation(t *testing.T) {
	for _, action := range []string{"fill", "keypress", "eval"} {
		for _, want := range []string{"new", "old"} {
			t.Run(action+"/"+want, func(t *testing.T) {
				m := &changingPage{mockOrcaClient: newMockClient(), text: "old"}
				r := NewRunner(m, "")
				report, err := r.Run(&Spec{Steps: []Step{{Action: "assert_text", Value: "old"}, {Action: action, Target: "Field", Key: "Enter"}, {Action: "assert_text", Value: want}}})
				if err != nil {
					t.Fatal(err)
				}
				if report.Success != (want == "new") {
					t.Fatalf("stale assertion: %+v", report)
				}
			})
		}
	}
}

func TestAssertionCannotUseCacheAfterSnapshotFailure(t *testing.T) {
	m := &changingPage{mockOrcaClient: newMockClient(), failSnapshot: true}
	r := NewRunner(m, "")
	r.cachedSnapshot = &SnapshotResult{Snapshot: "old"}
	if err := r.executeStep(context.Background(), m, "page", "", Step{Action: "assert_text", Value: "old"}, 1); err == nil {
		t.Fatal("failed snapshot fell back to stale text")
	}
}

func TestRequiredEvidenceWriteFailureFailsRun(t *testing.T) {
	for _, name := range []string{"report.json", "report.md"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// A directory at the output path fails even when tests run as root.
			if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
				t.Fatal(err)
			}
			r := NewRunner(newMockClient(), dir)
			report, err := r.Run(&Spec{Name: "evidence", Steps: []Step{{Action: "assert_title", Value: "mock"}}})
			if err == nil || report == nil || report.Success || report.PassedSteps != 1 {
				t.Fatalf("evidence failure reported as success: %+v err=%v", report, err)
			}
		})
	}
}

func TestRequiredPDFCannotPassWithoutEvidenceDirectory(t *testing.T) {
	report, err := NewRunner(newMockClient(), "").Run(&Spec{RequirePDF: true, Steps: []Step{{Action: "assert_title", Value: "mock"}}})
	if err == nil || report.Success {
		t.Fatalf("required PDF silently omitted: %+v %v", report, err)
	}
}
