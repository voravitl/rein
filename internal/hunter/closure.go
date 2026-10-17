package hunter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// Identities are controller assertions under Rein's same-user trust model,
// not authentication; owner acceptance is a separate human-terminal CLI gate.
type FixReview struct {
	Finding             string `json:"finding"`
	Fixed               string `json:"fixed"`
	VerificationAttempt string `json:"verification_attempt"`
	VerificationDigest  string `json:"verification_digest"`
	Worker              string `json:"worker"`
	Reviewer            string `json:"reviewer"`
	ReportPath          string `json:"report_path"`
	ReportDigest        string `json:"report_digest"`
	Status              string `json:"status"`
	At                  string `json:"at"`
	Digest              string `json:"digest"`
}

type OwnerAcceptance struct {
	Owner              string `json:"owner"`
	Reason             string `json:"reason"`
	ReviewDigest       string `json:"review_digest"`
	VerificationDigest string `json:"verification_digest"`
	Fixed              string `json:"fixed"`
	At                 string `json:"at"`
}

func digestBytes(b []byte) string     { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func reviewDigest(r FixReview) string { r.Digest = ""; b, _ := json.Marshal(r); return digestBytes(b) }

// ReviewFix records an independent recommendation for one exact runtime proof.
// A recommendation does not change the finding's lifecycle or approve it.
func ReviewFix(ctx context.Context, id, repo, attempt, digest, worker, reviewer, report string) (FixReview, error) {
	var out FixReview
	if strings.TrimSpace(worker) == "" || strings.TrimSpace(reviewer) == "" || strings.EqualFold(strings.TrimSpace(worker), strings.TrimSpace(reviewer)) {
		return out, errors.New("independent reviewer must differ from fix worker")
	}
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return out, err
	}
	defer unlock()
	all, at, err := closureFinding(id)
	if err != nil {
		return out, err
	}
	v, err := closureProof(ctx, all[at], repo, attempt, digest)
	if err != nil {
		return out, err
	}
	report, err = filepath.Abs(report)
	if err != nil {
		return out, err
	}
	b, err := os.ReadFile(report)
	if err != nil {
		return out, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return out, errors.New("independent review report is empty")
	}
	out = FixReview{Finding: id, Fixed: v.Fixed, VerificationAttempt: attempt, VerificationDigest: digest, Worker: strings.TrimSpace(worker), Reviewer: strings.TrimSpace(reviewer), ReportPath: report, ReportDigest: digestBytes(b), Status: "WAITING_OWNER_APPROVAL", At: time.Now().UTC().Format(time.RFC3339Nano)}
	out.Digest = reviewDigest(out)
	all[at].FixReview = &out
	if err = rewrite(all); err != nil {
		return FixReview{}, err
	}
	return out, nil
}

// ApproveFix accepts only the stored review and revalidates its complete chain.
// Callers must obtain an actual owner decision; CLI enforces a terminal prompt.
func ApproveFix(ctx context.Context, id, repo, verificationDigest, reviewHash, owner, reason string) (Finding, error) {
	var out Finding
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(reason) == "" {
		return out, errors.New("owner identity and acceptance reason are required")
	}
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return out, err
	}
	defer unlock()
	all, at, err := closureFinding(id)
	if err != nil {
		return out, err
	}
	r := all[at].FixReview
	if r == nil || r.Digest != reviewHash || reviewDigest(*r) != reviewHash || r.VerificationDigest != verificationDigest || r.Finding != id || r.Status != "WAITING_OWNER_APPROVAL" || strings.EqualFold(r.Worker, r.Reviewer) {
		return out, errors.New("exact independent review required before owner acceptance")
	}
	v, err := closureProof(ctx, all[at], repo, r.VerificationAttempt, verificationDigest)
	if err != nil {
		return out, err
	}
	if v.Fixed != r.Fixed {
		return out, errors.New("review fixed revision differs")
	}
	b, err := os.ReadFile(r.ReportPath)
	if err != nil {
		return out, err
	}
	if digestBytes(b) != r.ReportDigest {
		return out, errors.New("independent review report changed")
	}
	all[at].State = VerifiedFixed
	all[at].OwnerAcceptance = &OwnerAcceptance{Owner: strings.TrimSpace(owner), Reason: strings.TrimSpace(reason), ReviewDigest: reviewHash, VerificationDigest: verificationDigest, Fixed: v.Fixed, At: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = rewrite(all); err != nil {
		return out, err
	}
	return all[at], nil
}

func closureFinding(id string) ([]Finding, int, error) {
	all, err := load()
	if err != nil {
		return nil, 0, err
	}
	for i, f := range all {
		if f.ID == id {
			if f.State != FixInProgress || reproducedCount(f) == 0 {
				return nil, 0, errors.New("closure requires approved FIX_IN_PROGRESS with retained red proof")
			}
			return all, i, nil
		}
	}
	return nil, 0, fmt.Errorf("unknown finding %s", id)
}

func closureProof(ctx context.Context, f Finding, repo, attempt, digest string) (Verification, error) {
	var v Verification
	if !attemptName.MatchString(attempt) || len(digest) != 64 {
		return v, errors.New("exact verification attempt and SHA256 digest required")
	}
	p := filepath.Join(contract.IndexDir(), "hunter", "evidence", f.ID, "verify-"+attempt+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return v, err
	}
	if digestBytes(b) != digest {
		return v, errors.New("runtime verification evidence changed")
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	if v.Finding != f.ID || v.Attempt != attempt || v.Baseline != f.Baseline || !fullRevision.MatchString(v.Fixed) || v.Status != "WAITING_INDEPENDENT_REVIEW" || len(v.ProtectedTests) == 0 {
		return v, errors.New("runtime verification identity or status mismatch")
	}
	red := v.BaselineReplay
	if red.Finding != f.ID || red.Revision != f.Baseline || red.Exit <= 0 || red.Exit != red.ExpectedExit || red.Error != "" || len(red.Argv) == 0 {
		return v, errors.New("baseline replay is not a proven red check")
	}
	for _, e := range []Execution{v.Regression, v.Suite} {
		if e.Finding != f.ID || e.Revision != v.Fixed || e.Exit != 0 || e.Error != "" || len(e.Argv) == 0 {
			return v, errors.New("fixed regression and existing suite must pass")
		}
	}
	if !reflect.DeepEqual(red.Argv, v.Regression.Argv) {
		return v, errors.New("red and green commands differ")
	}
	found := false
	for _, a := range f.Attempts {
		if a.Observed && a.Outcome == "reproduced" && a.EvidenceHash == v.RedEvidenceHash && a.Revision == f.Baseline {
			expected, err := filepath.Abs(filepath.Join(contract.IndexDir(), "hunter", "evidence", f.ID, a.Attempt+".json"))
			if err != nil || !attemptName.MatchString(a.Attempt) || a.EvidencePath != expected {
				return v, errors.New("red evidence path differs")
			}
			data, err := os.ReadFile(expected)
			if err != nil {
				return v, err
			}
			if digestBytes(data) != a.EvidenceHash {
				return v, errors.New("baseline reproduction evidence changed")
			}
			found = true
		}
	}
	if !found {
		return v, errors.New("verification has no retained observed red evidence")
	}
	head, err := reproGit(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != v.Fixed {
		return v, errors.New("verification is stale at current HEAD")
	}
	status, err := reproGit(ctx, repo, "status", "--porcelain")
	if err != nil || len(status) != 0 {
		return v, errors.New("review acceptance requires a clean fixed checkout")
	}
	return v, nil
}
