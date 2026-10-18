package routing

// Same-task exclusive execution. A launch lease keeps one live writer per checkout: it records who holds the task by process
// identity (pid + start time) and is replaced only once the previous holder, launcher AND provider process, is PROVEN gone.
// A crash or an unreadable record never releases it by guesswork. A worker that Orca dispatches outlives the launcher, so its
// lease has no process to prove gone: public settlement cannot release it without remote liveness proof.
//
// A receipt is also single-use: ConsumeReceipt records that an attempt was used to execute, so a retry is a new decision with a
// new refresh and a new reservation, and a crashed execution cannot be replaced by a second one under the same attempt.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// Lease is the record of the process that currently owns a task's checkout.
type Lease struct {
	Task              string    `json:"task"`
	Attempt           string    `json:"attempt"`
	Decision          string    `json:"decision"`
	Pid               int       `json:"pid"`
	PidStart          int64     `json:"pid_start"`
	Child             int       `json:"child,omitempty"`
	ChildStart        int64     `json:"child_start,omitempty"`
	ChildStartUnknown bool      `json:"child_start_unknown,omitempty"`
	ChildGroup        int       `json:"child_group,omitempty"`
	DispatchPending   bool      `json:"dispatch_pending,omitempty"` // invocation may have started a remote worker; local handoff is unfinished
	OrcaTask          string    `json:"orca_task,omitempty"`
	Dispatch          string    `json:"dispatch,omitempty"`
	Dispatched        bool      `json:"dispatched,omitempty"` // an Orca invocation may have started a worker whose exit no local process can prove
	At                time.Time `json:"at"`
	path              string
}

// procStartOf is a test seam over run.ProcStart: the birth-time read races an instant-exiting child inside
// SetChildGroup, and the tests must be able to hold that window open deterministically.
var procStartOf = run.ProcStart

func leasePath(task string) string { return filepath.Join(dir(), "leases", task+".json") }

