package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

type graphExecution struct {
	Run             string    `json:"run"`
	Task            string    `json:"task"`
	Attempt         string    `json:"attempt"`
	Decision        string    `json:"decision"`
	Worktree        string    `json:"worktree"`
	Pool            string    `json:"pool"`
	Provider        string    `json:"provider"`
	PID             int       `json:"pid"`
	PIDStart        int64     `json:"pid_start"`
	PIDStartUnknown bool      `json:"pid_start_unknown,omitempty"`
	ExitCode        int       `json:"exit_code"`
	Output          string    `json:"output"`
	Truncated       bool      `json:"truncated"`
	Gone            bool      `json:"gone"`
	QuotaSignal     string    `json:"quota_signal,omitempty"`
	HandoffPath     string    `json:"handoff_path,omitempty"`
	HandoffDigest   string    `json:"handoff_digest,omitempty"`
	CompletedAt     time.Time `json:"completed_at"`
	inputDigest     string
}

func graphEvidencePath(attempt, kind string) string {
	return filepath.Join(contract.IndexDir(), "routes", "supervisor", attempt+"-"+kind+".json")
}
func graphHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func writeGraphEvidence(path string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".evidence-")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(name, path); err != nil {
		return "", err
	}
	for parent := dir; ; parent = filepath.Dir(parent) {
		d, err := os.Open(parent)
		if err != nil {
			return "", err
		}
		err = d.Sync()
		closeErr := d.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return "", err
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return graphHash(b), nil
}

func graphLaunchAuthority(control *run.Controller, c *contract.Contract, runID string) error {
	if err := control.Check(); err != nil {
		return err
	}
	m := control.Marker()
	if m.Run != runID || !m.Allows("task", c.Name) {
		return errors.New("controller does not authorize task/run")
	}
	root, err := run.Locate(m.Root)
	if err != nil {
		return err
	}
	work, err := run.Locate(c.Worktree)
	if err != nil {
		return err
	}
	a, err := filepath.EvalSymlinks(root.Common)
	if err != nil {
		return err
	}
	b, err := filepath.EvalSymlinks(work.Common)
	if err != nil || a != b {
		return errors.New("controller does not own task repository")
	}
	return nil
}

func graphLocalGone(e graphExecution) error {
	if !e.Gone || e.PID <= 0 || e.CompletedAt.IsZero() || (e.PIDStart == 0 && !e.PIDStartUnknown) {
		return errors.New("local completion evidence missing")
	}
	if e.PIDStartUnknown && e.PIDStart != 0 {
		return errors.New("local completion evidence identity is contradictory")
	}
	start, err := run.ProcStart(e.PID)
	if err == nil && start == e.PIDStart {
		return errors.New("provider process still alive")
	}
	if err != nil && !errors.Is(err, run.ErrNoProc) {
		return fmt.Errorf("provider process liveness unknown: %w", err)
	}
	return graphGroupGone(e.PID)
}
func loadGraphExecution(p supervisor.Plan, t supervisor.Task, d supervisor.Dispatch) (graphExecution, []byte, error) {
	var e graphExecution
	b, err := os.ReadFile(graphEvidencePath(d.Attempt, "execution"))
	if err != nil {
		return e, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return e, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e, nil, errors.New("trailing local execution evidence")
	}

	if e.Run != p.Run || e.Task != t.Task || e.Attempt != d.Attempt || e.Decision != d.Decision || e.Worktree != d.Worktree || e.Pool != d.Pool || d.OrcaTask != fmt.Sprintf("local:%d:%d", e.PID, e.PIDStart) || d.Dispatch != e.Decision {
		return e, nil, errors.New("local execution evidence identity mismatch")
	}
	if err := graphLocalGone(e); err != nil {
		return e, nil, err
	}
	return e, b, nil
}

