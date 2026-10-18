package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/voravitl/rein/internal/routing"
)

type graphEntry struct {
	Task      string          `json:"task"`
	Phase     string          `json:"phase"`
	Operation string          `json:"operation"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type graphTaskState struct {
	PreviousDispatch Dispatch
	PreviousOutcome  Outcome
	Stage            string
	Attempts         int
	SeenAttempts     map[string]bool
	Prepared         routing.AutoResult
	Dispatch         Dispatch
	Observation      Observation
	Outcome          Outcome
}

func strictDecode(b []byte, v any) error {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(b))); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing graph JSON")
	}
	return nil
}

func (s *graphTaskState) expected() string {
	switch s.Stage {
	case "", "retry":
		return "prepare"
	case "prepared":
		return "launch"
	case "launched":
		return "observe"
	case "observed":
		return "finish"
	}
	return ""
}

func (s *graphTaskState) apply(t Task, runID string, e graphEntry) error {
	if e.Operation != s.expected() || e.Phase != "result" || e.Error != "" {
		return errors.New("invalid graph result transition")
	}
	switch e.Operation {
	case "prepare":
		var p routing.AutoResult
		if err := strictDecode(e.Data, &p); err != nil {
			return err
		}
		d := p.Receipt
		if d == nil || p.Error != "" || p.ErrorCode != "" || d.Task != t.Task || d.Run != runID || d.ContractHash != t.ContractHash || d.ConfigPath != t.ConfigPath || d.ConfigHash != t.ConfigDigest || d.Attempt == "" || p.Attempt != d.Attempt || d.DecisionID == "" || d.Worktree == "" || d.Provider == "" || d.Pool == "" {
			return errors.New("prepared receipt does not match authorized graph task")
		}
		if s.Attempts >= t.MaxAttempts || s.SeenAttempts[d.Attempt] {
			return errors.New("attempt budget exhausted or receipt reused")
		}
		if _, err := expandLaunch(t, d, runID); err != nil {
			return err
		}
		seen := make(map[string]bool, len(s.SeenAttempts)+1)
		for k, v := range s.SeenAttempts {
			seen[k] = v
		}
		seen[d.Attempt] = true
		s.SeenAttempts = seen
		s.Attempts++
		if s.Stage == "retry" {
			s.PreviousDispatch = s.Dispatch
			s.PreviousOutcome = s.Outcome
		}
		s.Prepared = p
		s.Dispatch = Dispatch{}
		s.Observation = Observation{}
		s.Outcome = Outcome{}
		s.Stage = "prepared"
	case "launch":
		var d Dispatch
		if err := strictDecode(e.Data, &d); err != nil {
			return err
		}
		r := s.Prepared.Receipt
		if d.Task != t.Task || d.Attempt != r.Attempt || d.Decision != r.DecisionID || d.OrcaTask == "" || d.Dispatch == "" || d.Worktree != r.Worktree || d.Pool != r.Pool {
			return errors.New("dispatch identity differs from prepared receipt")
		}
		s.Dispatch = d
		s.Stage = "launched"
	case "observe":
		var o Observation
		if err := strictDecode(e.Data, &o); err != nil {
			return err
		}
		if o.Evidence == "" {
			return errors.New("observation lacks authoritative evidence")
		}
		switch o.State {
		case Running:
			s.Stage = "launched"
		case Completed, Quota, Auth, Failed:
			s.Stage = "observed"
		default:
			return errors.New("unknown observation outcome")
		}
		s.Observation = o
	case "finish":
		var o Outcome
		if err := strictDecode(e.Data, &o); err != nil {
			return err
		}
		if !validDigest(o.CheckpointDigest) {
			return errors.New("finish requires durable checkpoint digest")
		}
		switch o.State {
		case routing.WaitNone, routing.WaitQuota, routing.WaitAuth, routing.WaitBudget, routing.WaitHuman, routing.WaitReview, routing.WaitBlockedReconciliation:
		default:
			return errors.New("unknown finish wait state")
		}
		if o.State != "" {
			if o.Reason == "" {
				return errors.New("parked outcome requires reason")
			}
			s.Stage = "wait"
		} else {
			switch s.Observation.State {
			case Completed:
				if !validDigest(o.VerificationDigest) {
					return errors.New("completed task requires objective verification evidence digest")
				}
				s.Stage = "done"
			case Quota:
				s.Stage = "retry"
			default:
				return errors.New("auth or failed task cannot automatically advance")
			}
		}
		s.Outcome = o
	default:
		return errors.New("unknown graph operation")
	}
	return nil
}

type graphLog struct{ path string }

func (l graphLog) append(e graphEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// Reuse the monitor's durable append and parent-directory fsync implementation.
	j := journal{path: l.path}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(append(b, '\n'))
	if err == nil && n != len(b)+1 {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = j.syncDir()
	}
	return err
}
func (l graphLog) load(p Plan) (map[string]*graphTaskState, int, error) {
	return l.loadForRead(p, false)
}

func (l graphLog) loadForRead(p Plan, observeOnly bool) (map[string]*graphTaskState, int, error) {
	states := map[string]*graphTaskState{}
	tasks := map[string]Task{}
	for _, t := range p.Tasks {
		states[t.Task] = &graphTaskState{}
		tasks[t.Task] = t
	}
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return states, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if len(b) == 0 {
		return states, 0, nil
	}
	if b[len(b)-1] != '\n' {
		return nil, 0, errors.New("truncated graph journal")
	}
	if _, err := l.invocations(p, b); err != nil {
		return nil, 0, err
	}
	failed := false
	pending := ""
	operation := ""
	checks := 0
	for i, line := range bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'}) {
		var e graphEntry
		if err := strictDecode(line, &e); err != nil {
			return nil, 0, fmt.Errorf("graph journal line %d: %w", i+1, err)
		}
		if e.Phase == "lifecycle" {
			continue
		}
		if failed {
			return nil, 0, errors.New("task operation after failed graph operation")
		}
		s, ok := states[e.Task]
		if !ok {
			return nil, 0, errors.New("journal references unauthorized task")
		}
		switch e.Phase {
		case "intent":
			if pending != "" || e.Operation != s.expected() || len(e.Data) != 0 || e.Error != "" {
				return nil, 0, errors.New("invalid graph intent transition")
			}
			// Dependency gating must remain true after a restart, too.
			for _, dep := range tasks[e.Task].DependsOn {
				if states[dep].Stage != "done" {
					return nil, 0, errors.New("journal violates task dependencies")
				}
			}
			pending = e.Task
			operation = e.Operation
		case "result":
			if pending != e.Task || operation != e.Operation {
				return nil, 0, errors.New("graph result missing matching intent")
			}
			if err := s.apply(tasks[e.Task], p.Run, e); err != nil {
				return nil, 0, err
			}
			if operation == "prepare" {
				for name, other := range states {
					if name != e.Task && other.SeenAttempts[s.Prepared.Attempt] {
						return nil, 0, errors.New("journal reuses attempt across tasks")
					}
				}
			}
			if operation == "observe" {
				checks++
			}
			pending = ""
			operation = ""
		case "error":
			if pending != e.Task || operation != e.Operation || len(e.Data) != 0 || e.Error == "" {
				return nil, 0, errors.New("graph error missing matching intent")
			}
			failed = true
			pending, operation = "", ""
		default:
			return nil, 0, errors.New("unknown graph journal phase")
		}
	}
	if failed && !observeOnly {
		return nil, 0, errors.New("previous graph operation failed; explicit reconciliation required")
	}
	if pending != "" && !observeOnly {
		return nil, 0, errors.New("unresolved graph side effect; explicit reconciliation required")
	}
	if observeOnly {
		return states, checks, nil
	}
	// A previously appended record may have survived a failed fsync. Do not act
	// from it until its durability is re-established on this invocation.
	f, err := os.OpenFile(l.path, os.O_WRONLY, 0)
	if err != nil {
		return nil, 0, err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = (journal{path: l.path}).syncDir()
	}
	if err != nil {
		return nil, 0, err
	}
	return states, checks, nil
}
