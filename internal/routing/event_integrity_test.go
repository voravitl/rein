package routing

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordRefusesCorruptHistory(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	if err := os.MkdirAll(dir(), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{truncated")
	if err := os.WriteFile(eventLogPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	e := eventFixture("r", "t", "a", "new", time.Now(), KindWorker, ClassWorkerDone)
	if _, err := Record(e); err == nil {
		t.Fatal("corrupt history accepted")
	}
	got, err := os.ReadFile(eventLogPath())
	if err != nil || string(got) != string(original) {
		t.Fatalf("history changed: %q, %v", got, err)
	}
}

func TestRecordRejectsEventIdentityCollision(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	e := eventFixture("r", "t", "a", "same", time.Now(), KindWorker, ClassWorkerDone)
	if _, err := Record(e); err != nil {
		t.Fatal(err)
	}
	e.Attempt = "other"
	if _, err := Record(e); err == nil {
		t.Fatal("different attempt silently deduped")
	}
}

func mustReadEvents(t *testing.T) []Event {
	t.Helper()
	events, err := ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestRecordDuplicateRetriesFailedSync(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	original := syncEventFile
	t.Cleanup(func() { syncEventFile = original })
	syncFailure := errors.New("storage sync failed")
	calls := 0
	syncEventFile = func(f *os.File) error { calls++; return syncFailure }
	e := eventFixture("r", "t", "a", "same", time.Now(), KindWorker, ClassWorkerDone)
	if _, err := Record(e); !errors.Is(err, syncFailure) {
		t.Fatalf("first sync = %v", err)
	}
	if _, err := Record(e); !errors.Is(err, syncFailure) {
		t.Fatalf("duplicate bypassed failed sync: %v", err)
	}
	if calls != 2 {
		t.Fatalf("sync calls=%d", calls)
	}
	syncEventFile = original
	if dup, err := Record(e); !dup || err != nil {
		t.Fatalf("durable retry = %v %v", dup, err)
	}
}
