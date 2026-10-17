package hunter

import (
	"errors"
	"fmt"
	"strings"
)

// ScanReport summarizes the caller's bounded investigation without executing a
// worker or authorizing fixes. Coverage is declared, not runtime-tested proof.
func ScanReport(task, baseline string, covered, skipped, findingIDs []string) (string, error) {
	if strings.TrimSpace(task) == "" || !fullRevision.MatchString(baseline) || len(covered) == 0 {
		return "", errors.New("scan report requires task, exact baseline SHA and explicit covered scope")
	}
	for _, s := range append(append([]string{task}, covered...), skipped...) {
		upper := strings.ToUpper(s)
		if strings.TrimSpace(s) == "" || strings.ContainsAny(s, "\r\n\x00") || strings.Contains(upper, "BUG_FREE") || strings.Contains(upper, "NO_BUGS") {
			return "", errors.New("scan scope must be a nonempty single line without unsupported assurance")
		}
	}
	// Include all matching registry records so omitted IDs cannot turn known
	// candidates into a no-findings claim.
	all, err := List()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, id := range findingIDs {
		seen[id] = true
	}
	for _, finding := range all {
		if finding.Task == task && finding.Baseline == baseline && !seen[finding.ID] {
			findingIDs = append(findingIDs, finding.ID)
			seen[finding.ID] = true
		}
	}
	var b strings.Builder
	status := "NO_CONFIRMED_FINDINGS"
	for _, id := range findingIDs {
		finding, err := Get(id)
		if err != nil {
			return "", err
		}
		if finding.Task != task || finding.Baseline != baseline {
			return "", fmt.Errorf("finding %s does not match task and baseline", id)
		}
		if finding.State != Rejected && finding.State != Duplicate && finding.State != OutOfScope && finding.State != VerifiedFixed {
			status = "FINDINGS_RECORDED"
		}
	}
	fmt.Fprintf(&b, "%s\nmode: REPORT_ONLY\ntask: %s\nbaseline: %s\nscope covered (declared):\n", status, task, baseline)
	for _, s := range covered {
		fmt.Fprintf(&b, "  - %s\n", s)
	}
	b.WriteString("scope not covered:\n")
	if len(skipped) == 0 {
		b.WriteString("  - Any scope outside the declared investigation was not assessed.\n")
	}
	for _, s := range skipped {
		fmt.Fprintf(&b, "  - %s\n", s)
	}
	if len(findingIDs) > 0 {
		b.WriteString("finding records:\n")
		for _, id := range findingIDs {
			fmt.Fprintf(&b, "  - %s\n", id)
		}
	}
	b.WriteString("This bounded report makes no claim that the code is bug-free and authorizes no fix.\n")
	return b.String(), nil
}
