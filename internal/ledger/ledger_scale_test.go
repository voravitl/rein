package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Appending n charge rows must not cost O(n^2): the test counts the strict loads, and with the charge index the ledger
// is loaded once, not once per append.
func TestAppendDoesNotReloadTheLedgerPerCharge(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	loads := 0
	original := loadStrict
	loadStrict = func(since string) ([]Row, error) {
		loads++
		return original(since)
	}
	t.Cleanup(func() { loadStrict = original })

	const n = 30
	for i := 0; i < n; i++ {
		id := "charge-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		row, err := NewChargeRow("attempt", id, "worker", "pool", "usd", "w", "m", float64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if err := Append(row); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if loads > 2 {
		t.Errorf("the ledger was strictly loaded %d times for %d appends; the charge index is not used", loads, n)
	}
}

// A conflicting charge appended after the cache is warm still fails, and the same charge is still accepted.
func TestAppendKeepsChargeConflictAfterWarmCache(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	attempt, _ := NewChargeRow("attempt", "same-id", "worker", "pool", "usd", "w", "m", 1)
	if err := Append(attempt); err != nil {
		t.Fatal(err)
	}
	if err := Append(attempt); err != nil {
		t.Fatalf("exact duplicate rejected: %v", err)
	}
	bigger, _ := NewChargeRow("attempt", "same-id", "worker", "pool", "usd", "w", "m", 99)
	if err := Append(bigger); err == nil {
		t.Fatal("a conflicting charge was accepted")
	}
	rows, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	// The two identical appends are both written (the ledger keeps every row and the reader dedupes by charge id);
	// the conflicting one was refused, so it is not in the file.
	if len(rows) != 2 {
		t.Fatalf("the ledger holds %d rows, want 2", len(rows))
	}
}

// A ledger that already holds two conflicting charges must refuse a new append, exactly as before.
func TestAppendRefusesALedgerWithAPreExistingConflict(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ledger.jsonl")
	t.Setenv("PIPELINE_LEDGER", p)
	a, _ := NewChargeRow("attempt", "dup", "worker", "pool", "usd", "w", "m", 1)
	if err := Append(a); err != nil {
		t.Fatal(err)
	}
	b, _ := NewChargeRow("attempt", "dup", "worker", "pool", "usd", "w", "m", 2)
	// write the conflicting row straight into the file, bypassing Append
	line, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(line, '\n'))
	f.Close()
	// a new, unrelated append must fail: the ledger holds a conflict
	c, _ := NewChargeRow("attempt", "other", "worker", "pool", "usd", "w", "m", 3)
	if err := Append(c); err == nil {
		t.Fatal("an append to a ledger with a pre-existing conflict was accepted")
	}
}
