package routing

import (
	"path/filepath"
	"testing"
	"time"
)

func eventFixture(run, task, attempt, id string, at time.Time, kind EventKind, class ErrorClass) Event {
	return Event{EventID: id, Run: run, Task: task, Attempt: attempt, Source: "test", At: at, Kind: kind, Class: class}
}

func TestRecordRefusesIdentitylessEvents(t *testing.T) {
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	base := Event{EventID: "e1", Run: "r", Task: "t", Attempt: "a", Source: "s", At: time.Now(), Kind: KindProbe, Class: ClassUnknownError}
	for name, mutate := range map[string]func(*Event){
		"no event id":    func(e *Event) { e.EventID = "" },
		"no run":         func(e *Event) { e.Run = "" },
		"no task":        func(e *Event) { e.Task = "" },
		"no attempt":     func(e *Event) { e.Attempt = "" },
		"no source":      func(e *Event) { e.Source = "" },
		"no time":        func(e *Event) { e.At = time.Time{} },
		"no kind":        func(e *Event) { e.Kind = "" },
		"bad task":       func(e *Event) { e.Task = "../x" },
		"dot task":       func(e *Event) { e.Task = "." },
		"parent attempt": func(e *Event) { e.Attempt = ".." },
		"blank source":   func(e *Event) { e.Source = "  " },
	} {
		t.Run(name, func(t *testing.T) {
			e := base
			mutate(&e)
			if _, err := Record(e); err == nil {
				t.Errorf("Record accepted %+v", e)
			}
		})
	}
}

func TestRecordDedupes(t *testing.T) {
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	e := eventFixture("r", "t", "a", "dup1", time.Now(), KindProbe, ClassQuotaExhausted)
	if dup, err := Record(e); dup || err != nil {
		t.Fatalf("first Record = dup=%v err=%v", dup, err)
	}
	if dup, err := Record(e); !dup || err != nil {
		t.Fatalf("second Record = dup=%v err=%v, want dedupe", dup, err)
	}
	if n := len(mustReadEvents(t)); n != 1 {
		t.Fatalf("recorded %d events, want 1", n)
	}
}

func TestDeriveWait(t *testing.T) {
	now := time.Now()
	at := func(s int) time.Time { return now.Add(time.Duration(s) * time.Second) }
	tests := []struct {
		name    string
		task    string
		attempt string
		events  []Event
		want    WaitState
		actor   string
	}{
		{"no events means running", "t", "a", nil, WaitNone, ""},
		{"quota waits with supervisor next", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindProbe, ClassQuotaExhausted)}, WaitQuota, "supervisor"},
		{"auth waits for owner", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindProbe, ClassAuthRequired)}, WaitAuth, "owner"},
		{"budget denial is the only budget source", "t", "a",
			[]Event{{EventID: "e1", Run: "r", Task: "t", Attempt: "a", Source: "s", At: at(1), Kind: KindBudgetDenied, Class: ClassUnknownError, Evidence: "cap exceeded"}}, WaitBudget, "owner"},
		{"question waits for human", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindWorker, ClassApprovalRequired)}, WaitHuman, "owner"},
		{"missing reviewer waits review", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindReview, ClassUnknownError)}, WaitReview, "supervisor"},
		{"unclassified failure blocks reconciliation", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindWorker, ClassUnknownError)}, WaitBlockedReconciliation, "supervisor"},
		{"retryable classes are not waits", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindProbe, ClassTransientError)}, WaitNone, ""},
		{"rate limit is not a wait", "t", "a",
			[]Event{eventFixture("r", "t", "a", "e1", at(1), KindProbe, ClassRateLimit)}, WaitNone, ""},
		{"done clears the wait", "t", "a",
			[]Event{
				eventFixture("r", "t", "a", "e1", at(1), KindProbe, ClassQuotaExhausted),
				eventFixture("r", "t", "a", "e2", at(2), KindWorker, ClassWorkerDone),
			}, WaitNone, ""},
		{"stale attempt never moves current state", "t", "a",
			[]Event{
				eventFixture("r", "t", "old", "e1", at(2), KindProbe, ClassQuotaExhausted),
				eventFixture("r", "t", "a", "e2", at(1), KindWorker, ClassWorkerDone),
			}, WaitNone, ""},
		{"other tasks are ignored", "t", "a",
			[]Event{eventFixture("r", "other", "a", "e1", at(1), KindProbe, ClassQuotaExhausted)}, WaitNone, ""},
		{"out-of-order arrival still folds by time", "t", "a",
			[]Event{
				eventFixture("r", "t", "a", "e2", at(2), KindWorker, ClassWorkerDone),
				eventFixture("r", "t", "a", "e1", at(1), KindProbe, ClassQuotaExhausted),
			}, WaitNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveWait(tt.task, tt.attempt, tt.events)
			if got.State != tt.want || (tt.want != WaitNone && got.NextActor != tt.actor) {
				t.Errorf("DeriveWait = %+v, want state %q actor %q", got, tt.want, tt.actor)
			}
			if tt.want != WaitNone && (got.Reason == "" || got.EventID == "" || got.Since.IsZero()) {
				t.Errorf("incomplete wait: %+v", got)
			}
		})
	}
}
