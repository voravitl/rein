package routing

// Supervisor event log (Phase B slice B3). Append-only, persisted before the
// caller acknowledges the event to Orca: a Record error means "do not ack".
// State derivation is a pure function over the recorded events, so it is
// deterministic and testable without a daemon.
//
// Rules:
//   - duplicate EventID: recorded once, second write is a no-op (dedupe)
//   - out-of-order arrival: derivation sorts by (At, EventID), arrival order
//     never decides state
//   - stale attempt: an event for an older attempt is recorded but never
//     changes the task's current wait state (stale=true)

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/run"
)

// EventKind is what produced the event. KindBudgetDenied is the only source
// of WAITING_BUDGET: error classes alone never claim a budget verdict.
type EventKind string

const (
	KindProbe        EventKind = "probe"
	KindWorker       EventKind = "worker"
	KindBudgetDenied EventKind = "budget_denied"
	KindReview       EventKind = "review"
	KindApproval     EventKind = "approval"
)

// WaitState is what a task is waiting on, with who moves it next.
type WaitState string

const (
	WaitNone                  WaitState = ""
	WaitQuota                 WaitState = "WAITING_QUOTA"
	WaitAuth                  WaitState = "WAITING_AUTH"
	WaitBudget                WaitState = "WAITING_BUDGET"
	WaitReview                WaitState = "WAITING_REVIEW"
	WaitHuman                 WaitState = "WAITING_HUMAN"
	WaitBlockedReconciliation WaitState = "BLOCKED_RECONCILIATION"
)

// Event is one supervisor-observed fact. All identity fields are required:
// an event that cannot say which run/task/attempt it belongs to is refused,
// never guessed.
type Event struct {
	EventID  string     `json:"event_id"`
	Run      string     `json:"run"`
	Task     string     `json:"task"`
	Attempt  string     `json:"attempt"`
	Source   string     `json:"source"`  // e.g. "orca/1.4.224", "rein-probe"
	Version  string     `json:"version"` // source version, "" when unknown
	Pool     string     `json:"pool,omitempty"`
	Dispatch string     `json:"dispatch,omitempty"`
	Pid      *int       `json:"pid,omitempty"`
	PidStart *int64     `json:"pid_start,omitempty"`
	At       time.Time  `json:"at"`
	Kind     EventKind  `json:"kind"`
	Class    ErrorClass `json:"class"`
	Evidence string     `json:"evidence,omitempty"`
}

// TaskWait is the derived wait state of one task.
type TaskWait struct {
	State     WaitState `json:"state"`
	Reason    string    `json:"reason"`
	NextActor string    `json:"next_actor"`
	Since     time.Time `json:"since"`
	EventID   string    `json:"event_id"`
}

var syncEventFile = (*os.File).Sync

func eventLogPath() string { return filepath.Join(dir(), "events.jsonl") }

// Record persists one event. It returns (duplicate=true, nil) when EventID was
// already recorded. Redeliveries still need acknowledgement: a previous caller
// may have crashed after persistence and before acknowledgement.
func Record(ev Event) (duplicate bool, err error) {
	if err := validateEvent(ev); err != nil {
		return false, err
	}
	unlock, err := run.LockFile(eventLogPath()+".lock", 10*time.Second)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return false, err
	}
	events, err := ReadEvents()
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.EventID == ev.EventID {
			if e.Run != ev.Run || e.Task != ev.Task || e.Attempt != ev.Attempt ||
				e.Source != ev.Source || e.Dispatch != ev.Dispatch || e.Kind != ev.Kind ||
				e.Class != ev.Class || e.Pool != ev.Pool || e.Evidence != ev.Evidence {
				return false, fmt.Errorf("event identity collision: %s", ev.EventID)
			}
			// A prior append may have failed Sync before its delivery was ACKed.
			f, err := os.OpenFile(eventLogPath(), os.O_WRONLY, 0)
			if err != nil {
				return false, err
			}
			if err := syncAndCloseEventLog(f); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return false, fmt.Errorf("not recorded: %w", err)
	}
	f, err := os.OpenFile(eventLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return false, err
	}
	return false, syncAndCloseEventLog(f)
}

func syncAndCloseEventLog(f *os.File) error {
	if err := syncEventFile(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// File sync alone does not persist a newly created journal's directory entry.
	d, err := os.Open(dir())
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func validateEvent(ev Event) error {
	for _, identity := range []string{ev.EventID, ev.Run, ev.Task, ev.Attempt, ev.Source, string(ev.Kind)} {
		if strings.TrimSpace(identity) == "" {
			return errors.New("event needs event_id, run, task, attempt, source, at and kind")
		}
	}
	if ev.At.IsZero() {
		return errors.New("event needs observed time")
	}
	for _, identity := range []string{ev.Task, ev.Attempt} {
		if identity == "." || identity == ".." || filepath.Base(identity) != identity {
			return errors.New("invalid task or attempt in event")
		}
	}
	return nil
}

// ReadEvents refuses unreadable or corrupt history so recovery cannot infer an
// empty, healthy journal after a partial write or storage failure.
func ReadEvents() ([]Event, error) {
	b, err := os.ReadFile(eventLogPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read event log: %w", err)
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return nil, errors.New("event log has an incomplete final record")
	}
	var out []Event
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("event log line %d: %w", i+1, err)
		}
		if err := validateEvent(e); err != nil {
			return nil, fmt.Errorf("event log line %d: %w", i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// DeriveWait folds a task's events into its current wait state. currentAttempt
// is the task's live attempt: events from older attempts are recorded history
// and never move the state. A task with no events for its current attempt has
// no wait (it is running or not started).
func DeriveWait(task, currentAttempt string, events []Event) TaskWait {
	var mine []Event
	for _, e := range events {
		if e.Task == task && e.Attempt == currentAttempt {
			mine = append(mine, e)
		}
	}
	if len(mine) == 0 {
		return TaskWait{}
	}
	sort.Slice(mine, func(i, j int) bool {
		if mine[i].At.Equal(mine[j].At) {
			return mine[i].EventID < mine[j].EventID
		}
		return mine[i].At.Before(mine[j].At)
	})
	last := mine[len(mine)-1]
	terminal := map[ErrorClass]bool{ClassWorkerDone: true}
	if terminal[last.Class] && last.Kind != KindBudgetDenied {
		return TaskWait{}
	}
	wait := TaskWait{Since: last.At, EventID: last.EventID}
	switch {
	case last.Kind == KindBudgetDenied:
		wait.State, wait.Reason, wait.NextActor = WaitBudget, "budget gate denied: "+last.Evidence, "owner"
	case last.Class == ClassQuotaExhausted:
		wait.State, wait.Reason, wait.NextActor = WaitQuota, "pool exhausted: "+last.Pool, "supervisor"
	case last.Class == ClassAuthRequired:
		wait.State, wait.Reason, wait.NextActor = WaitAuth, "credentials required", "owner"
	case last.Class == ClassApprovalRequired:
		wait.State, wait.Reason, wait.NextActor = WaitHuman, "worker parked on a question only a human can answer", "owner"
	case last.Kind == KindReview:
		wait.State, wait.Reason, wait.NextActor = WaitReview, "required reviewer unavailable", "supervisor"
	case last.Class == ClassRateLimit || last.Class == ClassTransientError:
		// retryable by the supervisor itself: not a wait, still running.
		return TaskWait{}
	default:
		wait.State, wait.Reason, wait.NextActor = WaitBlockedReconciliation, "unclassified failure, liveness not proven", "supervisor"
	}
	return wait
}
