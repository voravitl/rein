package supervisor

import (
	"io"
	"os"
	"sync"
	"time"
)

// ReadDAGStatuslineHost reads the optional host payload a status-line host
// (Claude Code, AGY) writes to the command's stdin. It never fails and never
// blocks for longer than budget: a terminal or unreadable stdin yields nil,
// a payload larger than limit bytes yields nil, and a host that keeps stdin
// open yields whatever arrived within budget. After limit+1 bytes the rest is
// discarded in the background so a large writer is not left blocked.
func ReadDAGStatuslineHost(in *os.File, limit int64, budget time.Duration) []byte {
	if in == nil || limit <= 0 || budget <= 0 {
		return nil
	}
	if info, err := in.Stat(); err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	var (
		mu   sync.Mutex
		buf  []byte
		over bool
	)
	done := make(chan struct{})
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := in.Read(chunk)
			mu.Lock()
			if n > 0 && !over {
				if int64(len(buf)+n) > limit {
					over, buf = true, nil
				} else {
					buf = append(buf, chunk[:n]...)
				}
			}
			stop := over
			mu.Unlock()
			if err != nil || stop {
				break
			}
		}
		close(done)
		// Drain the oversized remainder without holding up the caller.
		_, _ = io.Copy(io.Discard, in)
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	mu.Lock()
	defer mu.Unlock()
	if over {
		return nil
	}
	return append([]byte(nil), buf...)
}
