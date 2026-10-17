package routing

import (
	"os"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
)

func TestSettlementCannotReleaseUnknownRemoteWorker(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatched", true: "pending"}[pending], func(t *testing.T) {
			w := newWorld(t)
			w.startRun()
			r := mustPrepare(t, w)
			l, err := AcquireLease(w.c, r.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			l.Pid, l.PidStart = 0, 0
			l.DispatchPending, l.Dispatched = pending, !pending
			l.OrcaTask, l.Dispatch = "orca-task", "dispatch"
			if err := writeLease(l); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(l.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Record(Event{EventID: "done", Run: "run", Task: "task", Attempt: r.Attempt, Source: "orca", Dispatch: l.Dispatch, At: time.Now(), Kind: KindWorker, Class: ClassifyWorkerExit(0, "worker_done")}); err != nil {
				t.Fatal(err)
			}
			if err := EndLease("task", r.Attempt); err == nil {
				t.Fatal("remote lease released without remote death proof")
			}
			if _, err := SettleAttempt(r.Attempt); err == nil {
				t.Fatal("remote worker_done was accepted as death proof")
			}
			after, err := os.ReadFile(l.path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("lease changed on refused settlement: %v", err)
			}
			holds, err := budget.HoldsFor(r.Attempt)
			if err != nil || len(holds) != 1 || holds[0].State != "reserved" {
				t.Fatalf("uncertain worker released budget: %+v, %v", holds, err)
			}
			if _, err := budget.Settle(holds[0].ID, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := SettleAttempt(r.Attempt); err == nil {
				t.Fatal("previously settled accounting bypassed remote lease protection")
			}
		})
	}
}
