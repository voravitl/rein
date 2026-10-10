package routing

// Same-task exclusive execution. A launch lease keeps one live writer per checkout: it records who holds the task by process
// identity (pid + start time) and is replaced only once the previous holder, launcher AND provider process, is PROVEN gone.
// A crash or an unreadable record never releases it by guesswork. A worker that Orca dispatches outlives the launcher, so its
// lease has no process to prove gone: it is held until `rein route settle` ends the attempt.
//
// A receipt is also single-use: ConsumeReceipt records that an attempt was used to execute, so a retry is a new decision with a
// new refresh and a new reservation, and a crashed execution cannot be replaced by a second one under the same attempt.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// Lease is the record of the process that currently owns a task's checkout.
type Lease struct {
	Task       string    `json:"task"`
	Attempt    string    `json:"attempt"`
	Decision   string    `json:"decision"`
	Pid        int       `json:"pid"`
	PidStart   int64     `json:"pid_start"`
	Child      int       `json:"child,omitempty"`
	ChildStart int64     `json:"child_start,omitempty"`
	Dispatched bool      `json:"dispatched,omitempty"` // an Orca worker is running that no process of ours can vouch for
	At         time.Time `json:"at"`
	path       string
}

func leasePath(task string) string { return filepath.Join(dir(), "leases", task+".json") }

// holderBlocks returns why a recorded holder still blocks the task ("" = provably gone). Unknown liveness blocks.
func holderBlocks(l Lease) string {
	if l.Dispatched {
		return fmt.Sprintf("the Orca worker dispatched for attempt %s has no recorded end: run `rein route settle --attempt %s` once it has finished", l.Attempt, l.Attempt)
	}
	for _, h := range []struct {
		pid   int
		start int64
		what  string
	}{{l.Pid, l.PidStart, "launcher"}, {l.Child, l.ChildStart, "provider process"}} {
		if h.pid <= 0 {
			continue
		}
		start, err := run.ProcStart(h.pid)
		switch {
		case errors.Is(err, run.ErrNoProc):
			continue
		case err != nil:
			return fmt.Sprintf("%s pid %d: liveness cannot be determined (%v)", h.what, h.pid, err)
		case h.start != 0 && start != 0 && start != h.start:
			continue // the pid now belongs to another process
		default:
			return fmt.Sprintf("%s pid %d is still running", h.what, h.pid)
		}
	}
	return ""
}

func writeLease(l *Lease) error {
	return atomic(l.path, l)
}

// lockTaskDecision serializes preparation/replacement and the final receipt validation/consumption.
func lockTaskDecision(task string, timeout time.Duration) (func(), error) {
	return run.LockFile(filepath.Join(dir(), "prepare", task+".lock"), timeout)
}

func validateCurrentReceipt(c *contract.Contract, d *Decision) error {
	current, err := Validate(c, d.Run, d.Agent, d.Model)
	if err != nil {
		return err
	}
	if current.Attempt != d.Attempt || current.DecisionID != d.DecisionID || current.Hold != d.Hold {
		return errors.New("routing receipt replaced since validation; prepare again")
	}
	return nil
}

// ConsumeReview validates and consumes an automatic review under the same lock as preparation.
func ConsumeReview(c *contract.Contract, d *Decision) error {
	unlock, err := lockTaskDecision(c.Name, 2*time.Second)
	if err != nil {
		return fmt.Errorf("task decision: %w", err)
	}
	defer unlock()
	if err := validateCurrentReceipt(c, d); err != nil {
		return err
	}
	return ConsumeReceipt(d)
}