// holderBlocks returns why a recorded holder still blocks the task ("" = provably gone). Unknown liveness blocks.
func holderBlocks(l Lease) string {
	if l.Dispatched || l.OrcaTask != "" || l.Dispatch != "" {
		return fmt.Sprintf("the Orca worker dispatched for attempt %s has no recorded end with proven remote termination; settlement is blocked", l.Attempt)
	}
	for _, h := range []struct {
		pid   int
		start int64
		what  string
	}{{l.Pid, l.PidStart, "launcher"}, {l.Child, l.ChildStart, "provider process"}} {
		if h.pid <= 0 {
			continue
		}
		start, err := procStartOf(h.pid)
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
	if l.ChildGroup != 0 {
		if l.ChildGroup != l.Child || l.Child <= 0 {
			return "local provider process group identity is invalid"
		}
		if err := leaseGroupGone(l.ChildGroup); err != nil {
			return err.Error()
		}
	}
	if l.DispatchPending {
		return fmt.Sprintf("the Orca dispatch for attempt %s has no recorded end with proven remote termination; settlement is blocked", l.Attempt)
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

// MarkDispatchPending fences an uncertain remote invocation before any process can start.
func (l *Lease) MarkDispatchPending() error {
	l.DispatchPending = true
	return writeLease(l)
}

// MarkDispatched keeps the task held after worker-start returns; local process exit cannot prove remote termination.
func (l *Lease) MarkDispatched() error {
	l.Child, l.ChildStart, l.Dispatched, l.DispatchPending = 0, 0, true, false
	return writeLease(l)
}

// MarkDispatchIdentity records IDs proven by a successful worker-start receipt.
func (l *Lease) MarkDispatchIdentity(taskID, dispatchID string) error {
	if taskID == "" || dispatchID == "" || strings.TrimSpace(taskID) != taskID || strings.TrimSpace(dispatchID) != dispatchID {
		return errors.New("worker-start task and dispatch IDs are required")
	}
	unlock, err := run.LockFile(l.path+".lock", 2*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	var current Lease
	if json.Unmarshal(b, &current) != nil || current.Task != l.Task || current.Attempt != l.Attempt || current.Decision != l.Decision || current.Pid != l.Pid || current.PidStart != l.PidStart {
		return errors.New("task lease changed before dispatch identity was recorded")
	}
	if (current.OrcaTask != "" && current.OrcaTask != taskID) || (current.Dispatch != "" && current.Dispatch != dispatchID) {
		return errors.New("worker-start dispatch identity cannot change")
	}
	next := *l
	next.OrcaTask, next.Dispatch = taskID, dispatchID
	next.Child, next.ChildStart, next.Dispatched, next.DispatchPending = 0, 0, true, false
	if err := writeLease(&next); err != nil {
		return err
	}
	*l = next
	return nil
}

// EndLease ends a lease only when every recorded local process is gone and no remote worker may remain.
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
	if why := holderBlocks(l); why != "" {
		return fmt.Errorf("attempt %s is still running or its termination is unproven: %s", attempt, why)
	}
	return os.Remove(p)
}

func (l *Lease) ownedLocal() (*Lease, error) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err
	}
	var current Lease
	if err := json.Unmarshal(b, &current); err != nil {
		return nil, err
	}
	start, err := run.ProcStart(os.Getpid())
	if err != nil {
		return nil, err
	}
	if current.Task != l.Task || current.Attempt != l.Attempt || current.Decision != l.Decision || current.Pid != l.Pid || current.PidStart != l.PidStart || current.Pid != os.Getpid() || start == 0 || current.PidStart != start {
		return nil, errors.New("local lease identity or owner changed")
	}
	if current.Dispatched || current.DispatchPending || current.OrcaTask != "" || current.Dispatch != "" {
		return nil, errors.New("remote worker termination cannot be proven by local completion")
	}
	current.path = l.path
	return &current, nil
}

// SetChildGroup records a local provider whose dedicated process group was created by the launcher.
func (l *Lease) SetChildGroup(pid int) error {
	unlock, err := run.LockFile(l.path+".lock", 2*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := l.ownedLocal()
	if err != nil {
		return err
	}
	if current.Child != 0 || pid <= 0 {
		return errors.New("local child identity cannot be replaced")
	}
	if err := dedicatedLeaseGroup(pid); err != nil {
		return err
	}
	start, err := procStartOf(pid)
	gone := errors.Is(err, run.ErrNoProc)
	if (err != nil && !gone) || (err == nil && start == 0) {
		return fmt.Errorf("local child identity cannot be proven: pid %d: %v", pid, err)
	}
	// A child may exit between entering its own group and this birth-time read; linux then reports ErrNoProc for
	// the zombie. The fenced group makes the exit provable, so record it as a finished child without a pinned
	// start instead of failing the launch, and let the group-absence proof decide the release.
	current.Child, current.ChildGroup = pid, pid
	current.ChildStart, current.ChildStartUnknown = start, gone
	if err := writeLease(current); err != nil {
		return err
	}
	*l = *current
	return nil
}

// CompleteLocal releases only this launcher's child after its entire recorded process group is proven gone.
// shortcut: detached descendants escape group proof; require OS containment before allowing daemonizing workers.
func (l *Lease) CompleteLocal() error {
	unlock, err := run.LockFile(l.path+".lock", 2*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := l.ownedLocal()
	if err != nil {
		return err
	}
	if current.Child <= 0 || (current.ChildStart == 0 && !current.ChildStartUnknown) || current.ChildGroup != current.Child {
		return errors.New("local child process group was not recorded")
	}
	start, err := procStartOf(current.Child)
	if !errors.Is(err, run.ErrNoProc) && !(err == nil && start != 0 && start != current.ChildStart) {
		return fmt.Errorf("local child pid %d termination is unproven", current.Child)
	}
	if err := awaitLeaseGroupGone(current.ChildGroup); err != nil {
		return err
	}
	return os.Remove(l.path)
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

// Release gives the task back only for this holder after its local child is gone. Remote leases remain fenced.
func (l *Lease) Release() {
	if l.Dispatched || l.DispatchPending {
		return
	}
	unlock, err := run.LockFile(l.path+".lock", 2*time.Second)
	if err != nil {
		return
	}
	defer unlock()
	if b, err := os.ReadFile(l.path); err == nil {
		var cur Lease
		if json.Unmarshal(b, &cur) == nil && cur.Task == l.Task && cur.Pid == l.Pid && cur.PidStart == l.PidStart && cur.Attempt == l.Attempt && cur.Decision == l.Decision && !cur.Dispatched && !cur.DispatchPending {
			cur.Pid, cur.PidStart = 0, 0
			if holderBlocks(cur) != "" {
				return
			}
			_ = os.Remove(l.path)
		}
	}
}
