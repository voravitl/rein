package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

type Runner func(context.Context, string, ...string) ([]byte, error)

type Result struct {
	State  routing.WaitState `json:"state"`
	Reason string            `json:"reason"`
	Checks int               `json:"checks"`
}

func Execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	var output limitedOutput
	cmd.Stdout = &output
	err := cmd.Run()
	return output.Bytes(), err
}

type limitedOutput struct{ buffer bytes.Buffer }

func (b *limitedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.buffer.Len() {
		return 0, errors.New("Orca output exceeds 1 MiB")
	}
	return b.buffer.Write(p)
}

// Monitor consumes only approved task signals. A completion never proves process
// death or accounting reconciliation, so it always returns control to a human.
func Monitor(ctx context.Context, p Plan, hash string, runner Runner) (out Result, err error) {
	out.State = routing.WaitBlockedReconciliation
	// Revalidate caller-supplied plans; file hash is checked against the marker.
	authorized, actualHash, err := Decode(p.raw)
	if err != nil {
		return out, err
	}
	b, _ := json.Marshal(p)
	want, _ := json.Marshal(authorized)
	if p.Schema != 1 || actualHash != hash || !bytes.Equal(b, want) {
		return out, errors.New("plan changed after decoding")
	}
	controller, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		return out, err
	}
	defer controller.Close()
	m := controller.Marker()
	root, err := filepath.EvalSymlinks(p.Repo)
	if err != nil {
		return out, err
	}
	markerRoot, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return out, err
	}
	if m.Run != p.Run || root != markerRoot {
		return out, errors.New("plan run/repository does not match controller")
	}
	if runner == nil {
		runner = Execute
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
	defer cancel()
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return out, err
	}
	j := journal{path: loc.Marker + ".supervisor-" + hash + ".jsonl"}
	pending, err := j.load()
	if err != nil {
		return out, err
	}
	checkAuthority := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := controller.Check(); err != nil {
			return err
		}
		return validateTasks(p)
	}
	if err := checkAuthority(); err != nil {
		return out, err
	}
	consume := func(raw []byte) (bool, error) {
		task, empty, err := boundTask(p, raw)
		if err != nil {
			return false, err
		}
		if empty {
			return false, j.append(journalEntry{Phase: "consumed"})
		}
		if err := checkAuthority(); err != nil {
			return false, err
		}
		wait, err := routing.ConsumeSupervisorBatch(routing.SupervisorBinding{Enabled: true, Run: p.Run, Task: task.Task, Attempt: task.Attempt, CurrentAttempt: task.CurrentAttempt, OrcaTask: task.OrcaTask, Dispatch: task.Dispatch, Source: p.Source, Version: p.Version, ObservedAt: time.Now().UTC()}, raw, func(id string) error {
			if err := checkAuthority(); err != nil {
				return err
			}
			response, err := runner(ctx, "orca", "orchestration", "check", "--run", p.Run, "--terminal", p.Terminal, "--ack", id, "--peek", "--json")
			if err != nil {
				return fmt.Errorf("Orca ACK: %w", err)
			}
			return validAck(response)
		})
		if err != nil {
			return false, err
		}
		if err := j.append(journalEntry{Phase: "consumed"}); err != nil {
			return false, err
		}
		if wait.State != routing.WaitNone {
			out.State, out.Reason = wait.State, wait.Reason
		} else {
			out = human(out)
		}
		return true, nil
	}
	if pending != nil {
		done, err := consume(pending)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
	}
	for out.Checks < p.MaxChecks {
		if err := checkAuthority(); err != nil {
			return out, err
		}
		state, err := routing.Status()
		if err != nil {
			return out, fmt.Errorf("cooldowns: %w", err)
		}
		if allExhausted(p.Pools, state, time.Now()) {
			out.State = routing.WaitQuota
			out.Reason = "all configured pools exhausted; unknown reset requires explicit clear"
			return out, nil
		}
		if err := j.append(journalEntry{Phase: "check"}); err != nil {
			return out, err
		}
		if err := checkAuthority(); err != nil {
			return out, err
		}
		raw, err := runner(ctx, "orca", "orchestration", "check", "--run", p.Run, "--terminal", p.Terminal, "--json")
		out.Checks++
		if err != nil {
			return out, fmt.Errorf("Orca check outcome unknown; reconcile before retry: %w", err)
		}
		if len(raw) > 1<<20 || !json.Valid(raw) {
			return out, errors.New("Orca check returned invalid JSON; reconcile delivery")
		}
		if err := j.append(journalEntry{Phase: "delivery", Raw: raw}); err != nil {
			return out, err
		}
		done, err := consume(raw)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
		if out.Checks == p.MaxChecks {
			break
		}
		timer := time.NewTimer(time.Duration(p.PollIntervalMS) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
	out.State = routing.WaitHuman
	out.Reason = "bounded monitor exhausted; no recovery or dispatch authorized"
	return out, nil
}

func human(out Result) Result {
	out.State = routing.WaitHuman
	out.Reason = "signal persisted and acknowledged; worker-gone and accounting proof required before recovery"
	return out
}

func allExhausted(pools []string, state map[string]routing.Cooldown, now time.Time) bool {
	if len(pools) == 0 {
		return false
	}
	for _, pool := range pools {
		c, ok := state[pool]
		if !ok || (c.Until != nil && !now.Before(*c.Until)) {
			return false
		}
	}
	return true
}

func validateTasks(p Plan) error {
	repo, err := run.Locate(p.Repo)
	if err != nil {
		return err
	}
	common, err := filepath.EvalSymlinks(repo.Common)
	if err != nil {
		return err
	}
	for _, t := range p.Tasks {
		c, err := contract.Load(t.Task)
		if err != nil {
			return fmt.Errorf("contract %s: %w", t.Task, err)
		}
		if c.Name != t.Task {
			return errors.New("contract task identity mismatch")
		}
		if !filepath.IsAbs(c.Worktree) || filepath.Clean(c.Worktree) != c.Worktree {
			return errors.New("contract worktree must be absolute and clean")
		}
		worktree, err := run.Locate(c.Worktree)
		if err != nil {
			return err
		}
		worktreeCommon, err := filepath.EvalSymlinks(worktree.Common)
		if err != nil || worktreeCommon != common {
			return errors.New("contract worktree belongs to another repository")
		}
		b, err := os.ReadFile(filepath.Join(contract.IndexDir(), "routes", "decisions", t.Task+".json"))
		if err != nil {
			return fmt.Errorf("current receipt missing or unreadable for %s: %w", t.Task, err)
		}
		var d routing.Decision
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		if d.Run != p.Run || d.Task != t.Task || d.Attempt != t.CurrentAttempt || d.Worktree != c.Worktree || d.ContractHash != routing.ContractHash(c) || d.HooksGeneration != c.HooksGeneration {
			return fmt.Errorf("current receipt/contract identity mismatch for %s", t.Task)
		}
		used, err := os.Lstat(filepath.Join(contract.IndexDir(), "routes", "used", t.CurrentAttempt))
		if err != nil || !used.Mode().IsRegular() || used.Size() != 0 {
			return fmt.Errorf("receipt consumption evidence unavailable for %s", t.Task)
		}
		b, err = os.ReadFile(filepath.Join(contract.IndexDir(), "routes", "leases", t.Task+".json"))
		if err != nil {
			return fmt.Errorf("dispatch lease missing or unreadable for %s: %w", t.Task, err)
		}
		var lease struct {
			Task            string `json:"task"`
			Attempt         string `json:"attempt"`
			Decision        string `json:"decision"`
			Dispatched      bool   `json:"dispatched"`
			DispatchPending bool   `json:"dispatch_pending"`
			OrcaTask        string `json:"orca_task"`
			Dispatch        string `json:"dispatch"`
		}
		if err := json.Unmarshal(b, &lease); err != nil {
			return err
		}
		if t.Attempt != t.CurrentAttempt || d.DecisionID == "" || lease.Task != t.Task || lease.Attempt != t.Attempt || lease.Decision != d.DecisionID || !lease.Dispatched || lease.DispatchPending || lease.OrcaTask != t.OrcaTask || lease.Dispatch != t.Dispatch {
			return fmt.Errorf("exact dispatched lease evidence unavailable or stale for %s; reconciliation required", t.Task)
		}
	}
	return nil
}

// shortcut: a delivery may contain one bound dispatch only; support mixed
// deliveries once Orca provides transactional batch acknowledgement semantics.
func boundTask(p Plan, raw []byte) (Task, bool, error) {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return Task{}, false, err
	}
	var e struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Result *struct {
			DeliveryID string `json:"deliveryId"`
			Messages   []struct {
				Payload json.RawMessage `json:"payload"`
			} `json:"messages"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return Task{}, false, err
	}
	if e.OK != nil && !*e.OK {
		return Task{}, false, errors.New("Orca check explicitly refused")
	}
	if len(e.Error) > 0 && string(e.Error) != "null" {
		return Task{}, false, errors.New("Orca check error")
	}
	if e.Result == nil {
		return Task{}, false, errors.New("Orca check missing result")
	}
	if len(e.Result.Messages) == 0 {
		if e.Result.DeliveryID != "" {
			return Task{}, false, errors.New("empty delivery has an ID; cannot safely acknowledge")
		}
		return Task{}, true, nil
	}
	var selected Task
	for _, m := range e.Result.Messages {
		payload := m.Payload
		var encoded string
		if len(payload) > 0 && payload[0] == '"' {
			if err := json.Unmarshal(payload, &encoded); err != nil {
				return Task{}, false, err
			}
			payload = []byte(encoded)
		}
		var identity struct {
			TaskID     string `json:"taskId"`
			DispatchID string `json:"dispatchId"`
		}
		if err := json.Unmarshal(payload, &identity); err != nil {
			return Task{}, false, err
		}
		var matched *Task
		for i := range p.Tasks {
			t := &p.Tasks[i]
			if t.OrcaTask == identity.TaskID && t.Dispatch == identity.DispatchID {
				matched = t
				break
			}
		}
		if matched == nil {
			return Task{}, false, errors.New("unbound delivery; no ACK")
		}
		if selected.Task != "" && selected.Task != matched.Task {
			return Task{}, false, errors.New("mixed dispatch batch unsupported; no writes or ACK")
		}
		selected = *matched
	}
	return selected, false, nil
}

func validAck(raw []byte) error {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return err
	}
	var e struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("decode ACK: %w", err)
	}
	if e.OK != nil && !*e.OK {
		return errors.New("Orca ACK explicitly refused")
	}
	if len(e.Error) > 0 && string(e.Error) != "null" {
		return errors.New("Orca ACK error")
	}
	if len(e.Result) == 0 || string(e.Result) == "null" {
		return errors.New("Orca ACK missing result")
	}
	return nil
}
