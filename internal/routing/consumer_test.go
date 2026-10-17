package routing

// Supervised-mode consumer path (Phase C slice C3, fake fixtures only).
// Pins the ordering the controller boundary design requires
// (docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md §2, §5): an Orca check batch
// is classified, recorded BEFORE any ack, and only then folded into a wait
// state. A redelivery dedupes stored events; the production consumer retries ACK. A stale attempt's event is
// recorded history and never moves the live attempt's state.
//
// Nothing here touches Orca, ocloop2.py, or any production path: the batch
// is a JSON fixture shaped like ocloop2.py's printout (deliveryId +
// messages with id/type/taskId/dispatchId/outcome), decoded by a small
// test-only parser. A simulated batch is NOT a live Orca message.

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// orcaBatch mirrors the fields ocloop2.py prints per message
// (id/type/task/dispatch/outcome), plus the delivery id the waiter acks.
type orcaBatch struct {
	DeliveryID string `json:"deliveryId"`
	Messages   []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		TaskID     string `json:"taskId"`
		DispatchID string `json:"dispatchId"`
		Outcome    string `json:"outcome"`
	} `json:"messages"`
}

// parseOrcaBatch decodes one fixture batch. Unknown shapes are an error,
// never guessed: the consumer refuses to classify what it cannot read.
func parseOrcaBatch(b []byte) (orcaBatch, error) {
	var batch orcaBatch
	if err := json.Unmarshal(b, &batch); err != nil {
		return orcaBatch{}, err
	}
	if batch.DeliveryID == "" || len(batch.Messages) == 0 {
		return orcaBatch{}, errNoMessage
	}
	return batch, nil
}

var errNoMessage = errBatchEmpty()

type batchError string

func (e batchError) Error() string { return string(e) }

func errBatchEmpty() error { return batchError("orca batch has no delivery id or no messages") }

// consumeBatch is the test-only consumer: for each non-noise message it
// classifies the outcome, records the event (persist-before-ack), and
// returns the delivery id to ack plus the dedupe flags. Recording happens
// before the caller may ack: a Record error means "do not ack".
func consumeBatch(t *testing.T, task, attempt, run, source string, at time.Time, raw string) (ack string, dupes []bool) {
	t.Helper()
	batch, err := parseOrcaBatch([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range batch.Messages {
		class := ClassifyWorkerExit(0, m.Outcome)
		ev := Event{
			EventID: m.ID, Run: run, Task: task, Attempt: attempt,
			Source: source, At: at.Add(time.Duration(i) * time.Second),
			Kind: KindWorker, Class: class,
			Dispatch: m.DispatchID,
			Evidence: "outcome=" + m.Outcome,
		}
		dup, err := Record(ev)
		if err != nil {
			t.Fatalf("Record(%s): %v (do not ack)", m.ID, err)
		}
		dupes = append(dupes, dup)
	}
	return batch.DeliveryID, dupes
}

func TestConsumerRecordsBeforeAck(t *testing.T) {
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	now := time.Now()

	// 1. A worker_done batch: classify -> record -> ack id returned.
	ack, dupes := consumeBatch(t, "t", "a1", "r", "orca-test/1.0", now, `{
		"deliveryId": "dlv-1",
		"messages": [{"id": "msg-1", "type": "worker_done", "taskId": "t",
			"dispatchId": "ctx-1", "outcome": "worker_done"}]
	}`)
	if ack != "dlv-1" {
		t.Fatalf("ack = %q, want dlv-1", ack)
	}
	if len(dupes) != 1 || dupes[0] {
		t.Fatalf("dupes = %v, want one fresh record", dupes)
	}
	if w := DeriveWait("t", "a1", mustReadEvents(t)); w.State != WaitNone {
		t.Fatalf("done task waits: %+v", w)
	}

	// 2. Redelivery of the same batch dedupes stored events.
	_, dupes = consumeBatch(t, "t", "a1", "r", "orca-test/1.0", now, `{
		"deliveryId": "dlv-1",
		"messages": [{"id": "msg-1", "type": "worker_done", "taskId": "t",
			"dispatchId": "ctx-1", "outcome": "worker_done"}]
	}`)
	if len(dupes) != 1 || !dupes[0] {
		t.Fatalf("redelivery dupes = %v, want dedupe", dupes)
	}
	if n := len(mustReadEvents(t)); n != 1 {
		t.Fatalf("recorded %d events, want 1", n)
	}

	// 3. A question batch parks the live attempt in WAITING_HUMAN.
	ack, _ = consumeBatch(t, "t", "a1", "r", "orca-test/1.0", now.Add(time.Minute), `{
		"deliveryId": "dlv-2",
		"messages": [{"id": "msg-2", "type": "question", "taskId": "t",
			"dispatchId": "ctx-2", "outcome": "question"}]
	}`)
	if ack != "dlv-2" {
		t.Fatalf("ack = %q, want dlv-2", ack)
	}
	if w := DeriveWait("t", "a1", mustReadEvents(t)); w.State != WaitHuman || w.NextActor != "owner" {
		t.Fatalf("DeriveWait = %+v, want WAITING_HUMAN/owner", w)
	}

	// 4. A stale attempt's event is recorded but never moves live state.
	consumeBatch(t, "t", "old", "r", "orca-test/1.0", now.Add(2*time.Minute), `{
		"deliveryId": "dlv-3",
		"messages": [{"id": "msg-3", "type": "worker_done", "taskId": "t",
			"dispatchId": "ctx-0", "outcome": "escalation"}]
	}`)
	if w := DeriveWait("t", "a1", mustReadEvents(t)); w.State != WaitHuman {
		t.Fatalf("stale event moved live state: %+v", w)
	}

	// 5. An unreadable batch is refused, never classified by guess.
	if _, err := parseOrcaBatch([]byte("{corrupt")); err == nil {
		t.Fatal("corrupt batch accepted")
	}
	if _, err := parseOrcaBatch([]byte(`{"deliveryId":"x","messages":[]}`)); err == nil {
		t.Fatal("empty batch accepted")
	}
}
