//go:build !windows

package providers

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pidsIn(t *testing.T, file string) []int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the fake CLI never recorded its pids: %v", err)
	}
	var out []int
	for _, f := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// every process the fake CLI (and its grandchild in the same process group) started must be gone once Discover returns.
func TestEveryStartedProcessIsGoneOnEveryExit(t *testing.T) {
	cases := []struct {
		name, scenario string
		timeout        time.Duration
		cancelAfter    time.Duration
	}{
		{"success", "ok", 20 * time.Second, 0},
		{"invalid schema", "badschema", 20 * time.Second, 0},
		{"partial pagination", "loop", 20 * time.Second, 0},
		{"timeout", "hang", 1500 * time.Millisecond, 0},
		{"cancel", "hang", time.Minute, 700 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pids")
			t.Setenv("REIN_FAKE_PIDFILE", pidfile)
			cfg := fake(t, "codex", c.scenario, "codex-cli 0.162.0")
			ctx := context.Background()
			if c.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				go func() { time.Sleep(c.cancelAfter); cancel() }()
			}
			Discover(ctx, cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: c.timeout, Harnesses: []string{"codex"}})
			pids := pidsIn(t, pidfile)
			if len(pids) < 2 || len(pids)%2 != 0 {
				t.Fatalf("every fake CLI run records a leader and a grandchild: %v", pids)
			}
			for _, pid := range pids {
				deadline := time.Now().Add(5 * time.Second)
				for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
					time.Sleep(25 * time.Millisecond)
				}
				if syscall.Kill(pid, 0) == nil {
					t.Errorf("process %d still alive after Discover returned (%s)", pid, c.name)
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		})
	}
}

// the one-shot adapters clean up their process groups too, including a grandchild that outlives a clean exit.
func TestOneShotAdaptersReapDescendants(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("REIN_FAKE_PIDFILE", pidfile)
	inv := one(t, fake(t, "kiro", "ok", "kiro-cli 2.21.0"), "kiro")
	if inv.Status != InvComplete {
		t.Fatalf("%+v", inv)
	}
	for _, pid := range pidsIn(t, pidfile) {
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			t.Errorf("process %d survived", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// An availability probe that is a wrapper (sh -c, npx, uv run, timeout) must not leave a descendant running after its bound
// expired: usage after the timeout would be unreserved and unrecorded.
func TestProbeTimeoutKillsDescendants(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pids")
	script := `sleep 300 & echo $! > "$REIN_PROBE_PIDFILE"; wait`
	t.Setenv("REIN_PROBE_PIDFILE", pidfile)
	cfg := &Config{Providers: map[string]Provider{"p": {Agent: "codex", Probe: []string{"/bin/sh", "-c", script}}}}
	start := time.Now()
	res := Probe(cfg, "p", time.Second)
	if res.State != "down" || !strings.Contains(res.Detail, "timeout") {
		t.Fatalf("probe: %+v", res)
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("the timeout took %s: the wrapper's pipes held the probe open", took)
	}
	pids := pidsIn(t, pidfile)
	if len(pids) != 1 {
		t.Fatalf("pids %v", pids)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pids[0], 0) == nil && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if syscall.Kill(pids[0], 0) == nil {
		_ = syscall.Kill(pids[0], syscall.SIGKILL)
		t.Fatal("the probe's background process outlived its timeout")
	}
}
