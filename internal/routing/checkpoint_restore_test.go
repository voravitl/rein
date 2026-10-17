package routing

// Production restore integration with real Git repositories and captured evidence.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func shaFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestCheckpointRestoresNewWorktree(t *testing.T) {
	src := cpRepo(t)
	if _, err := FinalizeCheckpoint("t", "a1", "r", src, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	cp, err := LoadCheckpoint("t", "a1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]FileEntry{}
	for _, e := range cp.Files {
		want[e.Path] = e
	}
	// Staged + unstaged + allow-listed binary + symlink must all be present,
	// otherwise the round-trip below proves nothing.
	for _, must := range []string{"staged.txt", "tracked.txt", "blob.bin", "link.txt"} {
		if _, ok := want[must]; !ok {
			t.Fatalf("manifest missing %s: %+v", must, cp.Files)
		}
	}

	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint("t", "a1", dest); err != nil {
		t.Fatal(err)
	}

	// Every recorded file is back with its recorded hash and symlink target.
	for _, e := range cp.Files {
		full := filepath.Join(dest, filepath.FromSlash(e.Path))
		if e.Mode == "120000" {
			target, err := os.Readlink(full)
			if err != nil || target != e.Symlink {
				t.Fatalf("symlink %s not restored: %q err=%v", e.Path, target, err)
			}
			continue
		}
		if got := shaFile(full); got != e.SHA {
			t.Fatalf("%s hash = %s, want %s", e.Path, got, e.SHA)
		}
	}

	// The restored copy satisfies the shared verifier: HEAD, hashes and the
	// untracked allow-list all match the manifest.
	if err := verifyCheckpointAt(cp, dest, cp.BaseSHA); err != nil {
		t.Fatalf("restored worktree fails verify: %v", err)
	}
}

func TestRestoreRefusesPartialOrCorruptCheckpoint(t *testing.T) {
	src := cpRepo(t)
	if _, err := FinalizeCheckpoint("t", "a1", "r", src, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	path := checkpointPath("t", "a1")

	// A corrupt manifest is never restorable.
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreCheckpoint("t", "a1", filepath.Join(t.TempDir(), "restored")); err == nil {
		t.Fatal("restore accepted a corrupt checkpoint")
	}

	// A partial (Complete=false) manifest is never restorable.
	if err := atomic(path, &Checkpoint{Task: "t", Attempt: "a1", Complete: false}); err != nil {
		t.Fatal(err)
	}
	if err := RestoreCheckpoint("t", "a1", filepath.Join(t.TempDir(), "restored")); err == nil {
		t.Fatal("restore accepted a partial checkpoint")
	}

	// Captured evidence remains restorable after deleting the source worktree.
	if _, err := FinalizeCheckpoint("t", "a2", "r", src, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if err := RestoreCheckpoint("t", "a2", filepath.Join(t.TempDir(), "restored")); err != nil {
		t.Fatalf("restore without source failed: %v", err)
	}

	// An existing destination is never overwritten.
	if _, err := FinalizeCheckpoint("t", "a3", "r", cpRepo(t), "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	taken := filepath.Join(t.TempDir(), "taken")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RestoreCheckpoint("t", "a3", taken); err == nil {
		t.Fatal("restore overwrote an existing destination")
	}
}
