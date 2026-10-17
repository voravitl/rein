package hunter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// Verification records runtime observations; independent review and owner
// approval remain separate gates and cannot be asserted by a command argument.
type Verification struct {
	Finding         string    `json:"finding"`
	Attempt         string    `json:"attempt"`
	Baseline        string    `json:"baseline"`
	Fixed           string    `json:"fixed"`
	RedEvidenceHash string    `json:"red_evidence_hash"`
	ProtectedTests  []string  `json:"protected_tests"`
	BaselineReplay  Execution `json:"baseline_replay"`
	Regression      Execution `json:"regression"`
	Suite           Execution `json:"suite"`
	Status          string    `json:"status"`
	EvidencePath    string    `json:"evidence_path"`
	EvidenceHash    string    `json:"evidence_hash,omitempty"`
}

// VerifyRuntime replays the recorded red argv, then the existing suite, in a
// disposable exact-revision clone. It never promotes a finding to VERIFIED_FIXED.
func VerifyRuntime(ctx context.Context, id, repo, attempt, fixed, redAttempt string, tests, suite []string, timeout time.Duration) (Verification, error) {
	var result Verification
	if runtime.GOOS == "windows" || !attemptName.MatchString(attempt) || !fullRevision.MatchString(fixed) || timeout <= 0 || timeout > time.Hour || len(tests) == 0 || len(suite) == 0 || strings.TrimSpace(suite[0]) == "" {
		return result, errors.New("verification requires supported process containment, exact fixed SHA, safe attempt, protected test paths, suite argv and timeout (0,1h]")
	}
	finding, err := Get(id)
	if err != nil {
		return result, err
	}
	var red ReproAttempt
	for _, a := range finding.Attempts {
		if a.Attempt == redAttempt {
			red = a
			break
		}
	}
	if !red.Observed || red.Outcome != "reproduced" || red.Revision != finding.Baseline || red.EvidenceHash == "" {
		return result, errors.New("verification requires an observed baseline reproduction")
	}
	canonical, err := filepath.Abs(filepath.Join(contract.IndexDir(), "hunter", "evidence", id, redAttempt+".json"))
	if err != nil || !attemptName.MatchString(redAttempt) || red.EvidencePath != canonical {
		return result, errors.New("red evidence must use its runner-owned attempt path")
	}
	data, err := os.ReadFile(red.EvidencePath)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != red.EvidenceHash {
		return result, errors.New("red execution evidence changed")
	}
	var execution Execution
	if err = json.Unmarshal(data, &execution); err != nil {
		return result, err
	}
	command, _ := json.Marshal(execution.Argv)
	if execution.Finding != id || execution.Attempt != redAttempt || execution.Revision != finding.Baseline || execution.Outcome != "reproduced" || execution.Exit <= 0 || execution.Exit != execution.ExpectedExit || execution.Exit != red.Exit || string(command) != red.Command || len(execution.Argv) == 0 || execution.Error != "" {
		return result, errors.New("red evidence identity or execution does not match registry")
	}
	repo, err = filepath.Abs(repo)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	head, err := reproGit(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != fixed {
		return result, errors.New("checkout HEAD must equal exact fixed revision")
	}
	if _, err = reproGit(ctx, repo, "merge-base", "--is-ancestor", finding.Baseline, fixed); err != nil {
		return result, errors.New("fixed revision must descend from reproduced baseline")
	}
	// Protected checks are byte-and-mode identical, a conservative refusal of
	// assertion weakening; additions or edits require fresh red evidence first.
	for _, path := range tests {
		if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == "." || strings.HasPrefix(path, "../") || strings.HasPrefix(path, ".git/") || path == ".git" || strings.ContainsAny(path, "\x00\n\r\t") || strings.HasPrefix(path, ":") {
			return result, fmt.Errorf("invalid protected test path %q", path)
		}
		before, e := reproGit(ctx, repo, "ls-tree", finding.Baseline, "--", path)
		if e != nil || !(strings.HasPrefix(string(before), "100644 blob ") || strings.HasPrefix(string(before), "100755 blob ")) || strings.Count(string(before), "\n") != 1 || !strings.HasSuffix(string(before), "\t"+path+"\n") {
			return result, fmt.Errorf("protected test must be a baseline tracked file: %s", path)
		}
		after, e := reproGit(ctx, repo, "ls-tree", fixed, "--", path)
		if e != nil || string(before) != string(after) {
			return result, fmt.Errorf("protected test changed, removed or weakened: %s", path)
		}
	}
	path, err := filepath.Abs(filepath.Join(contract.IndexDir(), "hunter", "evidence", id, "verify-"+attempt+".json"))
	if err != nil {
		return result, err
	}
	unlock, err := run.LockFile(path+".lock", 10*time.Second)
	if err != nil {
		return result, err
	}
	defer unlock()
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("verification evidence already exists or is inaccessible")
	}
	tmp, err := os.MkdirTemp("", "rein-hunter-verify-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")
	if out, e := reproGit(ctx, "", "clone", "--no-local", "--no-hardlinks", "--no-checkout", "--", repo, clone); e != nil {
		return result, fmt.Errorf("clone: %w: %s", e, out)
	}
	if out, e := reproGit(ctx, clone, "checkout", "--detach", finding.Baseline); e != nil {
		return result, fmt.Errorf("checkout: %w: %s", e, out)
	}
	result = Verification{Finding: id, Attempt: attempt, Baseline: finding.Baseline, Fixed: fixed, RedEvidenceHash: red.EvidenceHash, ProtectedTests: append([]string(nil), tests...), Status: "RUNTIME_FAILED", EvidencePath: path, Regression: Execution{Exit: -1}, Suite: Execution{Exit: -1}}
	result.BaselineReplay = verificationExecution(ctx, clone, id, attempt, finding.Baseline, execution.Argv)
	result.BaselineReplay.ExpectedExit = execution.ExpectedExit
	if result.BaselineReplay.Exit == execution.ExpectedExit && result.BaselineReplay.Error == "" {
		if e := resetVerificationCheckout(ctx, clone, fixed); e != nil {
			result.Regression.Error = "reset fixed checkout: " + e.Error()
		} else {
			result.Regression = verificationExecution(ctx, clone, id, attempt, fixed, execution.Argv)
		}
	}
	// Each check starts from the committed tree; execution artifacts cannot
	// replace or disable the following check. Reset failures retain evidence.
	if result.Regression.Exit == 0 && result.Regression.Error == "" {
		if e := resetVerificationCheckout(ctx, clone, fixed); e != nil {
			result.Suite.Error = "reset suite checkout: " + e.Error()
		} else {
			result.Suite = verificationExecution(ctx, clone, id, attempt, fixed, suite)
			if result.Suite.Exit == 0 && result.Suite.Error == "" {
				result.Status = "WAITING_INDEPENDENT_REVIEW"
			}
		}
	}
	head, err = reproGit(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != fixed {
		result.Status = "STALE"
	}
	data, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	data = append(data, '\n')
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return result, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return result, err
	}
	if err = syncHunterDirectory(filepath.Dir(path)); err != nil {
		return result, err
	}
	digest = sha256.Sum256(data)
	result.EvidenceHash = hex.EncodeToString(digest[:])
	if result.Status != "WAITING_INDEPENDENT_REVIEW" {
		return result, fmt.Errorf("verification %s; evidence: %s", result.Status, path)
	}
	return result, nil
}

func verificationExecution(ctx context.Context, dir, id, attempt, revision string, argv []string) Execution {
	record := Execution{Finding: id, Attempt: attempt, Revision: revision, Argv: append([]string(nil), argv...), Cwd: dir, Exit: -1, Outcome: "setup-failure", Started: time.Now().UTC().Format(time.RFC3339Nano)}
	var output cappedOutput
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = reproEnvironment()
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.WaitDelay = time.Second
	configureReproProcess(cmd)
	err := cmd.Run()
	stopReproProcess(cmd)
	record.Output, record.Truncated = output.b.String(), output.truncated
	switch {
	case ctx.Err() != nil:
		record.Error = ctx.Err().Error()
	case err == nil:
		record.Exit = 0
		record.Outcome = "passed"
	default:
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			record.Exit = exit.ExitCode()
			record.Outcome = "failed"
		} else {
			record.Error = err.Error()
		}
	}
	record.Finished = time.Now().UTC().Format(time.RFC3339Nano)
	return record
}

func resetVerificationCheckout(ctx context.Context, dir, revision string) error {
	if _, err := reproGit(ctx, dir, "reset", "--hard", revision); err != nil {
		return err
	}
	_, err := reproGit(ctx, dir, "clean", "-ffdx")
	return err
}
