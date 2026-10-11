package run

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockDir returns a temp dir and points every rein state location at temp dirs, so nothing here can reach the real home.
func lockDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(d, "ledger.jsonl"))
	t.Setenv("REIN_RUN_DIR", filepath.Join(d, "rundir"))
	return d
}

func TestLockFileBusyThenFree(t *testing.T) {
	p := filepath.Join(lockDir(t), "a", "b", "x.lock") // parent dirs do not exist yet
	unlock, err := LockFile(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("lock file mode: %v %v", fi, err)
		}
		if fi, err := os.Stat(filepath.Dir(p)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("lock dir mode: %v %v", fi, err)
		}
	}
	if _, err := LockFile(p, 0); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second holder with wait=0: %v, want ErrLockBusy", err)
	}
	unlock()
	again, err := LockFile(p, 0)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	again()
}

func TestLockFileWaitsForHolder(t *testing.T) {
	p := filepath.Join(lockDir(t), "x.lock")
	unlock, err := LockFile(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(150 * time.Millisecond); unlock() }()
	start := time.Now()
	second, err := LockFile(p, 10*time.Second)
	if err != nil {
		t.Fatalf("waiter: %v", err)
	}
	defer second()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("waiter got the lock after %v, before the holder released it", time.Since(start))
	}
}

func TestLockFileTimesOut(t *testing.T) {
	p := filepath.Join(lockDir(t), "x.lock")
	unlock, err := LockFile(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	if _, err := LockFile(p, 80*time.Millisecond); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("timed-out waiter: %v, want ErrLockBusy", err)
	}
	if d := time.Since(start); d < 70*time.Millisecond || d > 5*time.Second {
		t.Fatalf("waiter gave up after %v, want about 80ms", d)
	}
}

func TestLockFileUnlockIsIdempotent(t *testing.T) {
	p := filepath.Join(lockDir(t), "x.lock")
	first, err := LockFile(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	first()
	second, err := LockFile(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	first() // must not release the second holder's lock
	if _, err := LockFile(p, 0); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("a repeated unlock released someone else's lock: %v", err)
	}
	second()
}

// Without mutual exclusion the read-sleep-write below loses updates and two goroutines are inside at once. Atomics keep the
// race detector quiet: it cannot see flock, so a plain counter would be flagged even when the lock works.
func TestLockFileMutualExclusion(t *testing.T) {
	p := filepath.Join(lockDir(t), "x.lock")
	const workers, rounds = 8, 12
	var inside, overlaps, counter atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				unlock, err := LockFile(p, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				v := counter.Load()
				time.Sleep(time.Millisecond)
				counter.Store(v + 1)
				inside.Add(-1)
				unlock()
			}
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 || counter.Load() != workers*rounds {
		t.Fatalf("overlaps=%d counter=%d, want 0 and %d", overlaps.Load(), counter.Load(), workers*rounds)
	}
}
