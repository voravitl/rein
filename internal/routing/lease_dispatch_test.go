package routing

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLeaseDispatchIdentityPersistsAndCannotChange(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	r := mustPrepare(t, w)
	l, err := AcquireLease(w.c, r.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := l.MarkDispatchPending(); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][2]string{{"", "dispatch"}, {"task", ""}, {" task", "dispatch"}} {
		if err := l.MarkDispatchIdentity(ids[0], ids[1]); err == nil {
			t.Fatalf("invalid IDs accepted: %v", ids)
		}
	}
	if err := l.MarkDispatchIdentity("orca-task", "dispatch-1"); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatchIdentity("orca-task", "dispatch-1"); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatchIdentity("orca-task", "dispatch-2"); err == nil {
		t.Fatal("changed dispatch accepted")
	}
	b, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Lease
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.OrcaTask != "orca-task" || saved.Dispatch != "dispatch-1" || !saved.Dispatched || saved.DispatchPending || saved.Child != 0 {
		t.Fatalf("bad persisted identity: %+v", saved)
	}
	l.Release()
	if _, err := os.Stat(l.path); err != nil {
		t.Fatal("dispatched lease released", err)
	}
	changed := *l
	changed.Attempt = "replacement"
	if err := changed.MarkDispatchIdentity("orca-task", "dispatch-1"); err == nil {
		t.Fatal("stale lease wrote identity")
	}
}