func graphOperations(p supervisor.Plan) supervisor.Operations {
	handoffs := map[string]graphHandoffRef{}
	type continuation struct {
		dispatch supervisor.Dispatch
		outcome  supervisor.Outcome
	}
	previous := map[string]continuation{}
	return supervisor.Operations{
		InvocationID: os.Getenv("REIN_SUPERVISOR_INVOCATION"),
		Resume: func(ctx context.Context, control *run.Controller, t supervisor.Task, d supervisor.Dispatch, o supervisor.Outcome) error {
			ref, err := prepareGraphHandoff(control, p, t, d, o)
			if err == nil {
				handoffs[t.Task] = ref
				previous[t.Task] = continuation{d, o}
			}
			return err
		},
		Revalidate: func(ctx context.Context, control *run.Controller, t supervisor.Task, d supervisor.Dispatch, o supervisor.Outcome) error {
			return revalidateGraphCompletion(ctx, control, p, t, d, o)
		},
		Prepare: func(ctx context.Context, control *run.Controller, t supervisor.Task) (*routing.AutoResult, error) {
			c, err := contract.Load(t.Task)
			if err != nil {
				return nil, err
			}
			if err := graphLaunchAuthority(control, c, p.Run); err != nil {
				return nil, err
			}
			if err := routeBudget(c, p.Run); err != nil {
				return nil, err
			}
			owner, prof, marker, err := routeScope(c, p.Run)
			if err != nil {
				return nil, err
			}
			return routing.PrepareAuto(ctx, routing.AutoInput{Contract: c, Run: p.Run, TaskProfile: t.Profile, ConfigPath: t.ConfigPath, Owner: owner, Budget: prof, MarkerPath: marker, Base: t.Base, Timeout: time.Duration(p.TimeoutMS) * time.Millisecond})
		},
		Launch: func(ctx context.Context, control *run.Controller, t supervisor.Task, d *routing.Decision, argv []string) (supervisor.Dispatch, error) {
			var e graphExecution
			currentContract, err := contract.Load(t.Task)
			if err != nil {
				return supervisor.Dispatch{}, err
			}
			agent, model, err := launchIdentity(argv, currentContract, p.Run)
			if err != nil {
				return supervisor.Dispatch{}, err
			}
			current, err := routing.Validate(currentContract, p.Run, agent, model)
			if err != nil {
				return supervisor.Dispatch{}, err
			}
			if d == nil || current.Attempt != d.Attempt || current.DecisionID != d.DecisionID || current.Hold != d.Hold {
				return supervisor.Dispatch{}, errors.New("supervised receipt changed before input construction")
			}
			if prior, ok := previous[t.Task]; ok {
				ref, err := prepareGraphHandoff(control, p, t, prior.dispatch, prior.outcome)
				if err != nil {
					return supervisor.Dispatch{}, err
				}
				if ref != handoffs[t.Task] {
					return supervisor.Dispatch{}, errors.New("continuation identity changed before launch")
				}
			}
			enriched, digest, err := graphLaunchInput(control, p, t, d, argv, handoffs[t.Task])
			if err != nil {
				return supervisor.Dispatch{}, err
			}
			e.inputDigest = digest
			argv = enriched
			e.HandoffPath = handoffs[t.Task].Path
			e.HandoffDigest = handoffs[t.Task].Digest
			args := append([]string{"--task", t.Task, "--run", p.Run, "--"}, argv...)
			code := routeLaunch(ctx, args, control, &e, d)
			if !e.Gone {
				return supervisor.Dispatch{}, fmt.Errorf("local launch completion unknown (exit %d); reconcile receipt and lease", code)
			}
			if _, err := writeGraphEvidence(graphEvidencePath(d.Attempt, "execution"), e); err != nil {
				return supervisor.Dispatch{}, err
			}
			return supervisor.Dispatch{Task: t.Task, Attempt: d.Attempt, Decision: d.DecisionID, OrcaTask: fmt.Sprintf("local:%d:%d", e.PID, e.PIDStart), Dispatch: d.DecisionID, Worktree: d.Worktree, Pool: d.Pool}, nil
		},
		Observe: func(ctx context.Context, control *run.Controller, t supervisor.Task, d supervisor.Dispatch) (supervisor.Observation, error) {
			e, b, err := loadGraphExecution(p, t, d)
			if err != nil {
				return supervisor.Observation{}, err
			}
			state := supervisor.Completed
			if e.QuotaSignal != "" {
				state = supervisor.Quota
			} else if e.ExitCode != 0 {
				state = supervisor.Failed
				if !e.Truncated {
					cfg, err := providers.Load(t.ConfigPath)
					if err != nil {
						return supervisor.Observation{}, err
					}
					provider, ok := cfg.Providers[e.Provider]
					if !ok {
						return supervisor.Observation{}, errors.New("execution provider no longer configured")
					}
					lower := strings.ToLower(e.Output)
					for _, signal := range cfg.QuotaSignals[provider.Agent] {
						if strings.TrimSpace(signal) != "" && strings.Contains(lower, strings.ToLower(signal)) {
							state = supervisor.Quota
							break
						}
					}
					if state != supervisor.Quota && routing.ClassifyProbe(providers.Result{State: "down", Detail: e.Output}) == routing.ClassAuthRequired {
						state = supervisor.Auth
					}
				}
			}
			return supervisor.Observation{State: state, Evidence: graphHash(b)}, nil
		},
		Finish: func(ctx context.Context, control *run.Controller, t supervisor.Task, d supervisor.Dispatch, o supervisor.Observation) (supervisor.Outcome, error) {
			return finishGraphLocal(ctx, control, p, t, d, o)
		},
	}
}

