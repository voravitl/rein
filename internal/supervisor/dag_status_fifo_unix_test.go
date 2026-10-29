//go:build !windows

package supervisor

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func makeDAGStatusFIFO(t *testing.T, path string) func() {
	t.Helper()
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// If a regression blocks a reader, opening and closing both ends releases it
	// before fixture cleanup restores the environment and removes paths.
	return func() {
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
	}
}

func dagTimingAppendPromptly(t *testing.T, l *dagLog, unblock func()) {
	t.Helper()
	finished := make(chan error, 1)
	go func() {
		finished <- l.append(dagEntry{Task: "A", Operation: "prepare", Phase: "intent", OpID: digestText("timing writer open regression")})
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("advisory sidecar rejection changed authoritative append: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		if unblock != nil {
			unblock()
		}
		select {
		case <-finished:
		case <-time.After(500 * time.Millisecond):
			t.Error("timing append remained blocked after FIFO release")
		}
		t.Fatal("timing append must reject an unsafe sidecar within 500ms")
	}
}

func TestDAGTimingAppendOpenRejectsFIFOPromptly(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusLog(t, p, hash)
	path := dagTimesPath(l.path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	unblock := makeDAGStatusFIFO(t, path)
	dagTimingAppendPromptly(t, l, unblock)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("rejected FIFO sidecar must remain a FIFO: info=%v err=%v", info, err)
	}
	if _, err := l.read(p, hash, true); err != nil || l.seq != 2 {
		t.Fatalf("FIFO rejection must preserve strict journal append: seq=%d err=%v", l.seq, err)
	}
}

func TestDAGTimingAppendOpenRejectsDanglingSymlink(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}})
	l := dagStatusLog(t, p, hash)
	path := dagTimesPath(l.path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "must-not-create-timing-target")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("dangling-symlink target must initially be absent: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	dagTimingAppendPromptly(t, l, nil)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("timing append must not create dangling-symlink target: %v", err)
	}
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Fatalf("rejected symlink must remain unchanged: target=%q err=%v", got, err)
	}
	if _, err := l.read(p, hash, true); err != nil || l.seq != 2 {
		t.Fatalf("symlink rejection must preserve strict journal append: seq=%d err=%v", l.seq, err)
	}
}

func TestDAGTimingOpenRejectsFIFOPromptly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replacement.times.jsonl")
	makeDAGStatusFIFO(t, path)
	// Keep both ends open until the blocked writer is drained on a mutation.
	// The hardened opener must return before this cleanup reader exists.
	var reader *os.File
	defer func() {
		if reader != nil {
			_ = reader.Close()
		}
	}()
	f, err := dagTimingOpenPromptly(t, path, func() {
		var openErr error
		reader, openErr = os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if openErr != nil {
			t.Errorf("release blocked FIFO writer: %v", openErr)
		}
	})
	if f != nil {
		_ = f.Close()
		t.Error("timing opener returned a descriptor for a FIFO without a reader")
	}
	if err == nil {
		t.Error("timing opener must reject FIFO independently of caller precheck")
	}
}

func TestDAGTimingOpenRejectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	path, target := filepath.Join(dir, "replacement.times.jsonl"), filepath.Join(dir, "absent-target")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target must initially be absent: %v", err)
	}
	f, err := dagTimingOpenPromptly(t, path, nil)
	if f != nil {
		_ = f.Close()
		t.Error("timing opener followed a dangling symlink")
	}
	if err == nil {
		t.Error("timing opener must reject symlink independently of caller precheck")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("timing opener created dangling-symlink target: %v", err)
	}
}
