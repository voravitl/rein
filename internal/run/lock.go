package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrLockBusy means another holder still has the lock after the wait elapsed.
var ErrLockBusy = errors.New("lock busy")

// LockFile takes an exclusive advisory lock on path (created 0600, parent dirs 0700, if missing), polling every ~10ms for up
// to wait (wait <= 0 means a single try). The lock dies with the process (flock semantics), so a crash never leaves a stale
// lock. It returns ErrLockBusy (wrapped; use errors.Is) when wait elapsed. unlock is idempotent. The lock file is never
// removed: unlinking it would let two holders lock different inodes of the same path.
func LockFile(path string, wait time.Duration) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	// ponytail: polling try-lock, no queue or fairness (a waiter can lose every race and time out); the ceiling is ~10ms of
	// wake-up latency per hand-off. Upgrade path: a blocking flock in a goroutine if holders ever get long.
	for {
		locked, err := lockFileExclusive(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if locked {
			var once sync.Once // a second unlock must not touch a file descriptor number that was reused
			return func() { once.Do(func() { _ = unlockFile(f); _ = f.Close() }) }, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			f.Close()
			return nil, fmt.Errorf("%w: %s", ErrLockBusy, path)
		}
		time.Sleep(min(left, 10*time.Millisecond))
	}
}
