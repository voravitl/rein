package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

type ActionReceipt struct {
	DaemonID       string `json:"daemon_id"`
	Version        int    `json:"version"`
	Run            string `json:"run"`
	PlanHash       string `json:"plan_hash"`
	Task           string `json:"task"`
	Attempt        string `json:"attempt"`
	ReceiptID      string `json:"receipt_id"`
	ContractHash   string `json:"contract_hash"`
	SourceRevision string `json:"source_revision"`
	SourceDigest   string `json:"source_digest"`
	InputDigest    string `json:"input_digest"`
	ArgvDigest     string `json:"argv_digest"`
	SandboxDigest  string `json:"sandbox_digest"`
}
type PreparedAttempt struct {
	Park           *Outcome            `json:"park,omitempty"`
	Number         int                 `json:"number"`
	HandoffPath    string              `json:"handoff_path,omitempty"`
	HandoffDigest  string              `json:"handoff_digest,omitempty"`
	Run            string              `json:"run"`
	PlanHash       string              `json:"plan_hash"`
	Task           string              `json:"task"`
	Kind           string              `json:"kind"`
	Attempt        string              `json:"attempt"`
	ReceiptID      string              `json:"receipt_id"`
	ContractHash   string              `json:"contract_hash"`
	Worktree       string              `json:"worktree"`
	SourceRevision string              `json:"source_revision"`
	SourceDigest   string              `json:"source_digest"`
	InputDigest    string              `json:"input_digest"`
	Inputs         []ResolvedInput     `json:"inputs"`
	Worker         *routing.AutoResult `json:"worker,omitempty"`
	Action         *ActionReceipt      `json:"action,omitempty"`
	Argv           []string            `json:"argv"`
}
type ExecutionResult struct {
	Dispatch    Dispatch        `json:"dispatch"`
	Observation Observation     `json:"observation"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
	Cancelled   bool            `json:"cancelled,omitempty"`
}
type TerminalResult struct {
	Outcome   Outcome      `json:"outcome"`
	Artifacts *ArtifactRef `json:"artifacts,omitempty"`
	Retry     bool         `json:"retry,omitempty"`
	Cancelled bool         `json:"cancelled,omitempty"`
}
type dagTaskState struct {
	SeenAttempts map[string]bool
	Stage        string
	Attempts     int
	Prepared     PreparedAttempt
	Execution    ExecutionResult
	Terminal     TerminalResult
}
type dagEntry struct {
	Seq       int             `json:"seq"`
	Task      string          `json:"task,omitempty"`
	Attempt   string          `json:"attempt,omitempty"`
	OpID      string          `json:"op_id,omitempty"`
	Phase     string          `json:"phase"`
	Operation string          `json:"operation"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}
type dagHeader struct {
	JournalVersion int    `json:"journal_version"`
	PlanSchema     int    `json:"plan_schema"`
	Run            string `json:"run"`
	PlanHash       string `json:"plan_hash"`
}
type dagLog struct {
	path string
	seq  int
}

