package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func dagRoot(p supervisor.Plan, hash string) string {
	return filepath.Join(contract.IndexDir(), "routes", "dag", p.Run, hash)
}
func dagAttemptRoot(p supervisor.Plan, a supervisor.PreparedAttempt) string {
	return filepath.Join(dagRoot(p, a.PlanHash), a.Task, a.Attempt)
}
func dagArtifactRoot() string           { return filepath.Join(contract.IndexDir(), "routes", "dag-artifacts") }
func dagEncode(v any) []byte            { b, _ := json.Marshal(v); return b }
func dagWrite(path string, v any) error { return supervisor.WriteImmutable(path, dagEncode(v), 0400) }

func pathsOverlap(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func canonicalDAGPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("DAG ownership path must be absolute and clean")
	}
	ancestor := path
	for {
		real, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			relative, err := filepath.Rel(ancestor, path)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, relative), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		ancestor = parent
	}
}
func validateLocalDAGPlan(p supervisor.Plan) error {
	index, err := canonicalDAGPath(contract.IndexDir())
	if err != nil {
		return err
	}
	var paths []string
	var identities []os.FileInfo
	for _, t := range p.Tasks {
		c, err := contract.Load(t.Task)
		if err != nil {
			return err
		}
		if routing.ContractHash(c) != t.ContractHash {
			return errors.New("DAG contract hash changed")
		}
		wt, err := filepath.EvalSymlinks(c.Worktree)
		if err != nil {
			return err
		}
		st, err := os.Stat(wt)
		if err != nil {
			return err
		}
		for i, prior := range paths {
			if pathsOverlap(wt, prior) || os.SameFile(st, identities[i]) {
				return errors.New("DAG worktree ownership overlaps")
			}
		}
		paths = append(paths, wt)
		identities = append(identities, st)
		report, err := canonicalDAGPath(c.ReportPath)
		if err != nil {
			return err
		}
		if pathsOverlap(report, index) {
			return errors.New("DAG report path is missing or overlaps controller state")
		}
		for _, other := range p.Tasks {
			if other.Task == t.Task {
				continue
			}
			ct, err := contract.Load(other.Task)
			if err != nil {
				return err
			}
			otherReport, err := canonicalDAGPath(ct.ReportPath)
			if err != nil {
				return err
			}
			otherWorktree, err := filepath.EvalSymlinks(ct.Worktree)
			if err != nil {
				return err
			}
			shared := false
			first, firstErr := os.Stat(report)
			second, secondErr := os.Stat(otherReport)
			if firstErr == nil && secondErr == nil {
				shared = os.SameFile(first, second)
			}
			if pathsOverlap(report, otherReport) || pathsOverlap(report, otherWorktree) || shared {
				return errors.New("DAG report ownership overlaps")
			}
		}
		if t.Kind == "worker" {
			single := p
			single.Tasks = []supervisor.Task{t}
			if err := validateLocalGraphPlan(single); err != nil {
				return err
			}
		}
	}
	return nil
}

type dagSourceFile struct {
	Path   string      `json:"path"`
	Digest string      `json:"digest"`
	Mode   os.FileMode `json:"mode"`
}
type dagSource struct {
	Run, PlanHash, Task, Attempt, Revision, Checkpoint string
	Files                                              []dagSourceFile
}

