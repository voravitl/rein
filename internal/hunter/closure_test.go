package hunter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func closureFixture(t *testing.T) (string, string, Verification, string) {
	t.Helper()
	repo, id, _, fixed := verifyFixture(t)
	for _, state := range []State{Reproduced, Triaged, ApprovedForFX, FixInProgress} {
		if err := Move(id, state, "fixture owner-approved fix task"); err != nil {
			t.Fatal(err)
		}
	}
	v, err := VerifyRuntime(context.Background(), id, repo, "verified", fixed, "red", []string{"check_test.go"}, []string{os.Args[0], "-test.run=^TestRuntimeVerifyHelper$", "verify-helper", "suite"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "review.md")
	if err = os.WriteFile(report, []byte("Independent review of fixed revision: "+fixed+"\nAPPROVE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return repo, id, v, report
}

func TestReviewAndOwnerApprovalCloseOnlyExactRuntimeProof(t *testing.T) {
	repo, id, v, report := closureFixture(t)
	if _, err := ApproveFix(context.Background(), id, repo, v.EvidenceHash, "missing", "owner", "accept fixture"); err == nil {
		t.Fatal("approved without independent review")
	}
	r, err := ReviewFix(context.Background(), id, repo, v.Attempt, v.EvidenceHash, "worker", "reviewer", report)
	if err != nil || r.Status != "WAITING_OWNER_APPROVAL" {
		t.Fatalf("%+v %v", r, err)
	}
	f, _ := Get(id)
	if f.State != FixInProgress {
		t.Fatal("review self-approved closure")
	}
	f, err = ApproveFix(context.Background(), id, repo, v.EvidenceHash, r.Digest, "owner", "accept fixture")
	if err != nil || f.State != VerifiedFixed {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err = ApproveFix(context.Background(), id, repo, v.EvidenceHash, r.Digest, "owner", "again"); err == nil {
		t.Fatal("closed finding approved again")
	}
}

func TestClosureRefusesSelfReviewStaleTamperAndBypass(t *testing.T) {
	for _, mode := range []string{"same-reviewer", "verification-tamper", "red-tamper", "report-tamper", "stale", "head-moved", "dirty", "wrong-hash", "wrong-state", "empty-owner"} {
		t.Run(mode, func(t *testing.T) {
			repo, id, v, report := closureFixture(t)
			if mode == "same-reviewer" {
				if _, err := ReviewFix(context.Background(), id, repo, v.Attempt, v.EvidenceHash, "same", "same", report); err == nil {
					t.Fatal("self review accepted")
				}
				return
			}
			if mode == "wrong-state" {
				if err := Move(id, Blocked, "fixture pause"); err != nil {
					t.Fatal(err)
				}
				if _, err := ReviewFix(context.Background(), id, repo, v.Attempt, v.EvidenceHash, "worker", "reviewer", report); err == nil {
					t.Fatal("blocked lifecycle bypass")
				}
				return
			}
			r, err := ReviewFix(context.Background(), id, repo, v.Attempt, v.EvidenceHash, "worker", "reviewer", report)
			if err != nil {
				t.Fatal(err)
			}
			owner := "owner"
			switch mode {
			case "verification-tamper":
				os.WriteFile(v.EvidencePath, []byte("{}\n"), 0600)
			case "red-tamper":
				f, _ := Get(id)
				os.WriteFile(f.Attempts[0].EvidencePath, []byte("{}\n"), 0600)
			case "report-tamper":
				os.WriteFile(report, []byte("different review"), 0600)
			case "head-moved":
				c := exec.Command("git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "advance")
				c.Dir = repo
				c.Env = reproEnvironment()
				if b, e := c.CombinedOutput(); e != nil {
					t.Fatalf("%s %v", b, e)
				}
			case "dirty":
				if e := os.WriteFile(filepath.Join(repo, "value"), []byte("dirty"), 0600); e != nil {
					t.Fatal(e)
				}
			case "stale":
				v.EvidenceHash = strings.Repeat("0", 64)
			case "wrong-hash":
				r.Digest = strings.Repeat("0", 64)
			case "empty-owner":
				owner = ""
			}
			if _, err := ApproveFix(context.Background(), id, repo, v.EvidenceHash, r.Digest, owner, "accept"); err == nil {
				t.Fatal("invalid approval accepted")
			}
			f, _ := Get(id)
			if f.State != FixInProgress {
				t.Fatal("invalid approval changed lifecycle")
			}
		})
	}
}

func TestReportCannotInjectClosureHistory(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	for _, kind := range []string{"review", "owner"} {
		f := testFinding("t", "file.go", 1)
		if kind == "review" {
			f.FixReview = &FixReview{Status: "WAITING_OWNER_APPROVAL"}
		} else {
			f.OwnerAcceptance = &OwnerAcceptance{Owner: "claimed"}
		}
		if _, _, err := Report(f); err == nil {
			t.Fatal("reported caller-owned closure history")
		}
	}
}
