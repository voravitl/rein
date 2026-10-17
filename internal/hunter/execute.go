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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var attemptName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Execution contains observations, not caller-supplied reproduction claims.
type Execution struct {
	Finding      string             `json:"finding"`
	Attempt      string             `json:"attempt"`
	Revision     string             `json:"revision"`
	Argv         []string           `json:"argv"`
	Cwd          string             `json:"cwd"`
	ExpectedExit int                `json:"expected_exit"`
	Exit         int                `json:"exit"`
	Outcome      string             `json:"outcome"`
	Started      string             `json:"started"`
	Finished     string             `json:"finished"`
	Output       string             `json:"output"`
	Truncated    bool               `json:"output_truncated"`
	Contract     *contract.Contract `json:"contract,omitempty"`
	Error        string             `json:"error,omitempty"`
}

// Reproduce runs explicitly authorized argv in a disposable clone, not an OS
// security sandbox. It never installs hooks, approves a fix or closes a finding.
func Reproduce(ctx context.Context, id, repo, attempt string, timeout time.Duration, expectExit int, argv []string) (ReproAttempt, error) {
	if runtime.GOOS == "windows" {
		return ReproAttempt{}, errors.New("Windows reproduction is unavailable until descendant containment is supported")
	}
	if !attemptName.MatchString(attempt) || timeout <= 0 || timeout > time.Hour || expectExit <= 0 || expectExit > 255 || len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return ReproAttempt{}, errors.New("reproduction requires a safe attempt name, timeout in (0,1h], expected failure exit 1..255 and explicit argv")
	}
	finding, err := Get(id)
	if err != nil {
		return ReproAttempt{}, err
	}
	if !fullRevision.MatchString(finding.Baseline) {
		return ReproAttempt{}, errors.New("reproduction requires an exact full baseline commit SHA")
	}
	switch finding.State {
	case VerifiedFixed, Duplicate, Rejected, OutOfScope:
		return ReproAttempt{}, errors.New("cannot reproduce a closed finding")
	}
	repo, err = filepath.Abs(repo)
	if err != nil {
		return ReproAttempt{}, err
	}
	st, err := os.Stat(repo)
	if err != nil || !st.IsDir() {
		return ReproAttempt{}, fmt.Errorf("local repository directory required: %s", repo)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	head, err := reproGit(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return ReproAttempt{}, fmt.Errorf("resolve repository HEAD: %w", err)
	}
	if strings.TrimSpace(string(head)) != finding.Baseline {
		return ReproAttempt{}, errors.New("repository HEAD differs from finding baseline; reproduce from the exact baseline checkout")
	}
	evidence, err := filepath.Abs(filepath.Join(contract.IndexDir(), "hunter", "evidence", id, attempt+".json"))
	if err != nil {
		return ReproAttempt{}, err
	}
	unlock, err := run.LockFile(evidence+".lock", 10*time.Second)
	if err != nil {
		return ReproAttempt{}, err
	}
	defer unlock()
	finding, err = Get(id)
	if err != nil {
		return ReproAttempt{}, err
	}
	for _, a := range finding.Attempts {
		if a.Attempt == attempt {
			return ReproAttempt{}, errors.New("attempt already recorded; use a new attempt identity")
		}
	}
	if _, err := os.Lstat(evidence); !errors.Is(err, os.ErrNotExist) {
		return ReproAttempt{}, fmt.Errorf("evidence already exists or is inaccessible: %s", evidence)
	}
	tmp, err := os.MkdirTemp("", "rein-hunter-*")
	if err != nil {
		return ReproAttempt{}, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")
	record := Execution{Finding: id, Attempt: attempt, Revision: finding.Baseline, Argv: append([]string(nil), argv...), Cwd: clone, ExpectedExit: expectExit, Exit: -1, Outcome: "setup-failure", Started: time.Now().UTC().Format(time.RFC3339Nano)}
	if output, err := reproGit(ctx, "", "clone", "--no-local", "--no-hardlinks", "--no-checkout", "--", repo, clone); err != nil {
		record.Error = fmt.Sprintf("clone: %v", err)
		record.Output = string(output)
	} else if output, err := reproGit(ctx, clone, "checkout", "--detach", finding.Baseline); err != nil {
		record.Error = fmt.Sprintf("checkout: %v", err)
		record.Output = string(output)
	} else {
		reproContract := ReproducerContract("repro-"+id+"-"+attempt, clone)
		record.Contract = &reproContract
		index, err := writeReproducerContract(tmp, reproContract)
		if err != nil {
			return ReproAttempt{}, err
		}
		before, err := snapshotReproducer(clone)
		if err == nil {
			err = checkReproducerScope(before, before)
		}
		if err != nil {
			return ReproAttempt{}, err
		}
		var output cappedOutput
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = clone, &output, &output, time.Second
		cmd.Env = append(reproEnvironment(), "PIPELINE_CONTRACTS="+index, "REIN_TASK="+reproContract.Name)
		configureReproProcess(cmd)
		err = cmd.Run()
		stopReproProcess(cmd)
		record.Output, record.Truncated = output.b.String(), output.truncated
		switch {
		case ctx.Err() != nil:
			record.Error = ctx.Err().Error()
		case err == nil:
			record.Exit, record.Outcome = 0, "not-reproduced"
		default:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
				record.Exit, record.Outcome = exitErr.ExitCode(), "not-reproduced"
				if record.Exit == expectExit {
					record.Outcome = "reproduced"
				}
			} else {
				record.Error = err.Error()
			}
		}
		after, scopeErr := snapshotReproducer(clone)
		if scopeErr == nil {
			scopeErr = checkReproducerScope(before, after)
		}
		contractData, readErr := os.ReadFile(filepath.Join(index, reproContract.Name+".json"))
		expectedContract, _ := json.MarshalIndent(reproContract, "", "  ")
		contractInfo, statErr := os.Lstat(filepath.Join(index, reproContract.Name+".json"))
		if readErr != nil || statErr != nil || !contractInfo.Mode().IsRegular() || contractInfo.Mode().Perm() != 0400 || string(contractData) != string(expectedContract) {
			scopeErr = errors.New("reproducer scope violation: task contract changed")
		}
		if scopeErr != nil {
			record.Outcome = "scope-violation"
			record.Error = "reproducer scope validation: " + scopeErr.Error()
		}
	}
	record.Finished = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return ReproAttempt{}, err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
		return ReproAttempt{}, err
	}
	f, err := os.OpenFile(evidence, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ReproAttempt{}, err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return ReproAttempt{}, err
	}
	if err := syncHunterDirectory(filepath.Dir(evidence)); err != nil {
		return ReproAttempt{}, err
	}
	digest := sha256.Sum256(data)
	command, _ := json.Marshal(argv)
	a := ReproAttempt{Attempt: attempt, Revision: finding.Baseline, Observed: true, EvidencePath: evidence, EvidenceHash: hex.EncodeToString(digest[:]), Command: string(command), Cwd: clone, Exit: record.Exit, Outcome: record.Outcome, At: record.Finished}
	if err := RecordAttempt(id, a); err != nil {
		return a, fmt.Errorf("execution evidence saved at %s but registry update failed: %w", evidence, err)
	}
	return a, nil
}

func reproGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir, cmd.WaitDelay = dir, time.Second
	cmd.Env = reproEnvironment()
	var output cappedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	configureReproProcess(cmd)
	err := cmd.Run()
	stopReproProcess(cmd)
	return []byte(output.b.String()), err
}

// Output is capped so an authorized failing check cannot exhaust memory/disk.
type cappedOutput struct {
	mu        sync.Mutex
	b         strings.Builder
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := (1 << 20) - w.b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	_, _ = w.b.Write(p)
	return n, nil
}

func reproEnvironment() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}