func prepareDAGSource(ctx context.Context, p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt) (string, error) {
	cp, err := routing.FinalizeCheckpoint(t.Task, "source-"+a.Attempt, p.Run, a.Worktree, t.Base, t.AllowedUntracked, t.ContractHash)
	if err != nil {
		return "", err
	}
	if cp.BaseSHA != t.Base || cp.HeadSHA != t.Base {
		return "", errors.New("DAG source HEAD must equal approved full commit SHA")
	}
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z")
	cmd.Dir = a.Worktree
	var names dispatchReceiptBuffer
	cmd.Stdout = &names
	cmd.Stderr = &names
	if err := cmd.Run(); err != nil || names.overflow {
		return "", errors.New("cannot list bounded source snapshot")
	}
	files := strings.Split(strings.TrimSuffix(names.String(), "\x00"), "\x00")
	for _, f := range cp.Files {
		if f.Status == "untracked" {
			files = append(files, f.Path)
		}
	}
	source := filepath.Join(dagAttemptRoot(p, a), "source")
	if err := os.Mkdir(source, 0755); err != nil {
		return "", err
	}
	evidence := dagSource{Run: p.Run, PlanHash: a.PlanHash, Task: t.Task, Attempt: a.Attempt, Revision: cp.HeadSHA, Checkpoint: cp.Digest}
	var total int64
	for _, name := range files {
		if name == "" {
			continue
		}
		if len(evidence.Files) >= 10000 {
			return "", errors.New("source snapshot file bound exceeded")
		}
		b, err := supervisor.ReadArtifact(a.Worktree, name, 64<<20)
		if err != nil {
			return "", err
		}
		total += int64(len(b))
		if total > 64<<20 {
			return "", errors.New("source snapshot byte bound exceeded")
		}
		info, err := os.Stat(filepath.Join(a.Worktree, name))
		if err != nil {
			return "", err
		}
		mode := os.FileMode(0444)
		if info.Mode().Perm()&0111 != 0 {
			mode = 0555
		}
		dest := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return "", err
		}
		if err := supervisor.WriteImmutable(dest, b, mode); err != nil {
			return "", err
		}
		evidence.Files = append(evidence.Files, dagSourceFile{name, graphHash(b), mode})
	}
	if err := routing.VerifyCheckpoint(t.Task, "source-"+a.Attempt); err != nil {
		return "", err
	}
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, 0555)
		}
		return nil
	}); err != nil {
		return "", err
	}
	// The checkpoint binds dirty files; source.json also binds every unmodified byte.
	if err := dagWrite(filepath.Join(dagAttemptRoot(p, a), "source.json"), evidence); err != nil {
		return "", err
	}
	return graphHash(dagEncode(evidence)), nil
}
func verifyDAGSource(p supervisor.Plan, a supervisor.PreparedAttempt) error {
	dir := dagAttemptRoot(p, a)
	raw, err := supervisor.ReadArtifact(dir, "source.json", 4<<20)
	if err != nil {
		return err
	}
	if graphHash(raw) != a.SourceDigest {
		return errors.New("source manifest changed")
	}
	var source dagSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return err
	}
	if source.Run != a.Run || source.PlanHash != a.PlanHash || source.Task != a.Task || source.Attempt != a.Attempt || source.Revision != a.SourceRevision {
		return errors.New("source identity mismatch")
	}
	cp, err := routing.LoadCheckpoint(a.Task, "source-"+a.Attempt)
	if err != nil {
		return err
	}
	if cp.Run != a.Run || cp.Task != a.Task || cp.Attempt != "source-"+a.Attempt || cp.Worktree != a.Worktree || cp.ContractHash != a.ContractHash || cp.HeadSHA != a.SourceRevision || cp.BaseSHA != a.SourceRevision || cp.Digest != source.Checkpoint {
		return errors.New("source checkpoint binding changed")
	}
	expected := map[string]bool{}
	for _, f := range source.Files {
		expected[f.Path] = true
	}
	if err := verifyDAGSourceTree(filepath.Join(dir, "source"), expected); err != nil {
		return err
	}
	for _, f := range source.Files {
		b, err := supervisor.ReadArtifact(filepath.Join(dir, "source"), f.Path, 64<<20)
		if err != nil {
			return err
		}
		if graphHash(b) != f.Digest {
			return errors.New("source bytes changed")
		}
		info, err := os.Stat(filepath.Join(dir, "source", f.Path))
		if err != nil || info.Mode().Perm() != f.Mode {
			return errors.New("source mode changed")
		}
	}
	return nil
}
func verifyDAGSourceTree(root string, files map[string]bool) error {
	dirs := map[string]bool{".": true}
	for file := range files {
		for parent := filepath.ToSlash(filepath.Dir(file)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			dirs[parent] = true
		}
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	pending := []string{"."}
	count := 0
	bound := len(files) + len(dirs)
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		f, err := r.Open(name)
		if err != nil {
			return err
		}
		entries, readErr := f.ReadDir(bound - count + 1)
		closeErr := f.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		count += len(entries)
		if count > bound {
			return errors.New("source snapshot has unexpected entries")
		}
		for _, entry := range entries {
			relative := entry.Name()
			if name != "." {
				relative = name + "/" + relative
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("source snapshot link")
			}
			if entry.IsDir() {
				if !dirs[relative] {
					return errors.New("unexpected source directory")
				}
				pending = append(pending, relative)
			} else if !files[relative] {
				return errors.New("unexpected source file")
			}
		}
	}
	return nil
}
func verifyDAGInputs(p supervisor.Plan, a supervisor.PreparedAttempt) error {
	if a.InputDigest != supervisor.InputDigest(a.Inputs) {
		return errors.New("consumer input binding changed")
	}
	dir, err := os.Open(filepath.Join(dagAttemptRoot(p, a), "inputs"))
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(len(a.Inputs) + 1)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) != len(a.Inputs) {
		return errors.New("consumer input snapshot has unexpected entries")
	}
	for _, in := range a.Inputs {
		if in.Artifact.Manifest.Run != a.Run || in.Artifact.Manifest.PlanHash != a.PlanHash || in.Artifact.Manifest.Task != in.Producer {
			return errors.New("input from wrong run/plan/producer")
		}
		if err := supervisor.VerifyArtifacts(dagArtifactRoot(), in.Artifact); err != nil {
			return err
		}
		found := false
		for _, o := range in.Artifact.Manifest.Outputs {
			if o.Name != in.Name {
				continue
			}
			found = true
			b, err := supervisor.ReadArtifact(filepath.Join(dagAttemptRoot(p, a), "inputs"), in.As, 64<<20)
			if err != nil {
				return err
			}
			if graphHash(b) != o.SHA256 || int64(len(b)) != o.Size {
				return errors.New("consumer input snapshot changed")
			}
		}
		if !found {
			return errors.New("consumer input no longer declared")
		}
	}
	return nil
}
func checkDAGPrepared(p supervisor.Plan, a supervisor.PreparedAttempt) error {
	b, err := supervisor.ReadArtifact(dagAttemptRoot(p, a), "prepared.json", 1<<20)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, dagEncode(a)) {
		return errors.New("prepared receipt evidence changed")
	}
	if err := verifyDAGSource(p, a); err != nil {
		return err
	}
	return verifyDAGInputs(p, a)
}

