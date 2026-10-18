//go:build !windows

package orca

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func withFrozenTable(t *testing.T, rows []censusRow) {
	t.Helper()
	restore := censusScanTable
	censusScanTable = func() ([]censusRow, error) { return rows, nil }
	t.Cleanup(func() { censusScanTable = restore })
}

// TestCensusVerifyDecisionTable covers the judge semantics and same-host refusal with a frozen table.
func TestCensusVerifyDecisionTable(t *testing.T) {
	snap := &CensusSnapshot{
		Hostname: censusHostname(),
		Boot:     censusBoot(),
		Marker:   censusMarker(),
		Root:     censusRow{PID: 100, PPID: 9, Start: 1111, State: "S"},
		Nodes: []censusRow{
			{PID: 101, PPID: 100, Start: 2222, State: "S"}, // vanished
			{PID: 102, PPID: 100, Start: 3333, State: "Z"}, // same identity, now zombie
			{PID: 103, PPID: 100, Start: 4444, State: "S"}, // pid reused by another start marker
			{PID: 104, PPID: 100, Start: 5555, State: "S"}, // still alive in place
		},
	}
	withFrozenTable(t, []censusRow{
		{PID: 100, PPID: 1, Start: 1111, State: "S"},
		{PID: 102, PPID: 100, Start: 3333, State: "Z"},
		{PID: 103, PPID: 1, Start: 9999, State: "S"},
		{PID: 104, PPID: 100, Start: 5555, State: "S"},
		{PID: 200, PPID: 1, Start: 6000, State: "S"}, // unrelated process must not be judged
	})
	allGone, findings, err := CensusVerify(snap)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if allGone {
		t.Fatal("verify: report all-gone while pinned identities still exist")
	}
	want := []CensusFinding{
		{Identity: "root", Status: "present", Detail: "alive"},
		{Identity: "pid 101", Status: "gone", Detail: "absent"},
		{Identity: "pid 102", Status: "present", Detail: "zombie_pending_reap"},
		{Identity: "pid 103", Status: "gone", Detail: "pid_recycled"},
		{Identity: "pid 104", Status: "present", Detail: "alive"},
	}
	if len(findings) != len(want) {
		t.Fatalf("findings=%v want %d entries", findings, len(want))
	}
	for i, w := range want {
		if findings[i] != w {
			t.Errorf("finding[%d]=%+v want %+v", i, findings[i], w)
		}
	}

	gone := &CensusSnapshot{
		Hostname: censusHostname(),
		Boot:     censusBoot(),
		Marker:   censusMarker(),
		Root:     censusRow{PID: 100, PPID: 1, Start: 1111, State: "S"},
		Nodes:    []censusRow{{PID: 101, PPID: 100, Start: 2222, State: "S"}},
	}
	// unfork: 100 and 101 absent from this frozen table
	withFrozenTable(t, []censusRow{{PID: 200, PPID: 1, Start: 6000, State: "S"}})
	allGone, findings, err = CensusVerify(gone)
	if err != nil {
		t.Fatalf("verify gone: %v", err)
	}
	if !allGone || len(findings) != 2 || findings[0].Status != "gone" || findings[1].Detail != "absent" {
		t.Fatalf("gone case: allGone=%v findings=%v", allGone, findings)
	}

	foreign := *snap
	foreign.Hostname = "zzz-other-host-zzz"
	if _, _, err := CensusVerify(&foreign); !errors.Is(err, ErrCensusForeignSnapshot) {
		t.Fatalf("foreign hostname: err=%v", err)
	}
	otherBoot := *snap
	otherBoot.Boot = censusBoot() + "-wrong"
	if _, _, err := CensusVerify(&otherBoot); !errors.Is(err, ErrCensusForeignSnapshot) {
		t.Fatalf("foreign boot: err=%v", err)
	}
}

// TestCensusRecordRefusesMissingRoot fails closed when the root was never sampled.
func TestCensusRecordRefusesMissingRoot(t *testing.T) {
	dead := 1 << 30 // no host allocates this pid in practice; the table is rechecked below anyway
	real, err := censusScan()
	if err != nil {
		t.Skip(err)
	}
	for _, r := range real {
		if r.PID == dead {
			t.Skip("pid occupied unexpectedly")
		}
	}
	if _, err := CensusRecord(dead); err == nil {
		t.Fatal("record of an absent root must fail")
	}
}

