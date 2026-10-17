package hunter

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestScanReportRequiresBoundedCoverageAndNeverClaimsBugFree(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	sha := strings.Repeat("a", 40)
	report, err := ScanReport("task", sha, []string{"diff src/a.go:1-10"}, []string{"network integration: unavailable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NO_CONFIRMED_FINDINGS", sha, "diff src/a.go", "network integration", "REPORT_ONLY"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %s: %s", want, report)
		}
	}
	if strings.Contains(report, "BUG_FREE") {
		t.Fatal("unsupported assurance")
	}
	for _, tc := range []struct {
		sha              string
		covered, skipped []string
	}{{"short", []string{"diff"}, nil}, {sha, nil, []string{"all"}}, {sha, []string{"BUG_FREE"}, nil}} {
		if _, err := ScanReport("task", tc.sha, tc.covered, tc.skipped, nil); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
}

func TestScanReportCannotOmitKnownCandidate(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	f := testFinding("task", "source.go", 1)
	f.Baseline = strings.Repeat("a", 40)
	id, _, err := Report(f)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ScanReport("task", f.Baseline, []string{"diff source.go"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(report, "FINDINGS_RECORDED") || !strings.Contains(report, id) {
		t.Fatal(report)
	}
}
