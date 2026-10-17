package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendRequiresDurableFileBeforeSuccess(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "nested", "ledger.jsonl"))
	original := syncLedgerFile
	t.Cleanup(func() { syncLedgerFile = original })
	failure := errors.New("injected ledger sync failure")
	syncLedgerFile = func(*os.File) error { return failure }
	amount := 2.0
	row := Row{Kind: "cost", Run: "run", Task: "task", Attempt: "attempt", Pool: "pool", Unit: "usd", Amount: &amount, ChargeID: "charge"}
	for retry := range 2 {
		if err := Append(row); !errors.Is(err, failure) {
			t.Fatalf("attempt %d returned %v; charge must not release its hold before sync", retry, err)
		}
		if rows, err := LoadStrict(""); !errors.Is(err, failure) || rows != nil {
			t.Fatalf("failed append became trusted charges: rows=%+v err=%v", rows, err)
		}
	}
	syncLedgerFile = original
	if err := Append(row); err != nil {
		t.Fatalf("retry after durable storage recovered: %v", err)
	}
	if _, err := LoadStrict(""); err != nil {
		t.Fatalf("retry corrupted charge ledger: %v", err)
	}
}

func TestAppendPropagatesDirectorySyncFailure(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "nested", "ledger.jsonl"))
	original := syncLedgerFile
	t.Cleanup(func() { syncLedgerFile = original })
	failure := errors.New("injected directory sync failure")
	syncLedgerFile = func(f *os.File) error {
		if f.Name() == filepath.Dir(Path()) {
			return failure
		}
		return original(f)
	}
	if err := Append(Row{Kind: "routing_launch", Task: "task"}); !errors.Is(err, failure) {
		t.Fatalf("launch record accepted before its directory entry was durable: %v", err)
	}
}

func TestAppendSyncsDirectoryAncestry(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PIPELINE_LEDGER", filepath.Join(base, "first", "second", "ledger.jsonl"))
	original := syncLedgerFile
	t.Cleanup(func() { syncLedgerFile = original })
	seen := map[string]bool{}
	syncLedgerFile = func(f *os.File) error {
		seen[f.Name()] = true
		return original(f)
	}
	if err := Append(Row{Kind: "task", Task: "task"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{Path(), filepath.Dir(Path()), filepath.Join(base, "first"), base} {
		if !seen[path] {
			t.Errorf("new ledger ancestry was not synced: %s", path)
		}
	}
}

func TestStrictReadRefusesUnsyncedVisibleCharge(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	row, err := NewChargeRow("attempt", "charge", "worker", "pool", "usd", "codex", "model", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(row); err != nil {
		t.Fatal(err)
	}
	original := syncLedgerFile
	t.Cleanup(func() { syncLedgerFile = original })
	failure := errors.New("durability unavailable")
	for _, failDir := range []bool{false, true} {
		syncLedgerFile = func(f *os.File) error {
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if info.IsDir() == failDir {
				return failure
			}
			return original(f)
		}
		if rows, err := LoadStrict(""); !errors.Is(err, failure) || rows != nil {
			t.Fatalf("directory failure=%v: unsafe accounting rows=%+v err=%v", failDir, rows, err)
		}
	}
	syncLedgerFile = original
	if rows, err := LoadStrict(""); err != nil || len(rows) != 1 {
		t.Fatalf("durability recovery: rows=%+v err=%v", rows, err)
	}
}