// AcquireLease takes the task for the calling process or says why it cannot.
func AcquireLease(c *contract.Contract, d *Decision) (*Lease, error) {
	decisionUnlock, err := lockTaskDecision(c.Name, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("task decision: %w", err)
	}
	defer decisionUnlock()
	p := leasePath(c.Name)
	unlock, err := run.LockFile(p+".lock", 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("task lease: %w", err)
	}
	defer unlock()
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		var old Lease
		if json.Unmarshal(b, &old) != nil {
			return nil, fmt.Errorf("task %s has an unreadable lease record %s: not guessing whether a writer is alive", c.Name, p)
		}
		if why := holderBlocks(old); why != "" {
			return nil, fmt.Errorf("task %s already has a writer (attempt %s): %s", c.Name, old.Attempt, why)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if d.DecisionID != "" {
		if err := validateCurrentReceipt(c, d); err != nil {
			return nil, err
		}
	}
	if err := ConsumeReceipt(d); err != nil {
		return nil, err
	}
	me := os.Getpid()
	start, _ := run.ProcStart(me)
	l := &Lease{Task: c.Name, Attempt: d.Attempt, Decision: d.DecisionID, Pid: me, PidStart: start, At: time.Now().UTC(), path: p}
	return l, writeLease(l)
}

func usedPath(attempt string) string { return filepath.Join(dir(), "used", attempt) }

// ConsumeReceipt records that the receipt's attempt is being used to execute and refuses a second use. Receipts of ordered
// chains (no attempt) keep their reusable semantics.
// ponytail: markers accumulate in routes/used/ (one empty file per attempt); prune old ones when that matters.
func ConsumeReceipt(d *Decision) error {
	if d.Attempt == "" {
		return nil
	}
	if filepath.Base(d.Attempt) != d.Attempt || d.Attempt == "." || d.Attempt == ".." {
		return errors.New("invalid attempt ID in the routing receipt")
	}
	p := usedPath(d.Attempt)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("this routing receipt (attempt %s) was already used for an execution: prepare again, because a retry is a new decision with a new refresh and a new reservation", d.Attempt)
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// ReceiptUsed reports whether an attempt's receipt was used to execute. A marker that cannot be read counts as used.
func ReceiptUsed(attempt string) bool {
	if filepath.Base(attempt) != attempt {
		return true
	}
	_, err := os.Stat(usedPath(attempt))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// MarkDispatched keeps the task held after an Orca worker-start returned: the worker runs on, and only `rein route settle` ends it.
func (l *Lease) MarkDispatched() error {
	l.Child, l.ChildStart, l.Dispatched = 0, 0, true
	return writeLease(l)
}

// EndLease ends the lease of an attempt that is being settled. A worker that is still running keeps it, and the settlement is refused.
func EndLease(task, attempt string) error {
	if task == "" || filepath.Base(task) != task {
		return nil
	}
	p := leasePath(task)
	unlock, err := run.LockFile(p+".lock", 2*time.Second)
	if err != nil {
		return fmt.Errorf("task lease: %w", err)
	}
	defer unlock()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var l Lease
	if json.Unmarshal(b, &l) != nil {
		return fmt.Errorf("task %s has an unreadable lease record %s: not guessing whether a writer is alive", task, p)
	}
	if l.Attempt != attempt {
		return nil // another attempt's lease
	}
	if !l.Dispatched {
		if why := holderBlocks(l); why != "" {
			return fmt.Errorf("attempt %s is still running: %s", attempt, why)
		}
	}
	return os.Remove(p)
}

// TaskBusy says why a task's checkout still has a writer that cannot be proven gone ("" = none).
func TaskBusy(task string) string {
	if filepath.Base(task) != task {
		return "invalid task name"
	}
	b, err := os.ReadFile(leasePath(task))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ""
	case err != nil:
		return err.Error()
	}
	var l Lease
	if json.Unmarshal(b, &l) != nil {
		return "unreadable lease record: not guessing whether a writer is alive"
	}
	return holderBlocks(l)
}

// SetChild records the provider process so a crashed launcher cannot hide a still-running writer.
func (l *Lease) SetChild(pid int) error {
	l.Child = pid
	l.ChildStart, _ = run.ProcStart(pid)
	return writeLease(l)
}

// Release gives the task back, but only if this process still holds it. A dispatched lease is kept until the attempt is settled.
func (l *Lease) Release() {
	if l.Dispatched {
		return
	}
	if b, err := os.ReadFile(l.path); err == nil {
		var cur Lease
		if json.Unmarshal(b, &cur) == nil && cur.Pid == l.Pid && cur.PidStart == l.PidStart {
			_ = os.Remove(l.path)
		}
	}
}
