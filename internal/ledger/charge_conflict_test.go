package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestChargeIdentityConflictsFailClosed(t *testing.T) {
	for _, field := range []string{"amount", "pool", "unit", "run", "task", "provider", "config", "effort", "component", "model"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
			a, _ := NewChargeRow("a", "x", "worker", "p", "usd", "w", "m", 1)
			a.Run = "r"
			a.Task = "t"
			a.Config = "cfg"
			a.Effort = "low"
			if err := Append(a); err != nil {
				t.Fatal(err)
			}
			if err := Append(a); err != nil {
				t.Fatalf("exact duplicate: %v", err)
			}
			b := a
			switch field {
			case "amount":
				n := 9.0
				b.Amount = &n
			case "pool":
				b.Pool = "other"
			case "unit":
				b.Unit = "tokens"
			case "run":
				b.Run = "other"
			case "task":
				b.Task = "other"
			case "provider":
				b.Provider = "other"
			case "config":
				b.Config = "other"
			case "effort":
				b.Effort = "high"
			case "component":
				b.Component = "review"
			case "model":
				b.Model = "other"
			}
			if err := Append(b); err == nil {
				t.Error("conflicting append accepted")
			}
			raw, _ := json.Marshal(b)
			f, err := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			f.Write(append(raw, '\n'))
			f.Close()
			if _, err := LoadStrict(""); err == nil {
				t.Error("existing conflicting ledger accepted")
			}
		})
	}
}

func TestConcurrentConflictingChargeAppend(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	results := make(chan error, 2)
	for _, amount := range []float64{1, 9} {
		go func(amount float64) {
			r, _ := NewChargeRow("a", "x", "worker", "p", "usd", "w", "m", amount)
			results <- Append(r)
		}(amount)
	}
	accepted := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d conflicting concurrent appends", accepted)
	}
	rows, err := LoadStrict("")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ledger = %+v, %v", rows, err)
	}
}
