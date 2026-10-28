package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/voravitl/rein/internal/run"
)

type DAGNodeStatus struct {
	Task        string  `json:"task"`
	Kind        string  `json:"kind"`
	Layer       int     `json:"layer"`
	State       string  `json:"state"`
	Attempt     int     `json:"attempt"`
	MaxAttempts int     `json:"max_attempts"`
	Agent       *string `json:"agent"`
	Model       *string `json:"model"`
	Effort      *string `json:"effort"`
	GuardLine   *string `json:"guard_line"`
	Tokens      *int64  `json:"tokens"`
	ElapsedMS   *int64  `json:"elapsed_ms"`
}

type DAGStatus struct {
	Run      string          `json:"run"`
	PlanHash string          `json:"plan_hash"`
	Status   string          `json:"status"`
	Nodes    []DAGNodeStatus `json:"nodes"`
}

// Terminal deliberately excludes changing snapshots even if their complete
// prefix contains terminal results: an unfinished append is still unknown.
func (s DAGStatus) Terminal() bool {
	if s.Status == "changing" {
		return false
	}
	if s.Status == "reconciliation" {
		return true
	}
	for _, n := range s.Nodes {
		if !dagStatusTerminal(n.State) {
			return false
		}
	}
	return len(s.Nodes) > 0
}

func dagStatusTerminal(state string) bool {
	switch state {
	case "done", "wait", "failed", "blocked", "cancelled":
		return true
	}
	return false
}

type dagStatusSnapshot struct {
	states  map[string]*dagTaskState
	pending map[string]dagEntry
	entries []dagEntry
	failed  bool
}

// ReadDAGStatus observes an immutable byte snapshot without controller authority.
// All timing is advisory; neither this function nor its parser writes files.
func ReadDAGStatus(ctx context.Context, p Plan, hash string) (DAGStatus, error) {
	return readDAGStatus(ctx, p, hash, time.Now)
}

func readDAGStatus(ctx context.Context, p Plan, hash string, now func() time.Time) (DAGStatus, error) {
	var empty DAGStatus
	authorized, h, err := Decode(p.raw)
	if err != nil {
		return empty, err
	}
	if p.Schema != 3 || h != hash || !bytes.Equal(mustJSON(p), mustJSON(authorized)) {
		return empty, errors.New("status requires unchanged schema 3 plan")
	}
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return empty, err
	}
	path := dagJournalPath(loc.Marker, hash)
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		raw, err := readDAGStatusFile(path, 0)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
		missing := errors.Is(err, os.ErrNotExist)
		changing := !missing && (len(raw) == 0 || raw[len(raw)-1] != '\n')
		prefix := raw
		if changing {
			prefix = raw[:bytes.LastIndexByte(raw, '\n')+1]
		}
		snapshot, err := parseDAGStatus(p, hash, prefix)
		if err != nil {
			return empty, err
		}
		if changing && attempt < 2 {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return empty, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		view := projectDAGStatus(p, hash, snapshot)
		if changing {
			view.Status = "changing"
		}
		applyDAGTimes(&view, snapshot.entries, dagTimesPath(path), now())
		return view, nil
	}
	return empty, errors.New("status retry bound exhausted")
}

func projectDAGStatus(p Plan, hash string, snapshot *dagStatusSnapshot) DAGStatus {
	view := DAGStatus{Run: p.Run, PlanHash: hash, Status: "ok", Nodes: make([]DAGNodeStatus, len(p.Tasks))}
	indices := map[string]int{}
	taskErrors := map[string]bool{}
	for _, e := range snapshot.entries {
		if e.Phase == "error" {
			taskErrors[e.Task] = true
		}
	}
	for i, t := range p.Tasks {
		s := snapshot.states[t.Task]
		n := DAGNodeStatus{Task: t.Task, Kind: t.Kind, State: s.Stage, Attempt: s.Attempts, MaxAttempts: t.MaxAttempts}
		switch s.Stage {
		case "":
			n.State = "ready"
		case "prepared":
			n.State = "running"
		case "executed":
			n.State = "finalizing"
		}
		if pending := snapshot.pending[t.Task]; pending.Operation != "" {
			switch pending.Operation {
			case "prepare":
				n.State = "preparing"
			case "execute":
				n.State = "running"
			case "finalize":
				n.State = "finalizing"
			}
		}
		if taskErrors[t.Task] || (snapshot.failed && len(taskErrors) == 0 && !dagStatusTerminal(n.State)) {
			n.State = "failed"
		}
		if w := s.Prepared.Worker; w != nil && w.Receipt != nil {
			agent, model, effort := w.Receipt.Agent, w.Receipt.Model, w.Receipt.Effort
			if w.Launch != nil {
				agent, model, effort = w.Launch.Agent, w.Launch.Model, w.Launch.Effort
			}
			n.Agent, n.Model, n.Effort = &agent, &model, &effort
		}
		// Current seen logs carry hook generation, not attempt identity. Returning
		// null avoids attributing a prior attempt's activity to this receipt.
		view.Nodes[i] = n
		indices[t.Task] = i
	}
	visited := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		i := indices[name]
		n := &view.Nodes[i]
		for _, dep := range p.Tasks[i].DependsOn {
			visit(dep)
			d := view.Nodes[indices[dep]]
			if d.Layer >= n.Layer {
				n.Layer = d.Layer + 1
			}
			if n.State == "ready" || n.State == "pending" || n.State == "blocked" {
				if dagStatusTerminal(d.State) && d.State != "done" {
					n.State = "blocked"
				} else if d.State != "done" && n.State != "blocked" {
					n.State = "pending"
				}
			}
		}
	}
	for _, t := range p.Tasks {
		visit(t.Task)
	}
	if snapshot.failed {
		view.Status = "reconciliation"
	} else if view.Terminal() {
		view.Status = "done"
		for _, n := range view.Nodes {
			if n.State != "done" {
				view.Status = "partial"
			}
		}
	}
	return view
}