func dagOperations(p supervisor.Plan, hash string) supervisor.DAGOperations {
	backendChecked := false
	return supervisor.DAGOperations{
		InvocationID: os.Getenv("REIN_SUPERVISOR_INVOCATION"),
		Authority: func(ctx context.Context, c *run.Controller) error {
			if err := validateLocalDAGPlan(p); err != nil {
				return err
			}
			if !backendChecked {
				if err := preflightDAGActions(ctx, p); err != nil {
					return err
				}
				backendChecked = true
			}
			return nil
		},
		Prepare: func(ctx context.Context, control *run.Controller, t supervisor.Task, inputs []supervisor.ResolvedInput, prior *supervisor.PreparedAttempt, terminal *supervisor.TerminalResult) (supervisor.PreparedAttempt, error) {
			if len(dagEncode(inputs)) > 256<<10 {
				return supervisor.PreparedAttempt{}, errors.New("resolved input metadata exceeds 256 KiB")
			}
			a := supervisor.PreparedAttempt{Number: 1, Run: p.Run, PlanHash: hash, Task: t.Task, Kind: t.Kind, ContractHash: t.ContractHash, SourceRevision: t.Base, InputDigest: supervisor.InputDigest(inputs), Inputs: inputs}
			c, err := contract.Load(t.Task)
			if err != nil {
				return a, err
			}
			if err := graphLaunchAuthority(control, c, p.Run); err != nil {
				return a, err
			}
			if why := routing.TaskBusy(t.Task); why != "" {
				return a, fmt.Errorf("task writer unresolved: %s", why)
			}
			a.Worktree = c.Worktree
			var ref graphHandoffRef
			var daemon string
			if prior != nil {
				a.Number = prior.Number + 1
				if terminal == nil {
					return a, errors.New("retry lacks prior terminal")
				}
				if t.Kind == "worker" {
					ref, err = prepareGraphHandoff(control, p, t, priorExecutionDispatch(p, *prior), terminal.Outcome)
					if err != nil {
						return a, err
					}
				} else {
					if err := checkDAGPrepared(p, *prior); err != nil {
						return a, err
					}
					_, execution, err := loadActionExecution(p, t, *prior)
					if err != nil {
						return a, err
					}
					recorded, err := supervisor.ReadArtifact(dagAttemptRoot(p, *prior), "terminal.json", 1<<20)
					if err != nil {
						return a, err
					}
					if !terminal.Retry || terminal.Cancelled || terminal.Artifacts != nil || terminal.Outcome.ExecutionDigest != graphHash(execution) || !bytes.Equal(recorded, dagEncode(terminal)) {
						return a, errors.New("prior action retry termination/evidence changed")
					}
				}
			}
			if t.Kind == "action" {
				daemon, err = preflightAction(ctx, t.Action)
				if err != nil {
					return a, err
				}
				a.Attempt, err = supervisor.NewDAGID()
				if err != nil {
					return a, err
				}
				a.ReceiptID, err = supervisor.NewDAGID()
				if err != nil {
					return a, err
				}
				a.Argv = append([]string(nil), t.Action.Argv...)
			} else {
				probePlan := p
				probePlan.TimeoutMS = min(p.TimeoutMS, 30000)
				prepared, prepareErr := graphOperations(probePlan).Prepare(ctx, control, t)
				if prepareErr != nil {
					if prepared == nil || routing.ErrCode(prepareErr) != routing.CodeNoCandidate || prepared.Receipt != nil || prepared.Attempt == "" {
						return a, prepareErr
					}
					holds, err := budget.HoldsFor(prepared.Attempt)
					if err != nil {
						return a, err
					}
					for _, h := range holds {
						if h.State == "reserved" || h.Run != p.Run || h.Task != t.Task {
							return a, errors.New("preparation wait has uncertain accounting")
						}
					}
					a.Worker = prepared
					a.Attempt = prepared.Attempt
					a.ReceiptID, err = supervisor.NewDAGID()
					if err != nil {
						return a, err
					}
					a.Park = &supervisor.Outcome{State: routing.WaitHuman, Reason: prepared.Error}
				} else {
					if prepared == nil || prepared.Receipt == nil {
						return a, errors.New("worker preparation did not yield a launch receipt; reconcile before retry")
					}
					a.Worker = prepared
					a.Attempt = prepared.Receipt.Attempt
					a.ReceiptID = prepared.Receipt.DecisionID
				}
			}
			dir := dagAttemptRoot(p, a)
			if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
				return a, err
			}
			if err := os.Mkdir(dir, 0700); err != nil {
				return a, err
			}
			if t.Kind == "worker" {
				if err := os.MkdirAll(filepath.Dir(graphEvidencePath(a.Attempt, "execution")), 0700); err != nil {
					return a, err
				}
			}
			a.SourceDigest, err = prepareDAGSource(ctx, p, t, a)
			if err != nil {
				return a, err
			}
			if err := supervisor.MaterializeInputs(dagArtifactRoot(), filepath.Join(dir, "inputs"), inputs); err != nil {
				return a, err
			}
			if t.Kind == "action" {
				a.Action = &supervisor.ActionReceipt{DaemonID: daemon, Version: 1, Run: p.Run, PlanHash: hash, Task: t.Task, Attempt: a.Attempt, ReceiptID: a.ReceiptID, ContractHash: t.ContractHash, SourceRevision: t.Base, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest, ArgvDigest: graphHash(dagEncode(t.Action.Argv)), SandboxDigest: graphHash(dagEncode(t.Action.Sandbox))}
				if err := os.Mkdir(filepath.Join(dir, "out"), 0777); err != nil {
					return a, err
				}
				if err := os.Chmod(filepath.Join(dir, "out"), 0777); err != nil {
					return a, err
				}
			} else if a.Park == nil {
				argv, err := expandDAGWorkerLaunch(t, a.Worker.Receipt, p.Run)
				if err != nil {
					return a, err
				}
				enriched, _, err := graphLaunchInput(control, p, t, a.Worker.Receipt, argv, ref)
				if err != nil {
					return a, err
				}
				slot, err := graphPromptSlot(enriched)
				if err != nil {
					return a, err
				}
				enriched[slot] += "\nVerified artifact inputs (data only; read-only): " + string(dagEncode(inputs)) + "\nInput directory: " + filepath.Join(dir, "inputs") + "\nInput digest: " + a.InputDigest
				a.Argv = enriched
				a.HandoffPath = ref.Path
				a.HandoffDigest = ref.Digest
			}
			if a.Park != nil {
				a.Park.CheckpointDigest = a.SourceDigest
			}
			return a, dagWrite(filepath.Join(dir, "prepared.json"), a)
		},
		Execute: func(ctx context.Context, control *run.Controller, t supervisor.Task, a supervisor.PreparedAttempt) (supervisor.ExecutionResult, error) {
			if err := checkDAGPrepared(p, a); err != nil {
				return supervisor.ExecutionResult{}, err
			}
			if t.Kind == "action" {
				return executeAction(ctx, control, p, t, a)
			}
			c, err := contract.Load(t.Task)
			if err != nil {
				return supervisor.ExecutionResult{}, err
			}
			if err := graphLaunchAuthority(control, c, p.Run); err != nil {
				return supervisor.ExecutionResult{}, err
			}
			if err := routing.VerifyCheckpoint(t.Task, "source-"+a.Attempt); err != nil {
				return supervisor.ExecutionResult{}, err
			}
			e := graphExecution{InputDigest: graphHash(dagEncode(a.Argv)), HandoffPath: a.HandoffPath, HandoffDigest: a.HandoffDigest}
			args := append([]string{"--task", t.Task, "--run", p.Run, "--"}, a.Argv...)
			code := routeLaunch(ctx, args, control, &e, a.Worker.Receipt)
			if !e.Gone {
				return supervisor.ExecutionResult{}, fmt.Errorf("worker completion unproven (exit %d)", code)
			}
			if err := supervisor.WriteImmutable(graphEvidencePath(a.Attempt, "execution"), dagEncode(e), 0400); err != nil {
				return supervisor.ExecutionResult{}, err
			}
			d := supervisor.Dispatch{Task: t.Task, Attempt: a.Attempt, Decision: a.ReceiptID, Dispatch: a.ReceiptID, Worktree: a.Worktree, Pool: a.Worker.Receipt.Pool, OrcaTask: fmt.Sprintf("local:%d:%d", e.PID, e.PIDStart)}
			observation, err := graphOperations(p).Observe(ctx, control, t, d)
			return supervisor.ExecutionResult{Dispatch: d, Observation: observation, Cancelled: ctx.Err() != nil}, err
		},
		Finalize: func(ctx context.Context, control *run.Controller, t supervisor.Task, a supervisor.PreparedAttempt, e supervisor.ExecutionResult) (terminal supervisor.TerminalResult, returned error) {
			defer func() {
				if returned == nil {
					returned = dagWrite(filepath.Join(dagAttemptRoot(p, a), "terminal.json"), terminal)
				}
			}()
			if err := checkDAGPrepared(p, a); err != nil {
				return supervisor.TerminalResult{}, err
			}
			var out supervisor.Outcome
			var err error
			outputRoot := a.Worktree
			if t.Kind == "action" {
				out, err = finalizeAction(ctx, control, p, t, a, e)
				outputRoot = filepath.Join(dagAttemptRoot(p, a), "out")
			} else {
				if err := validateDAGAccounting(p, a, false); err != nil {
					return terminal, err
				}
				observation := e.Observation
				if e.Cancelled {
					observation.State = supervisor.Failed
				}
				out, err = finishGraphLocal(ctx, control, p, t, e.Dispatch, observation)
			}
			if err != nil {
				return supervisor.TerminalResult{}, err
			}
			terminal = supervisor.TerminalResult{Outcome: out, Cancelled: e.Cancelled}
			if e.Cancelled {
				terminal.Outcome.State = routing.WaitHuman
				terminal.Outcome.Reason = "execution cancelled after proven cleanup"
				return terminal, nil
			}
			if t.Kind == "worker" && e.Observation.State == supervisor.Quota {
				if a.Number < t.MaxAttempts {
					terminal.Retry = true
				} else {
					terminal.Outcome.State = routing.WaitQuota
					terminal.Outcome.Reason = "quota retry budget exhausted"
				}
				return terminal, nil
			}
			if out.State != routing.WaitNone {
				if t.Kind == "action" && t.Action.Idempotent && a.Number < t.MaxAttempts {
					terminal.Retry = true
				}
				return terminal, nil
			}
			// First read detects known missing/invalid producer outputs before publication.
			for _, o := range t.Outputs {
				if _, err := supervisor.ReadArtifact(outputRoot, o.Path, o.MaxBytes); err != nil {
					terminal.Outcome.State = routing.WaitHuman
					terminal.Outcome.Reason = "declared output failed capture: " + err.Error()
					return terminal, nil
				}
			}
			m := supervisor.ArtifactManifest{Version: 1, Run: p.Run, PlanHash: hash, Task: t.Task, Attempt: a.Attempt, Receipt: a.ReceiptID, ContractHash: t.ContractHash, SourceRevision: a.SourceRevision, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest, ExecutionDigest: out.ExecutionDigest, VerificationDigest: out.VerificationDigest, CheckpointDigest: out.CheckpointDigest}
			ref, err := supervisor.PublishArtifacts(dagArtifactRoot(), outputRoot, m, t.Outputs)
			if err != nil {
				return terminal, err
			}
			terminal.Artifacts = &ref
			return terminal, nil
		},
		Revalidate: func(ctx context.Context, control *run.Controller, t supervisor.Task, a supervisor.PreparedAttempt, terminal supervisor.TerminalResult) error {
			if err := checkDAGPrepared(p, a); err != nil {
				return err
			}
			if t.Kind == "worker" {
				if why := routing.TaskBusy(t.Task); why != "" {
					return fmt.Errorf("terminal worker liveness uncertain: %s", why)
				}
				if err := validateDAGAccounting(p, a, true); err != nil {
					return err
				}
				if a.Park != nil {
					return nil
				}
			}
			raw, err := supervisor.ReadArtifact(dagAttemptRoot(p, a), "terminal.json", 1<<20)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, dagEncode(terminal)) {
				return errors.New("terminal evidence changed")
			}
			if terminal.Outcome.State != routing.WaitNone || terminal.Cancelled {
				if t.Kind == "worker" {
					d := priorExecutionDispatch(p, a)
					_, b, err := loadGraphExecution(p, t, d)
					if err != nil {
						return err
					}
					if graphHash(b) != terminal.Outcome.ExecutionDigest {
						return errors.New("failed worker execution changed")
					}
					cp, err := routing.LoadCheckpoint(t.Task, a.Attempt)
					if err != nil {
						return err
					}
					if cp.Digest != terminal.Outcome.CheckpointDigest {
						return errors.New("failed worker checkpoint changed")
					}
					return routing.VerifyCheckpoint(t.Task, a.Attempt)
				}
				_, b, err := loadActionExecution(p, t, a)
				if err != nil {
					return err
				}
				if graphHash(b) != terminal.Outcome.ExecutionDigest {
					return errors.New("failed action evidence changed")
				}
				return nil
			}
			if t.Kind == "worker" {
				if err := revalidateGraphCompletion(ctx, control, p, t, priorExecutionDispatch(p, a), terminal.Outcome); err != nil {
					return err
				}
				holds, err := budget.HoldsFor(a.Attempt)
				if err != nil {
					return err
				}
				for _, h := range holds {
					if h.State == "reserved" {
						return errors.New("successful worker accounting still reserved")
					}
				}
			} else {
				if err := revalidateAction(p, t, a, terminal.Outcome); err != nil {
					return err
				}
			}
			if terminal.Artifacts == nil {
				return errors.New("successful artifact manifest missing")
			}
			return supervisor.VerifyArtifacts(dagArtifactRoot(), *terminal.Artifacts)
		},
	}
}
func validateDAGAccounting(p supervisor.Plan, a supervisor.PreparedAttempt, settled bool) error {
	holds, err := budget.HoldsFor(a.Attempt)
	if err != nil {
		return err
	}
	expected := ""
	if a.Worker != nil && a.Worker.Receipt != nil {
		expected = a.Worker.Receipt.Hold
		if expected == "" {
			return errors.New("worker receipt lacks reservation identity")
		}
	}
	found := expected == ""
	for _, h := range holds {
		if h.ID == expected {
			found = true
		}
		if h.Run != p.Run || h.Task != a.Task || h.Attempt != a.Attempt || (settled && h.State == "reserved") {
			return errors.New("worker accounting unsettled or mismatched")
		}
	}
	if !found {
		return errors.New("worker reservation evidence missing")
	}
	return nil
}
func priorExecutionDispatch(p supervisor.Plan, a supervisor.PreparedAttempt) supervisor.Dispatch {
	var e graphExecution
	raw, err := os.ReadFile(graphEvidencePath(a.Attempt, "execution"))
	if err != nil || json.Unmarshal(raw, &e) != nil {
		return supervisor.Dispatch{}
	}
	return supervisor.Dispatch{Task: a.Task, Attempt: a.Attempt, Decision: a.ReceiptID, Dispatch: a.ReceiptID, Worktree: a.Worktree, Pool: e.Pool, OrcaTask: fmt.Sprintf("local:%d:%d", e.PID, e.PIDStart)}
}
func expandDAGWorkerLaunch(t supervisor.Task, d *routing.Decision, runID string) ([]string, error) {
	argv, ok := t.Launches[d.Provider]
	if !ok {
		return nil, errors.New("selected provider has no approved launch")
	}
	out := append([]string(nil), argv...)
	values := map[string]string{"{model}": d.Model, "{effort}": d.Effort, "{run}": runID, "{worktree}": d.Worktree, "{model}#{effort}": d.Model + "#" + d.Effort}
	for i, arg := range out {
		if v, ok := values[arg]; ok {
			out[i] = v
		}
	}
	return out, nil
}