func finishGraphLocal(ctx context.Context, control *run.Controller, p supervisor.Plan, t supervisor.Task, d supervisor.Dispatch, o supervisor.Observation) (supervisor.Outcome, error) {
	var out supervisor.Outcome
	e, b, err := loadGraphExecution(p, t, d)
	if err != nil {
		return out, err
	}
	if graphHash(b) != o.Evidence {
		return out, errors.New("observation evidence changed")
	}
	c, err := contract.Load(t.Task)
	if err != nil {
		return out, err
	}
	if err := graphLaunchAuthority(control, c, p.Run); err != nil {
		return out, err
	}
	cp, err := routing.FinalizeCheckpoint(t.Task, d.Attempt, p.Run, d.Worktree, t.Base, t.AllowedUntracked, t.ContractHash)
	if err != nil {
		return out, err
	}
	out.CheckpointDigest = cp.Digest
	out.ExecutionDigest = graphHash(b)
	// Unknown usage is charged at the reserved bound; no measured usage is invented.
	if _, err := routing.SettleAttempt(d.Attempt); err != nil {
		return out, err
	}
	switch o.State {
	case supervisor.Quota:
		if err := routing.SetCooldown(t.ConfigPath, e.Provider, "local worker matched configured quota signal; reset unknown", nil, false); err != nil {
			return out, err
		}
		return out, nil
	case supervisor.Auth:
		out.State = routing.WaitAuth
		out.Reason = "provider authentication required"
		return out, nil
	case supervisor.Failed:
		out.State = routing.WaitHuman
		out.Reason = "local provider exited unsuccessfully"
		return out, nil
	case supervisor.Completed:
	default:
		return out, errors.New("cannot finish nonterminal local execution")
	}
	type check struct {
		Argv     []string `json:"argv"`
		ExitCode int      `json:"exit_code"`
		Output   string   `json:"output"`
	}
	evidence := struct {
		Run, Task, Attempt, Checkpoint, Execution string
		Checks                                    []check
	}{Run: p.Run, Task: t.Task, Attempt: d.Attempt, Checkpoint: cp.Digest, Execution: graphHash(b)}
	for _, argv := range t.Verify {
		if err := graphLaunchAuthority(control, c, p.Run); err != nil {
			return out, err
		}
		if err := routing.VerifyCheckpoint(t.Task, d.Attempt); err != nil {
			return out, err
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = d.Worktree
		cmd.Env = os.Environ()
		var captured dispatchReceiptBuffer
		cmd.Stdout = &captured
		cmd.Stderr = &captured
		if err := configureGraphProcess(cmd); err != nil {
			return out, err
		}
		err := cmd.Run()
		code := 0
		if err != nil {
			code = 1
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			}
		}
		if cmd.Process == nil {
			return out, errors.New("verification command could not start")
		}
		if err := graphGroupGone(cmd.Process.Pid); err != nil {
			return out, err
		}
		if err := routing.VerifyCheckpoint(t.Task, d.Attempt); err != nil {
			return out, fmt.Errorf("verification changed the captured worktree: %w", err)
		}
		evidence.Checks = append(evidence.Checks, check{Argv: argv, ExitCode: code, Output: captured.String()})
		if code != 0 || captured.overflow {
			out.State = routing.WaitHuman
			out.Reason = "objective verification failed or output exceeded limit"
			_, err := writeGraphEvidence(graphEvidencePath(d.Attempt, "verification"), evidence)
			return out, err
		}
	}
	out.VerificationDigest, err = writeGraphEvidence(graphEvidencePath(d.Attempt, "verification"), evidence)
	return out, err
}

