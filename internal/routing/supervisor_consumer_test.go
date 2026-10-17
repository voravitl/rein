package routing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func supervisorTestBinding(t *testing.T) SupervisorBinding {
	t.Helper()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	return SupervisorBinding{
		Enabled: true, Run: "run", Task: "task", Attempt: "attempt", CurrentAttempt: "attempt",
		OrcaTask: "orca-task", Dispatch: "dispatch", Source: "orca-fixture", Version: "test",
		ObservedAt: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC),
	}
}

func supervisorBatch(id, messageType, dispatch string, encoded bool) []byte {
	payload, _ := json.Marshal(map[string]string{"taskId": "orca-task", "dispatchId": dispatch, "outcome": messageType})
	var value any = json.RawMessage(payload)
	if encoded {
		value = string(payload)
	}
	raw, _ := json.Marshal(map[string]any{"result": map[string]any{
		"deliveryId": "delivery", "messages": []any{map[string]any{"id": id, "type": messageType, "payload": value}},
	}})
	return raw
}

func TestSupervisorConsumerDurableBeforeAckAndRedelivery(t *testing.T) {
	binding := supervisorTestBinding(t)
	calls := 0
	ackFailure := errors.New("disconnected")
	ack := func(delivery string) error {
		calls++
		if delivery != "delivery" {
			t.Fatalf("delivery = %q", delivery)
		}
		events, err := ReadEvents()
		if err != nil || len(events) != 1 || events[0].Dispatch != binding.Dispatch {
			t.Fatalf("ACK preceded durable event: events=%+v err=%v", events, err)
		}
		if calls == 1 {
			return ackFailure
		}
		return nil
	}
	for i, encoded := range []bool{false, true} {
		binding.ObservedAt = binding.ObservedAt.Add(time.Minute)
		wait, err := ConsumeSupervisorBatch(binding, supervisorBatch("message", "question", "dispatch", encoded), ack)
		if i == 0 && !errors.Is(err, ackFailure) || i == 1 && err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
		if wait.State != WaitHuman {
			t.Fatalf("wait=%+v", wait)
		}
	}
	if calls != 2 {
		t.Fatalf("ACK calls=%d, need retry after durable duplicate", calls)
	}
}