// parseDAGStatus mirrors the replay envelope checks on one immutable prefix.
// It reuses apply/strictDecode/dagInvocations, and omits only the replay-only
// reconciliation fence and fsync. Keep envelope checks aligned with dagLog.read.
func parseDAGStatus(p Plan, hash string, raw []byte) (*dagStatusSnapshot, error) {
	states := map[string]*dagTaskState{}
	tasks := map[string]Task{}
	for _, t := range p.Tasks {
		states[t.Task] = &dagTaskState{}
		tasks[t.Task] = t
	}
	if len(raw) == 0 {
		return &dagStatusSnapshot{states: states, pending: map[string]dagEntry{}}, nil
	}
	entries := []dagEntry{}
	pending := map[string]dagEntry{}
	ops := map[string]bool{}
	attempts := map[string]bool{}
	failed := false
	for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'}) {
		var e dagEntry
		if err := strictDecode(line, &e); err != nil {
			return nil, err
		}
		if e.Seq != i+1 {
			return nil, errors.New("DAG journal sequence mismatch")
		}
		entries = append(entries, e)
		if i == 0 {
			var h dagHeader
			if e.Phase != "header" || e.Operation != "bind" || e.Task != "" || e.Attempt != "" || e.OpID != "" || e.Error != "" {
				return nil, errors.New("invalid DAG header")
			}
			if err := strictDecode(e.Data, &h); err != nil {
				return nil, err
			}
			if h != (dagHeader{1, 3, p.Run, hash}) {
				return nil, errors.New("DAG journal plan binding changed")
			}
			continue
		}
		if e.Phase == "lifecycle" {
			continue
		}
		if e.Phase == "control" && e.Operation == "cancel" && e.Task == "" && e.Attempt == "" && e.OpID == "" && len(e.Data) == 0 && e.Error != "" {
			failed = true
			continue
		}
		s, ok := states[e.Task]
		if !ok {
			return nil, errors.New("unauthorized DAG journal task")
		}
		switch e.Phase {
		case "intent":
			if (failed && e.Operation != "finalize") || pending[e.Task].OpID != "" || !validDigest(e.OpID) || ops[e.OpID] || len(e.Data) != 0 || e.Error != "" || e.Operation != s.expected() {
				return nil, errors.New("invalid DAG intent")
			}
			want := s.Prepared.Attempt
			if e.Operation == "prepare" {
				want = ""
			}
			if e.Attempt != want {
				return nil, errors.New("DAG intent attempt changed")
			}
			if e.Operation == "prepare" {
				active := 0
				for _, other := range states {
					if other.Stage == "prepared" || other.Stage == "executed" {
						active++
					}
				}
				for task, entry := range pending {
					if entry.Operation == "prepare" && states[task].Stage != "prepared" {
						active++
					}
				}
				if active >= p.Execution.MaxParallel {
					return nil, errors.New("journal exceeds active attempt bound")
				}
				for _, dep := range tasks[e.Task].DependsOn {
					if states[dep].Stage != "done" {
						return nil, errors.New("DAG journal dependency violation")
					}
				}
			}
			ops[e.OpID] = true
			pending[e.Task] = e
		case "result", "error":
			prior := pending[e.Task]
			if prior.OpID == "" || prior.OpID != e.OpID || prior.Operation != e.Operation || prior.Attempt != e.Attempt {
				return nil, errors.New("DAG result lacks exact intent")
			}
			if e.Phase == "error" {
				if e.Error == "" || len(e.Data) != 0 {
					return nil, errors.New("invalid DAG error")
				}
				failed = true
			} else {
				if err := s.apply(p, tasks[e.Task], e); err != nil {
					return nil, fmt.Errorf("task %s: %w", e.Task, err)
				}
				if e.Operation == "prepare" {
					if attempts[s.Prepared.Attempt] {
						return nil, errors.New("DAG attempt reused")
					}
					attempts[s.Prepared.Attempt] = true
					if len(attempts) > p.MaxChecks {
						return nil, errors.New("DAG journal exceeds persistent attempt/check budget")
					}
					for _, in := range s.Prepared.Inputs {
						dep := states[in.Producer]
						if dep == nil || dep.Stage != "done" || dep.Terminal.Artifacts == nil || !bytes.Equal(mustJSON(in.Artifact), mustJSON(dep.Terminal.Artifacts)) {
							return nil, errors.New("stale predecessor artifact binding")
						}
					}
				}
			}
			delete(pending, e.Task)
		default:
			return nil, errors.New("unknown DAG journal phase")
		}
	}
	if _, err := dagInvocations(p, raw); err != nil {
		return nil, err
	}
	reconciliation := false
	for _, e := range entries {
		if e.Phase != "lifecycle" {
			continue
		}
		if e.Operation == "invocation_start" {
			reconciliation = false
		} else if e.Operation == "invocation_return" {
			var event GraphInvocationEvent
			if err := strictDecode(e.Data, &event); err != nil {
				return nil, err
			}
			reconciliation = event.Result != nil && event.Result.Status == "reconciliation"
		}
	}
	return &dagStatusSnapshot{states: states, pending: pending, entries: entries, failed: failed || reconciliation}, nil
}
