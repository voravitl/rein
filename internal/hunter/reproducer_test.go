package hunter

// Test-only reproducer contract (Phase D slice D2, test-only).
//
// The hunter draft requires two permission modes with different contracts:
//
//	SCAN:      application source read-only; writes allowed only to
//	          evidence/scratch paths the contract names.
//	REPRODUCE: a SEPARATE test-only contract in an isolated worktree;
//	          the reproducer may write test files but must never change
//	          application code to make a bug "appear".
//
// And the report rule: when nothing reproduces, the hunter reports
// NO_CONFIRMED_FINDINGS with the scope it did and did not cover — never
// BUG_FREE (a clean fixture proves nothing about the rest of the repo).
//
// This test pins, with real contracts judged by the real guard decision
// function:
//  1. a SCAN contract cannot write application source, but can write its
//     evidence file;
//  2. a REPRODUCE contract cannot write application source either, but can
//     write its test files;
//  3. a worker contract that ALLOWS application source is refused as a
//     reproducer contract (no passing a normal worker off as a reproducer);
//  4. NO_CONFIRMED_FINDINGS carries scope-covered and scope-skipped, and the
//     words BUG_FREE / NO_BUGS / CLEAN never appear in it.
//
// Test-only. No production path changes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/guard"
)

// reproEnv builds an isolated worktree with application source, a test dir
// and an evidence dir, plus SCAN and REPRODUCE contracts over it.
type reproEnv struct {
	wt      string
	appFile string // application source: never writable by the hunter
	testDir string // test scope: writable only under REPRODUCE
	evDir   string // evidence scope: writable under both
}