// TestCensusTreeLifecycle pins a real shell plus its child, judges it alive, then gone.
func TestCensusTreeLifecycle(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 1; sleep 15") // two commands: no exec-optimization
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	root := cmd.Process.Pid
	time.Sleep(1400 * time.Millisecond) // first sleep exits and reaps; the tree is sh + sleep 15

	snap, err := CensusRecord(root)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if snap.Root.PID != root || snap.Root.Start <= 0 || snap.Root.PPID != os.Getpid() {
		t.Fatalf("root row wrong: %+v", snap.Root)
	}
	if snap.Marker != censusMarker() || snap.Hostname == "" {
		t.Fatalf("identity fields: %+v", snap)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].PPID != root || snap.Nodes[0].Start <= 0 || snap.Nodes[0].State == "Z" {
		t.Fatalf("subtree rows: %+v", snap.Nodes)
	}

	allGone, findings, err := CensusVerify(snap)
	if err != nil {
		t.Fatalf("verify alive: %v", err)
	}
	if allGone {
		t.Fatalf("verify says gone while tree alive: %+v", findings)
	}

	if err := syscall.Kill(-root, syscall.SIGKILL); err != nil { // whole group, not the shell alone
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil { // reap the shell
		t.Logf("shell wait: %v", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		gone, findings, err := CensusVerify(snap)
		if err != nil {
			t.Fatalf("verify after kill: %v", err)
		}
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tree still present after group kill: %+v", findings)
		}
		time.Sleep(300 * time.Millisecond)
	}

	path := filepath.Join(t.TempDir(), "census.json")
	if err := SaveCensus(snap, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCensus(path)
	if err != nil {
		t.Fatal(err)
	}
	gone, _, err := CensusVerify(loaded)
	if err != nil || !gone {
		t.Fatalf("roundtrip: gone=%v err=%v", gone, err)
	}
}

// TestCensusZombieIdentityStaysPresent keeps a zombie around: a strict verifier must not call it gone.
func TestCensusZombieIdentityStaysPresent(t *testing.T) {
	if _, err := censusScan(); err != nil {
		t.Skipf("process table unreadable here: %v", err)
	}
	cmd := exec.Command("sleep", "20")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		rows, err := censusScan()
		if err != nil {
			t.Fatal(err)
		}
		zombie := false
		for _, r := range rows {
			if r.PID == pid {
				zombie = r.State == "Z"
			}
		}
		if zombie || time.Now().After(deadline) {
			if !zombie {
				t.Skip("kernel reaped the zombie before it was visible to the table")
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	snap, err := CensusRecord(pid)
	if err != nil {
		t.Fatalf("record of zombie: %v", err)
	}
	allGone, findings, err := CensusVerify(snap)
	if err != nil {
		t.Fatal(err)
	}
	if allGone || len(findings) != 1 || findings[0].Status != "present" || findings[0].Detail != "zombie_pending_reap" {
		t.Fatalf("zombie judged as: allGone=%v findings=%v", allGone, findings)
	}
	if err := cmd.Wait(); err != nil { // reap for real; the pid then proves absent
		t.Logf("reap: %v", err)
	}
	allGone, _, err = CensusVerify(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !allGone {
		t.Fatal("reaped identity still reported present")
	}
}

// TestCensusResidualSpawnAfterRecord documents the known residual with real processes: a descendant
// that appears after the record is outside the sampled tree, so verify reports all-gone while that
// descendant is provably still running. This must stay honest — it is by-construction incompleteness
// that the caller covers with policy (fenced workers spawn inside the pinned group).
func TestCensusResidualSpawnAfterRecord(t *testing.T) {
	if _, err := censusScan(); err != nil {
		t.Skipf("process table unreadable here: %v", err)
	}
	dir := t.TempDir()
	trigger := filepath.Join(dir, "trigger")
	out := filepath.Join(dir, "orphan.pid")
	// A shell builtin blocked on a FIFO has no polling sleep child. The old
	// polling loop raced CensusRecord by creating exactly such a descendant.
	if err := syscall.Mkfifo(trigger, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `read -r trigger < "$TRIGGER"; "$SLEEPBIN" 15 & echo $! > "$OUT"; exit`, "")
	// $TRIGGER/$OUT/$SLEEPBIN come from env; the lone "" is the shell's $0
	cmd.Env = append(os.Environ(), "TRIGGER="+trigger, "OUT="+out, "SLEEPBIN=/bin/sleep")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	root := cmd.Process.Pid
	defer func() {
		// Reap the owned trigger shell even if recording/assertions fail before
		// the FIFO is released. Otherwise a failed test leaves a waiting shell.
		if cmd.ProcessState == nil {
			_ = syscall.Kill(-root, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	}()

	snap, err := CensusRecord(root)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(snap.Nodes) != 0 { // the shell is still waiting on the trigger
		t.Fatalf("expected an empty node set behind the trigger shell, got %+v", snap.Nodes)
	}

	if err := os.WriteFile(trigger, []byte("go\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil { // shell runs to exit after spawning the orphan
		t.Logf("shell exit: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("orphan pid not written: %v", err)
	}
	orphanPID := 0
	for _, c := range string(b) {
		if c >= '0' && c <= '9' {
			orphanPID = orphanPID*10 + int(c-'0')
		}
	}
	if orphanPID <= 0 {
		t.Fatalf("unparseable orphan pid %q", string(b))
	}

	allGone, findings, err := CensusVerify(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !allGone {
		t.Fatalf("residual demo failed: expected all-gone over the sampled tree, got %+v", findings)
	}
	orphanProc, err := os.FindProcess(orphanPID)
	if err != nil {
		t.Fatalf("find orphan: %v", err)
	}
	if err := orphanProc.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("orphan pid %d not alive while census said all-gone: %v", orphanPID, err)
	}
	// Cleanup: kill the orphan; it is ours (spawned inside our own pinned group), and no reap is owed.
	if err := orphanProc.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill orphan: %v", err)
	}
	if err := syscall.Kill(-root, syscall.SIGKILL); err == nil { // group may already be empty
		t.Log("group still existed at cleanup")
	}
}