func dagJournalPath(marker, hash string) string { return marker + ".graph3-" + hash + ".jsonl" }
func (l *dagLog) append(e dagEntry) error {
	e.Seq = l.seq + 1
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// graphLog supplies durable append without interpreting the schema-3 envelope.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(append(raw, '\n'))
	if err == nil && n != len(raw)+1 {
		err = errors.New("short DAG journal append")
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = syncArtifactDir(filepath.Dir(l.path))
	}
	if err == nil {
		l.seq++
		appendDAGTime(l.path, l.seq)
	}
	return err
}
func (s *dagTaskState) expected() string {
	switch s.Stage {
	case "", "retry":
		return "prepare"
	case "prepared":
		return "execute"
	case "executed":
		return "finalize"
	}
	return ""
}
func preparedIdentity(p Plan, t Task, a PreparedAttempt) error {
	if a.Run != p.Run || a.PlanHash != digestBytes(p.raw) || a.Task != t.Task || a.Kind != t.Kind || a.ContractHash != t.ContractHash || a.SourceRevision != t.Base || !validDigest(a.SourceDigest) || a.InputDigest != InputDigest(a.Inputs) || !validDigest(a.InputDigest) || !run.ValidName(a.Attempt) || !run.ValidName(a.ReceiptID) || !filepath.IsAbs(a.Worktree) {
		return errors.New("prepared DAG attempt identity mismatch")
	}
	if len(a.Inputs) != len(t.Inputs) {
		return errors.New("wrong consumer input count")
	}
	for i, in := range t.Inputs {
		got := a.Inputs[i]
		if got.Producer != in.Producer || got.Name != in.Name || got.As != in.As {
			return errors.New("wrong consumer binding")
		}
	}
	if t.Kind == "worker" {
		if a.Worker == nil || a.Action != nil {
			return errors.New("invalid worker union")
		}
		if a.Park != nil {
			if a.Worker.Receipt != nil || a.Worker.Attempt != a.Attempt || a.Worker.ErrorCode != routing.CodeNoCandidate || a.Worker.Error == "" || a.Park.State != routing.WaitHuman || a.Park.Reason != a.Worker.Error || a.Park.CheckpointDigest != a.SourceDigest {
				return errors.New("invalid known preparation wait")
			}
			return nil
		}
		var legacy graphTaskState
		if err := legacy.apply(t, p.Run, graphEntry{Phase: "result", Operation: "prepare", Data: mustJSON(a.Worker)}); err != nil {
			return err
		}
		d := a.Worker.Receipt
		if d.Attempt != a.Attempt || d.DecisionID != a.ReceiptID || d.Worktree != a.Worktree {
			return errors.New("worker receipt differs from DAG identity")
		}
	} else {
		r := a.Action
		if a.Park != nil || a.Worker != nil || r == nil || r.DaemonID == "" || r.Version != 1 || r.Run != a.Run || r.PlanHash != a.PlanHash || r.Task != a.Task || r.Attempt != a.Attempt || r.ReceiptID != a.ReceiptID || r.ContractHash != a.ContractHash || r.SourceRevision != a.SourceRevision || r.SourceDigest != a.SourceDigest || r.InputDigest != a.InputDigest || r.ArgvDigest != digestBytes(mustJSON(t.Action.Argv)) || r.SandboxDigest != digestBytes(mustJSON(t.Action.Sandbox)) {
			return errors.New("invalid action receipt union or digest")
		}
		if !bytes.Equal(mustJSON(a.Argv), mustJSON(t.Action.Argv)) {
			return errors.New("action argv changed")
		}
	}
	return nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (s *dagTaskState) apply(p Plan, t Task, e dagEntry) error {
	if e.Phase != "result" || e.Error != "" || e.Operation != s.expected() {
		return errors.New("invalid DAG state transition")
	}
	switch e.Operation {
	case "prepare":
		var a PreparedAttempt
		if err := strictDecode(e.Data, &a); err != nil {
			return err
		}
		if err := preparedIdentity(p, t, a); err != nil {
			return err
		}
		if s.Attempts >= t.MaxAttempts || a.Number != s.Attempts+1 || s.SeenAttempts[a.Attempt] {
			return errors.New("DAG attempt budget exhausted or number changed")
		}
		s.Attempts++
		seen := make(map[string]bool, len(s.SeenAttempts)+1)
		for id, used := range s.SeenAttempts {
			seen[id] = used
		}
		seen[a.Attempt] = true
		s.SeenAttempts = seen
		s.Prepared = a
		s.Execution = ExecutionResult{}
		s.Terminal = TerminalResult{}
		s.Stage = "prepared"
		if a.Park != nil {
			s.Stage = "wait"
			s.Terminal = TerminalResult{Outcome: *a.Park}
		}
	case "execute":
		var r ExecutionResult
		if err := strictDecode(e.Data, &r); err != nil {
			return err
		}
		d := r.Dispatch
		a := s.Prepared
		if d.Task != t.Task || d.Attempt != a.Attempt || d.Decision != a.ReceiptID || d.Dispatch != a.ReceiptID || d.Worktree != a.Worktree || d.OrcaTask == "" || !validDigest(r.Observation.Evidence) {
			return errors.New("execution identity mismatch")
		}
		switch r.Observation.State {
		case Completed, Failed, Auth, Quota:
		default:
			return errors.New("execution is not proven terminal")
		}
		if t.Kind == "action" && (d.Pool != "" || r.Observation.State == Quota || r.Observation.State == Auth) {
			return errors.New("action cannot have provider accounting")
		}
		if t.Kind == "worker" && d.Pool != a.Worker.Receipt.Pool {
			return errors.New("worker pool changed")
		}
		s.Execution = r
		s.Stage = "executed"
	case "finalize":
		var r TerminalResult
		if err := strictDecode(e.Data, &r); err != nil {
			return err
		}
		if !validDigest(r.Outcome.CheckpointDigest) || !validDigest(r.Outcome.ExecutionDigest) {
			return errors.New("terminal accounting/checkpoint evidence missing")
		}
		if r.Outcome.State == routing.WaitBlockedReconciliation {
			return errors.New("uncertain outcome is global")
		}
		switch r.Outcome.State {
		case routing.WaitNone, routing.WaitQuota, routing.WaitAuth, routing.WaitBudget, routing.WaitHuman, routing.WaitReview:
		default:
			return errors.New("unknown DAG wait state")
		}
		if r.Cancelled {
			if !s.Execution.Cancelled || r.Retry || r.Artifacts != nil || r.Outcome.State == routing.WaitNone {
				return errors.New("invalid cancelled terminal")
			}
			s.Stage = "cancelled"
		} else if r.Retry {
			if s.Execution.Cancelled || r.Artifacts != nil || s.Attempts >= t.MaxAttempts || (t.Kind == "action" && !t.Action.Idempotent) || (t.Kind == "worker" && s.Execution.Observation.State != Quota) {
				return errors.New("unauthorized retry")
			}
			s.Stage = "retry"
		} else if r.Outcome.State != routing.WaitNone {
			if r.Outcome.Reason == "" || r.Artifacts != nil {
				return errors.New("invalid branch wait")
			}
			s.Stage = "wait"
		} else {
			if s.Execution.Cancelled || s.Execution.Observation.State != Completed || !validDigest(r.Outcome.VerificationDigest) || r.Artifacts == nil {
				return errors.New("unverified DAG success")
			}
			m := r.Artifacts.Manifest
			a := s.Prepared
			if err := validateManifest(m); err != nil {
				return err
			}
			if m.Run != a.Run || m.PlanHash != a.PlanHash || m.Task != a.Task || m.Attempt != a.Attempt || m.Receipt != a.ReceiptID || m.ContractHash != a.ContractHash || m.SourceRevision != a.SourceRevision || m.SourceDigest != a.SourceDigest || m.InputDigest != a.InputDigest || m.ExecutionDigest != r.Outcome.ExecutionDigest || m.VerificationDigest != r.Outcome.VerificationDigest || m.CheckpointDigest != r.Outcome.CheckpointDigest || r.Artifacts.Digest != digestBytes(mustJSON(m)) || len(m.Outputs) != len(t.Outputs) {
				return errors.New("artifact terminal identity mismatch")
			}
			for i, o := range t.Outputs {
				if m.Outputs[i].Name != o.Name || m.Outputs[i].Path != o.Path || m.Outputs[i].Size < 0 || m.Outputs[i].Size > o.MaxBytes || !validDigest(m.Outputs[i].SHA256) {
					return errors.New("published output differs from declaration")
				}
			}
			s.Stage = "done"
		}
		s.Terminal = r
	default:
		return errors.New("unknown DAG operation")
	}
	return nil
}

func (l *dagLog) load(p Plan, hash string) (map[string]*dagTaskState, error) {
	return l.read(p, hash, false)
}
func (l *dagLog) read(p Plan, hash string, observeOnly bool) (map[string]*dagTaskState, error) {
	states := map[string]*dagTaskState{}
	tasks := map[string]Task{}
	for _, t := range p.Tasks {
		states[t.Task] = &dagTaskState{}
		tasks[t.Task] = t
	}
	raw, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		if observeOnly {
			return nil, err
		}
		data := mustJSON(dagHeader{1, 3, p.Run, hash})
		err = l.append(dagEntry{Phase: "header", Operation: "bind", Data: data})
		return states, err
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, errors.New("empty or truncated DAG journal")
	}
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
		l.seq = e.Seq
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
	if observeOnly {
		return states, nil
	}
	if failed || len(pending) > 0 {
		return nil, errors.New("DAG has failed or unresolved side effects; explicit reconciliation required")
	}
	for _, s := range states {
		if s.Stage == "prepared" || s.Stage == "executed" {
			return nil, errors.New("interrupted DAG attempt requires reconciliation")
		}
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	err = f.Sync()
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = syncArtifactDir(filepath.Dir(l.path))
	}
	return states, err
}