func TestSupervisorConsumerRejectsWholeInvalidBatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed", "{"},
		{"no-envelope", `{"deliveryId":"delivery","messages":[]}`},
		{"error", `{"error":{"code":"waiter_exists"}}`},
		{"explicit-refusal", strings.Replace(string(supervisorBatch("m", "question", "dispatch", false)), `{"result":`, `{"ok":false,"result":`, 1)},
		{"empty", `{"result":{"deliveryId":"delivery","messages":[]}}`},
		{"bad-payload", `{"result":{"deliveryId":"delivery","messages":[{"id":"m","type":"question","payload":"{bad"}]}}`},
		{"missing-identity", `{"result":{"deliveryId":"delivery","messages":[{"id":"m","type":"question","payload":{}}]}}`},
		{"wrong-dispatch", string(supervisorBatch("m", "question", "foreign", false))},
		{"wrong-task", strings.ReplaceAll(string(supervisorBatch("m", "question", "dispatch", false)), "orca-task", "foreign")},
		{"unknown-type", string(supervisorBatch("m", "unsupported", "dispatch", false))},
		{"mixed", `{"result":{"deliveryId":"delivery","messages":[{"id":"m1","type":"question","payload":{"taskId":"orca-task","dispatchId":"dispatch"}},{"id":"m2","type":"question","payload":{"taskId":"foreign","dispatchId":"dispatch"}}]}}`},
		{"duplicate-ids", `{"result":{"deliveryId":"delivery","messages":[{"id":"m","type":"question","payload":{"taskId":"orca-task","dispatchId":"dispatch"}},{"id":"m","type":"question","payload":{"taskId":"orca-task","dispatchId":"dispatch"}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := supervisorTestBinding(t)
			_, err := ConsumeSupervisorBatch(binding, []byte(tc.raw), func(string) error { t.Fatal("invalid batch ACKed"); return nil })
			if err == nil {
				t.Fatal("invalid batch accepted")
			}
			if _, err := os.Stat(eventLogPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid batch wrote journal: %v", err)
			}
		})
	}
}

func TestSupervisorConsumerPersistenceFailureNeverAcks(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "unwritable", true: "corrupt"}[corrupt], func(t *testing.T) {
			binding := supervisorTestBinding(t)
			if err := os.MkdirAll(dir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if err := os.WriteFile(eventLogPath(), []byte("{bad\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(eventLogPath(), 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := ConsumeSupervisorBatch(binding, supervisorBatch("m", "question", "dispatch", false), func(string) error { t.Fatal("failed persistence ACKed"); return nil })
			if err == nil {
				t.Fatal("persistence failure accepted")
			}
		})
	}
}

func TestSupervisorConsumerStaleAttemptAndOtherRunStayIsolated(t *testing.T) {
	binding := supervisorTestBinding(t)
	foreign := Event{EventID: "foreign", Run: "other-run", Task: binding.Task, Attempt: binding.CurrentAttempt,
		Source: binding.Source, At: binding.ObservedAt.Add(time.Hour), Kind: KindWorker, Class: ClassWorkerDone}
	if _, err := Record(foreign); err != nil {
		t.Fatal(err)
	}
	ack := func(string) error { return nil }
	if wait, err := ConsumeSupervisorBatch(binding, supervisorBatch("question", "question", "dispatch", false), ack); err != nil || wait.State != WaitHuman {
		t.Fatalf("other run moved state: wait=%+v err=%v", wait, err)
	}
	binding.Attempt = "old-attempt"
	binding.ObservedAt = binding.ObservedAt.Add(2 * time.Hour)
	wait, err := ConsumeSupervisorBatch(binding, supervisorBatch("stale-done", "worker_done", "dispatch", false), ack)
	if err != nil || wait.State != WaitHuman {
		t.Fatalf("stale event moved state: wait=%+v err=%v", wait, err)
	}
}

func TestSupervisorConsumerRequiresExplicitOptInAndBinding(t *testing.T) {
	for _, mode := range []string{"disabled", "no-current-attempt", "no-time", "no-ack"} {
		t.Run(mode, func(t *testing.T) {
			binding := supervisorTestBinding(t)
			ack := func(string) error { t.Fatal("invalid binding ACKed"); return nil }
			switch mode {
			case "disabled":
				binding.Enabled = false
			case "no-current-attempt":
				binding.CurrentAttempt = ""
			case "no-time":
				binding.ObservedAt = time.Time{}
			case "no-ack":
				ack = nil
			}
			if _, err := ConsumeSupervisorBatch(binding, supervisorBatch("m", "question", "dispatch", false), ack); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestSupervisorConsumerAmbiguousCompletionBlocks(t *testing.T) {
	for _, outcome := range []string{"failed", ""} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			binding := supervisorTestBinding(t)
			raw := strings.Replace(string(supervisorBatch("m", "worker_done", "dispatch", false)), `"outcome":"worker_done"`, `"outcome":"`+outcome+`"`, 1)
			wait, err := ConsumeSupervisorBatch(binding, []byte(raw), func(string) error { return nil })
			if err != nil || wait.State != WaitBlockedReconciliation {
				t.Fatalf("ambiguous completion treated as clean: wait=%+v err=%v", wait, err)
			}
		})
	}
}

func TestSupervisorConsumerConflictingTransitionsRefusedBeforeWrite(t *testing.T) {
	for _, ids := range [][2]string{{"a", "z"}, {"z", "a"}} {
		t.Run(strings.Join(ids[:], "-"), func(t *testing.T) {
			binding := supervisorTestBinding(t)
			raw, err := json.Marshal(map[string]any{"result": map[string]any{
				"deliveryId": "delivery", "messages": []any{
					map[string]any{"id": ids[0], "type": "question", "payload": map[string]string{"taskId": "orca-task", "dispatchId": "dispatch", "outcome": "question"}},
					map[string]any{"id": ids[1], "type": "worker_done", "payload": map[string]string{"taskId": "orca-task", "dispatchId": "dispatch", "outcome": "worker_done"}},
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			acked := false
			_, err = ConsumeSupervisorBatch(binding, raw, func(string) error { acked = true; return nil })
			if err == nil || acked {
				t.Fatalf("unordered conflicting transitions accepted: err=%v ACK=%v", err, acked)
			}
			if _, err := os.Stat(eventLogPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("conflicting batch wrote journal: %v", err)
			}
		})
	}
}

func TestSupervisorConsumerActualOrcaOutcomes(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			binding := supervisorTestBinding(t)
			raw := strings.Replace(string(supervisorBatch("m", "worker_done", "dispatch", false)), `"outcome":"worker_done"`, `"outcome":"`+outcome+`"`, 1)
			acked := false
			wait, err := ConsumeSupervisorBatch(binding, []byte(raw), func(string) error { acked = true; return nil })
			if err != nil || !acked {
				t.Fatalf("outcome not durably consumed: %+v %v", wait, err)
			}
			events, e := ReadEvents()
			if e != nil || len(events) != 1 || !strings.Contains(events[0].Evidence, "outcome="+outcome) {
				t.Fatalf("lost observed outcome: %+v %v", events, e)
			}
			if outcome == "succeeded" && wait.State != WaitNone {
				t.Fatalf("actual success blocked: %+v", wait)
			}
			if outcome == "failed" && wait.State != WaitBlockedReconciliation {
				t.Fatalf("failed completion accepted: %+v", wait)
			}
			if events[0].Pid != nil || events[0].PidStart != nil {
				t.Fatal("event invented worker death proof")
			}
		})
	}
}