// Process-group proof is valid only for an explicitly approved cooperative
// harness that does not detach; this is not a same-UID security sandbox.
func validateLocalGraphPlan(p supervisor.Plan) error {
	seenWorktrees := map[string]bool{}
	pools := map[string]bool{}
	for _, pool := range p.Pools {
		pools[pool] = true
	}
	for _, t := range p.Tasks {
		c, err := contract.Load(t.Task)
		if err != nil {
			return err
		}
		if routing.ContractHash(c) != t.ContractHash {
			return fmt.Errorf("task %s contract changed", t.Task)
		}
		wt, err := filepath.EvalSymlinks(c.Worktree)
		if err != nil {
			return err
		}
		if seenWorktrees[wt] {
			return errors.New("local graph requires a distinct worktree per task so prior checkpoint evidence stays valid")
		}
		seenWorktrees[wt] = true
		if t.ProcessPolicy != "non_detaching" {
			return errors.New("local graph requires process_policy non_detaching; detached descendants cannot be proven gone")
		}
		configBytes, err := os.ReadFile(t.ConfigPath)
		if err != nil {
			return err
		}
		if graphHash(configBytes) != t.ConfigDigest {
			return fmt.Errorf("task %s provider config changed", t.Task)
		}
		cfg, err := providers.Load(t.ConfigPath)
		if err != nil {
			return err
		}
		profile, err := routing.DecodeTaskProfile(t.Profile)
		if err != nil {
			return err
		}
		if profile.ContractHash != t.ContractHash {
			return fmt.Errorf("task %s profile contract hash does not match plan", t.Task)
		}
		if profile.Phase != "worker" {
			return errors.New("graph task profiles must describe worker execution")
		}
		for provider, argv := range t.Launches {
			if _, err := graphPromptSlot(argv); err != nil {
				return fmt.Errorf("task %s provider %s: %w", t.Task, provider, err)
			}
			selected, ok := cfg.Providers[provider]
			if !ok || selected.Launch != "shell" || isOrcaLaunch(argv) {
				return errors.New("graph supports local shell providers only; remote worker death cannot be proven")
			}
			quotaPool, err := routing.QuotaPool(selected)
			if err != nil {
				return err
			}
			if !pools[quotaPool] {
				return errors.New("graph pools must include every authorized launch provider quota pool")
			}

		}
	}
	return nil
}

func revalidateGraphCompletion(ctx context.Context, control *run.Controller, p supervisor.Plan, t supervisor.Task, d supervisor.Dispatch, out supervisor.Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, err := contract.Load(t.Task)
	if err != nil {
		return err
	}
	if err := graphLaunchAuthority(control, c, p.Run); err != nil {
		return err
	}
	_, executionBytes, err := loadGraphExecution(p, t, d)
	if err != nil {
		return err
	}
	cp, err := routing.LoadCheckpoint(t.Task, d.Attempt)
	if err != nil {
		return err
	}
	if cp.Run != p.Run || cp.Task != t.Task || cp.Attempt != d.Attempt || cp.ContractHash != t.ContractHash || cp.Worktree != d.Worktree || cp.Digest != out.CheckpointDigest {
		return errors.New("completed checkpoint identity or digest changed")
	}
	if err := routing.VerifyCheckpoint(t.Task, d.Attempt); err != nil {
		return err
	}
	b, err := os.ReadFile(graphEvidencePath(d.Attempt, "verification"))
	if err != nil {
		return err
	}
	if graphHash(b) != out.VerificationDigest {
		return errors.New("completed verification evidence digest changed")
	}
	var evidence struct {
		Run, Task, Attempt, Checkpoint, Execution string
		Checks                                    []struct {
			Argv     []string `json:"argv"`
			ExitCode int      `json:"exit_code"`
			Output   string   `json:"output"`
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing verification evidence")
	}

	if evidence.Run != p.Run || evidence.Task != t.Task || evidence.Attempt != d.Attempt || evidence.Checkpoint != cp.Digest || evidence.Execution != graphHash(executionBytes) || len(evidence.Checks) != len(t.Verify) {
		return errors.New("completed verification identity changed")
	}
	for i, check := range evidence.Checks {
		actual, _ := json.Marshal(check.Argv)
		want, _ := json.Marshal(t.Verify[i])
		if check.ExitCode != 0 || !bytes.Equal(actual, want) {
			return errors.New("completed verification argv or exit evidence changed")
		}
	}
	return nil
}
