package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Appending n events must not cost O(n^2): the test counts how often the log is read, and with the dedupe index it is
// read once, not once per append.
func TestRecordDoesNotRescanTheLogPerAppend(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	reads := 0
	original := readEvents
	readEvents = func() ([]Event, error) {
		reads++
		return original()
	}
	t.Cleanup(func() { readEvents = original })

	const n = 40
	for i := 0; i < n; i++ {
		e := eventFixture("r", "t", "a", fmt.Sprintf("e%d", i), time.Now(), KindWorker, ClassWorkerDone)
		if _, err := Record(e); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	// One read for the first append; the rest are served by the index.
	if reads > 2 {
		t.Errorf("the log was read %d times for %d appends; the dedupe index is not used", reads, n)
	}
}

// A duplicate is still recognized after the index was built, and a changed identity for the same EventID is refused.
func TestRecordIndexKeepsDedupeAndCollisionRules(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	e := eventFixture("r", "t", "a", "same", time.Now(), KindWorker, ClassWorkerDone)
	if dup, err := Record(e); err != nil || dup {
		t.Fatalf("first record = %v %v", dup, err)
	}
	if dup, err := Record(e); err != nil || !dup {
		t.Fatalf("duplicate = %v %v", dup, err)
	}
	other := e
	other.Attempt = "other"
	if _, err := Record(other); err == nil {
		t.Fatal("a different attempt under the same EventID was accepted")
	}
	events := mustReadEvents(t)
	if len(events) != 1 {
		t.Fatalf("the log holds %d events, want 1", len(events))
	}
}

// A log that grew behind the index's back (another process appended) is re-read, so a duplicate is still seen.
func TestRecordRereadsWhenTheLogChanged(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	e := eventFixture("r", "t", "a", "same", time.Now(), KindWorker, ClassWorkerDone)
	if _, err := Record(e); err != nil {
		t.Fatal(err)
	}
	// Simulate another writer: append a line the index has never seen.
	other := eventFixture("r", "t", "a", "other", time.Now(), KindWorker, ClassWorkerDone)
	b, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(eventLogPath(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if dup, err := Record(e); err != nil || !dup {
		t.Fatalf("duplicate after an outside append = %v %v", dup, err)
	}
}
