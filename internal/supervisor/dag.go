package supervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

type TaskStatus struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

// Only Execute may overlap. All other callbacks run in the controller loop.
// Execute must return only after bounded termination/cleanup, or return an error
// leaving its intent fenced. Callbacks must never mutate the supplied values.
type DAGOperations struct {
	InvocationID string
	Authority    func(context.Context, *run.Controller) error
	Prepare      func(context.Context, *run.Controller, Task, []ResolvedInput, *PreparedAttempt, *TerminalResult) (PreparedAttempt, error)
	Execute      func(context.Context, *run.Controller, Task, PreparedAttempt) (ExecutionResult, error)
	Finalize     func(context.Context, *run.Controller, Task, PreparedAttempt, ExecutionResult) (TerminalResult, error)
	Revalidate   func(context.Context, *run.Controller, Task, PreparedAttempt, TerminalResult) error
}

func NewDAGID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// RunDAG is deliberately a separate protocol from serial schema-2 replay.
func RunDAG(ctx context.Context, p Plan, hash string, ops DAGOperations) (GraphResult, error) {
	out := GraphResult{State: routing.WaitBlockedReconciliation, Status: "reconciliation"}
	authorized, h, err := Decode(p.raw)
	if err != nil {
		return out, err
	}
	if p.Schema != 3 || h != hash || !bytes.Equal(mustJSON(p), mustJSON(authorized)) {
		return out, errors.New("DAG requires unchanged schema 3 authorization")
	}
	p = authorized
	if ops.Authority == nil || ops.Prepare == nil || ops.Execute == nil || ops.Finalize == nil || ops.Revalidate == nil {
		return out, errors.New("DAG production operations required")
	}
	c, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		return out, err
	}
	defer c.Close()
	m := c.Marker()
	repo, err := filepath.EvalSymlinks(p.Repo)
	if err != nil {
		return out, err
	}
	root, err := filepath.EvalSymlinks(m.Root)
	if err != nil || root != repo || m.Run != p.Run {
		return out, errors.New("DAG marker identity mismatch")
	}
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return out, err
	}
	log := &dagLog{path: dagJournalPath(loc.Marker, hash)}
	states, err := log.load(p, hash)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
	defer cancel()
	start, err := log.startInvocation(p, hash, ops.InvocationID, c)
	if err != nil {
		return out, err
	}
	out, err = runDAGOwned(ctx, p, ops, c, log, states)
	if telemetryErr := log.returnInvocation(start, c, out, err); telemetryErr != nil {
		out.State = routing.WaitBlockedReconciliation
		out.Status = "reconciliation"
		err = errors.Join(err, telemetryErr)
	}
	return out, err
}

type dagCompletion struct {
	task     Task
	prepared PreparedAttempt
	intent   dagEntry
	result   ExecutionResult
	err      error
}