func newReproEnv(t *testing.T) *reproEnv {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	wt := filepath.Join(d, "wt")
	app := filepath.Join(wt, "src", "auth.go")
	testDir := filepath.Join(wt, "hunter-tests")
	evDir := filepath.Join(wt, "evidence")
	for _, dir := range []string{filepath.Join(wt, "src"), testDir, evDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(app, []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &reproEnv{wt: wt, appFile: app, testDir: testDir, evDir: evDir}
}

// scanContract allows ONLY the evidence dir. Everything else — including the
// test dir — is out of scope for a scan.
func (e *reproEnv) scanContract(t *testing.T) *contract.Contract {
	t.Helper()
	return &contract.Contract{
		Name: "hunt-scan", Worktree: e.wt,
		Allow: []string{"evidence/**"},
		Deny:  append([]string{}, contract.AlwaysDeny...),
		Scope: []string{"S-scan"}, ReportPath: filepath.Join(e.wt, "evidence", "scan.md"),
	}
}

// reproContract allows the test dir and the evidence dir, never application
// source. It is a separate contract, not the worker's.
func (e *reproEnv) reproContract(t *testing.T) *contract.Contract {
	t.Helper()
	return &contract.Contract{
		Name: "hunt-repro", Worktree: e.wt,
		Allow: []string{"hunter-tests/**", "evidence/**"},
		Deny:  append([]string{}, contract.AlwaysDeny...),
		Scope: []string{"S-repro"}, ReportPath: filepath.Join(e.wt, "evidence", "repro.md"),
	}
}

// workerContract is what an implementation worker would hold: application
// source is writable. It must never pass as a reproducer contract.
func (e *reproEnv) workerContract(t *testing.T) *contract.Contract {
	t.Helper()
	return &contract.Contract{
		Name: "worker", Worktree: e.wt,
		Allow: []string{"src/**", "hunter-tests/**", "evidence/**"},
		Deny:  append([]string{}, contract.AlwaysDeny...),
		Scope: []string{"S-impl"}, ReportPath: filepath.Join(e.wt, "evidence", "worker.md"),
	}
}

// IsReproducerContract reports whether c may serve as a test-only reproducer:
// it must allow a test scope and must NOT allow application source. The check
// is structural (contract globs), not a promise about behavior.
func IsReproducerContract(c *contract.Contract, appGlobs, testGlobs []string) (bool, string) {
	for _, a := range c.Allow {
		for _, g := range appGlobs {
			if a == g {
				return false, "reproducer contract allows application source " + a
			}
		}
	}
	for _, want := range testGlobs {
		found := false
		for _, a := range c.Allow {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			return false, "reproducer contract lacks test scope " + want
		}
	}
	return true, ""
}

func TestReproducerContractSeparation(t *testing.T) {
	e := newReproEnv(t)
	app := []string{"src/**"}
	testScope := []string{"hunter-tests/**"}

	// 1. SCAN: application source is outside the contract, evidence is inside —
	// judged by the real guard, the same check a worker's Bash would take.
	scan := e.scanContract(t)
	if why := guard.CheckBash("echo x > src/auth.go", scan, e.wt, e.wt); why == "" {
		t.Error("SCAN contract allows writing application source")
	}
	if why := guard.CheckBash("echo x > evidence/scan.md", scan, e.wt, e.wt); why != "" {
		t.Errorf("SCAN contract cannot write its evidence file: %s", why)
	}
	if why := guard.CheckBash("echo x > hunter-tests/repro_test.go", scan, e.wt, e.wt); why == "" {
		t.Error("SCAN contract allows writing test files (test scope is REPRODUCE-only)")
	}
	if ok, _ := IsReproducerContract(scan, app, testScope); ok {
		t.Error("SCAN contract passes as a reproducer contract")
	}

	// 2. REPRODUCE: test files and evidence are writable, application source
	// is not — on the real guard decision function, not just Writable.
	repro := e.reproContract(t)
	if why := guard.CheckBash("echo x > src/auth.go", repro, e.wt, e.wt); why == "" {
		t.Error("REPRODUCE contract allows writing application source")
	}
	if why := guard.CheckBash("echo x > hunter-tests/repro_test.go", repro, e.wt, e.wt); why != "" {
		t.Errorf("REPRODUCE contract cannot write its test files: %s", why)
	}
	if why := guard.CheckBash("echo x > evidence/repro.md", repro, e.wt, e.wt); why != "" {
		t.Errorf("REPRODUCE contract cannot write its evidence file: %s", why)
	}
	if ok, why := IsReproducerContract(repro, app, testScope); !ok {
		t.Errorf("REPRODUCE contract rejected: %s", why)
	}

	// 3. A worker contract allows application source, so it is refused AS a
	// reproducer contract — no passing a normal worker off as a reproducer.
	worker := e.workerContract(t)
	if why := guard.CheckBash("echo x > src/auth.go", worker, e.wt, e.wt); why != "" {
		t.Fatalf("fixture worker contract should allow application source: %s", why)
	}
	if ok, why := IsReproducerContract(worker, app, testScope); ok {
		t.Error("worker contract passes as a reproducer contract")
	} else if !strings.Contains(why, "application source") {
		t.Errorf("refusal reason does not name the problem: %s", why)
	}

	// 4. NO_CONFIRMED_FINDINGS carries both scopes and never claims BUG_FREE.
	report := NoConfirmedFindings("t", "abc123",
		[]string{"src/auth.go (SCAN, read-only)", "hunter-tests/repro_test.go (3 attempts, all not-reproduced)"},
		[]string{"e2e scope (no URL/account authorization — blocked, not skipped silently)"})
	for _, want := range []string{"NO_CONFIRMED_FINDINGS", "scope covered", "scope not covered", "abc123"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	for _, forbidden := range []string{"BUG_FREE", "NO_BUGS", "CLEAN", "no defects"} {
		if strings.Contains(strings.ToUpper(report), forbidden) {
			t.Errorf("report claims %q:\n%s", forbidden, report)
		}
	}
}

// NoConfirmedFindings renders the report a hunter files when nothing
// reproduced: the baseline, what was covered, what was not (and why), and an
// explicit refusal to claim the code is bug-free.
func NoConfirmedFindings(task, baseline string, covered, skipped []string) string {
	var b strings.Builder
	b.WriteString("NO_CONFIRMED_FINDINGS\n")
	b.WriteString("task: " + task + "\n")
	b.WriteString("baseline: " + baseline + "\n")
	b.WriteString("scope covered:\n")
	for _, s := range covered {
		b.WriteString("  - " + s + "\n")
	}
	b.WriteString("scope not covered:\n")
	for _, s := range skipped {
		b.WriteString("  - " + s + "\n")
	}
	b.WriteString("This report does not claim the code is bug-free. Uncovered scope stays uncovered.\n")
	return b.String()
}