func runDAGOwned(ctx context.Context, p Plan, ops DAGOperations, c *run.Controller, log *dagLog, states map[string]*dagTaskState) (out GraphResult, returned error) {
	out.State = routing.WaitBlockedReconciliation
	out.Status = "reconciliation"
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan dagCompletion, p.Execution.MaxParallel)
	active := 0
	used := map[string]bool{}
	for _, s := range states {
		out.Checks += s.Attempts
		for id, seen := range s.SeenAttempts {
			used[id] = seen
		}
	}
	authority := func(checkCtx context.Context) error {
		if err := graphAuthority(checkCtx, c, p); err != nil {
			return err
		}
		if err := ops.Authority(checkCtx, c); err != nil {
			return err
		}
		for _, t := range p.Tasks {
			s := states[t.Task]
			if s.Stage == "done" || s.Stage == "wait" || s.Stage == "cancelled" {
				if err := ops.Revalidate(checkCtx, c, t, s.Prepared, s.Terminal); err != nil {
					return fmt.Errorf("terminal task %s: %w", t.Task, err)
				}
			}
		}
		return nil
	}
	intent := func(t Task, op, attempt string) (dagEntry, error) {
		if err := c.Check(); err != nil {
			return dagEntry{}, err
		}
		id, err := NewDAGID()
		if err != nil {
			return dagEntry{}, err
		}
		e := dagEntry{Task: t.Task, Attempt: attempt, OpID: id, Phase: "intent", Operation: op}
		return e, log.append(e)
	}
	result := func(t Task, e dagEntry, value any, operationErr error) error {
		if err := c.Check(); err != nil {
			return err
		}
		if operationErr != nil {
			e.Phase = "error"
			e.Error = operationErr.Error()
			return errors.Join(operationErr, log.append(e))
		}
		e.Phase = "result"
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		e.Data = raw
		next := *states[t.Task]
		if err := next.apply(p, t, e); err != nil {
			return err
		}
		if e.Operation == "prepare" {
			for _, in := range next.Prepared.Inputs {
				dep := states[in.Producer]
				if dep == nil || dep.Stage != "done" || dep.Terminal.Artifacts == nil || !bytes.Equal(mustJSON(in.Artifact), mustJSON(dep.Terminal.Artifacts)) {
					return errors.New("prepared consumer binding differs from verified predecessor")
				}
			}
		}
		if err := log.append(e); err != nil {
			return err
		}
		*states[t.Task] = next
		return nil
	}
	finalize := func(finCtx context.Context, done dagCompletion) error {
		if err := result(done.task, done.intent, done.result, done.err); err != nil {
			return err
		}
		if err := c.Check(); err != nil {
			return err
		}
		e, err := intent(done.task, "finalize", done.prepared.Attempt)
		if err != nil {
			return err
		}
		terminal, err := ops.Finalize(finCtx, c, done.task, done.prepared, done.result)
		return result(done.task, e, terminal, err)
	}
	// Keep the controller capability while cancelled external executions drain.
	// A failed drain leaves durable intents; a later invocation cannot relaunch.
	defer func() {
		if returned == nil {
			return
		}
		cancel()
		if ctx.Err() != nil {
			returned = errors.Join(returned, log.append(dagEntry{Phase: "control", Operation: "cancel", Error: ctx.Err().Error()}))
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for active > 0 {
			select {
			case done := <-completed:
				active--
				done.result.Cancelled = true
				returned = errors.Join(returned, finalize(cleanupCtx, done))
			case <-cleanupCtx.Done():
				returned = errors.Join(returned, errors.New("DAG drain unproven; reconcile owned attempts"))
				return
			}
		}
		out = dagResult(p, states, out.Checks, true)
		out.State = routing.WaitBlockedReconciliation
		out.Status = "reconciliation"
	}()
	for {
		if err := authority(ctx); err != nil {
			return out, err
		}
		admitted := false
		for _, t := range p.Tasks {
			if active >= p.Execution.MaxParallel {
				break
			}
			s := states[t.Task]
			if s.Stage != "" && s.Stage != "retry" {
				continue
			}
			if out.Checks >= p.MaxChecks {
				continue
			}
			ready := true
			for _, dep := range t.DependsOn {
				if states[dep].Stage != "done" {
					ready = false
				}
			}
			if !ready {
				continue
			}
			if err := authority(ctx); err != nil {
				return out, err
			}
			inputs := make([]ResolvedInput, 0, len(t.Inputs))
			for _, in := range t.Inputs {
				ref := states[in.Producer].Terminal.Artifacts
				if ref == nil {
					return out, errors.New("verified predecessor manifest missing")
				}
				inputs = append(inputs, ResolvedInput{in.Producer, in.Name, in.As, *ref})
			}
			e, err := intent(t, "prepare", "")
			if err != nil {
				return out, err
			}
			var prior *PreparedAttempt
			var priorTerminal *TerminalResult
			if s.Stage == "retry" {
				prior = &s.Prepared
				priorTerminal = &s.Terminal
			}
			prepared, err := ops.Prepare(ctx, c, t, inputs, prior, priorTerminal)
			if err == nil {
				if used[prepared.Attempt] {
					err = errors.New("DAG receipt attempt reused")
				}
				ct, loadErr := contract.Load(t.Task)
				if loadErr != nil {
					err = loadErr
				} else if prepared.Worktree != ct.Worktree {
					err = errors.New("DAG receipt worktree differs from contract")
				}
			}
			if err := result(t, e, prepared, err); err != nil {
				return out, err
			}
			used[prepared.Attempt] = true
			out.Checks++
			if states[t.Task].Stage == "wait" {
				admitted = true
				continue
			}
			if err := authority(ctx); err != nil {
				return out, err
			}
			e, err = intent(t, "execute", prepared.Attempt)
			if err != nil {
				return out, err
			}
			// Detach mutable slices/maps from the authoritative state before dispatch.
			var immutable PreparedAttempt
			if err := strictDecode(mustJSON(prepared), &immutable); err != nil {
				return out, err
			}
			active++
			admitted = true
			go func(t Task, a PreparedAttempt, e dagEntry) {
				r, err := ops.Execute(executionCtx, c, t, a)
				completed <- dagCompletion{t, a, e, r, err}
			}(t, immutable, e)
		}
		if active == 0 {
			if admitted {
				continue
			}
			out = dagResult(p, states, out.Checks, false)
			return out, nil
		}
		select {
		case <-ctx.Done():
			// A cancellation fence survives even when no task result can be collected.
			if err := log.append(dagEntry{Phase: "control", Operation: "cancel", Error: ctx.Err().Error()}); err != nil {
				return out, err
			}
			return out, ctx.Err()
		case done := <-completed:
			active--
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := finalize(cleanupCtx, done)
			cleanupCancel()
			if err != nil {
				return out, err
			}
		}
	}
}
func dagResult(p Plan, states map[string]*dagTaskState, checks int, uncertain bool) GraphResult {
	out := GraphResult{State: routing.WaitHuman, Status: "verified", Checks: checks, Reason: "all tasks verified; independent review and human approval required"}
	for _, t := range p.Tasks {
		s := states[t.Task]
		switch s.Stage {
		case "done":
			out.Completed = append(out.Completed, t.Task)
		case "wait":
			out.Failed = append(out.Failed, TaskStatus{t.Task, s.Terminal.Outcome.Reason})
		case "cancelled":
			out.Cancelled = append(out.Cancelled, TaskStatus{t.Task, "execution cancelled"})
		default:
			reason := "dependency failed or waiting"
			if checks >= p.MaxChecks {
				reason = "attempt/check budget exhausted"
			}
			if uncertain {
				reason = "run has uncertain side effects"
			}
			out.Blocked = append(out.Blocked, TaskStatus{t.Task, reason})
		}
	}
	if len(out.Failed)+len(out.Blocked)+len(out.Cancelled) > 0 {
		out.Status = "partial"
		out.Reason = "known branches failed, parked or blocked; inspect task outcomes"
	}
	return out
}
