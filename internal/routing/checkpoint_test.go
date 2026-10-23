package routing

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func cpSh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// cpRepo builds a real git repo with a main branch: one committed file plus a
// mix of staged, unstaged, allow-listed untracked, symlink and binary states.
func cpRepo(t *testing.T) (wt string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	wt = filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "init", "-q", "-b", "main", ".")
	cpSh(t, wt, "git", "config", "core.hooksPath", "/dev/null")
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "staged.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "add", ".")
	cpSh(t, wt, "git", "commit", "-qm", "base")
	cpSh(t, wt, "git", "update-ref", "refs/remotes/origin/main", "HEAD")
	// staged change
	if err := os.WriteFile(filepath.Join(wt, "staged.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "add", "staged.txt")
	// unstaged change
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("base\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// allow-listed untracked (binary) + symlink
	if err := os.WriteFile(filepath.Join(wt, "blob.bin"), []byte{0, 1, 2, 3, 255}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(wt, "link.txt")); err != nil {
		t.Fatal(err)
	}
	return wt
}

func TestCheckpointRoundtrip(t *testing.T) {
	wt := cpRepo(t)
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err != nil {
		t.Fatal(err)
	}
	if !cp.Complete || cp.BaseSHA == "" || cp.HeadSHA == "" || cp.ContractHash != "ch" {
		t.Fatalf("incomplete checkpoint: %+v", cp)
	}
	byPath := map[string]FileEntry{}
	for _, e := range cp.Files {
		byPath[e.Path] = e
	}
	for path, want := range map[string]string{"staged.txt": "staged", "tracked.txt": "unstaged", "blob.bin": "untracked", "link.txt": "untracked"} {
		e, ok := byPath[path]
		if !ok || e.Status != want {
			t.Errorf("%s = %+v, want status %s", path, e, want)
		}
		if e.SHA == "" || e.Mode == "" {
			t.Errorf("%s missing hash/mode: %+v", path, e)
		}
	}
	if byPath["link.txt"].Mode != "120000" || byPath["link.txt"].Symlink != "tracked.txt" {
		t.Errorf("symlink not recorded: %+v", byPath["link.txt"])
	}
	loaded, err := LoadCheckpoint("t", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HeadSHA != cp.HeadSHA || len(loaded.Files) != len(cp.Files) {
		t.Fatalf("load mismatch: %+v", loaded)
	}
	if err := VerifyCheckpoint("t", "a1"); err != nil {
		t.Fatalf("verify clean checkout: %v", err)
	}
}

func TestCheckpointRefusals(t *testing.T) {
	wt := cpRepo(t)
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin"}, "ch"); err == nil {
		t.Fatal("stray untracked link.txt accepted")
	}
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "refs/remotes/origin/missing", []string{"blob.bin", "link.txt"}, "ch"); err == nil {
		t.Fatal("unresolvable base accepted")
	}
	if _, err := FinalizeCheckpoint("../x", "a1", "r", wt, "origin/main", nil, "ch"); err == nil {
		t.Fatal("bad task accepted")
	}
	if _, err := LoadCheckpoint("t", "missing"); err == nil {
		t.Fatal("missing checkpoint accepted")
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(checkpointPath("t", "bad")), "t"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpointPath("t", "bad"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint("t", "bad"); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
	if err := atomic(checkpointPath("t", "part"), &Checkpoint{Task: "t", Attempt: "part", Complete: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint("t", "part"); err == nil {
		t.Fatal("partial checkpoint accepted as restorable")
	}
}

func TestVerifyDetectsDrift(t *testing.T) {
	wt := cpRepo(t)
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("base\nmore\nCHANGED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpoint("t", "a1"); err == nil {
		t.Fatal("modified file accepted")
	}
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("base\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "add", "-A")
	cpSh(t, wt, "git", "commit", "-qm", "move head")
	if err := VerifyCheckpoint("t", "a1"); err == nil {
		t.Fatal("moved HEAD accepted")
	}
	if err := os.WriteFile(filepath.Join(wt, "sneaky.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpoint("t", "a1"); err == nil {
		t.Fatal("new uncheckpointed file accepted")
	}
}

func TestCheckpointRefusesUnrepresentableStates(t *testing.T) {
	for _, scenario := range []string{"mixed", "staged deletion", "unstaged deletion", "rename"} {
		t.Run(scenario, func(t *testing.T) {
			wt := cpRepo(t)
			switch scenario {
			case "mixed":
				if err := os.WriteFile(filepath.Join(wt, "staged.txt"), []byte("v3\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "staged deletion":
				cpSh(t, wt, "git", "rm", "-f", "staged.txt")
			case "unstaged deletion":
				if err := os.Remove(filepath.Join(wt, "tracked.txt")); err != nil {
					t.Fatal(err)
				}
			case "rename":
				cpSh(t, wt, "git", "mv", "staged.txt", "new name.txt")
			}
			if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err == nil {
				t.Fatal("state that cannot be restored by the manifest was marked complete")
			}
			if _, err := os.Stat(checkpointPath("t", "a1")); !os.IsNotExist(err) {
				t.Fatalf("failed finalize published a checkpoint: %v", err)
			}
		})
	}
}

func TestVerifyCheckpointDetectsIndexAndModeDrift(t *testing.T) {
	for _, scenario := range []string{"index", "executable"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "executable" && runtime.GOOS == "windows" {
				t.Skip("Windows chmod does not set POSIX execute mode bits")
			}
			wt := cpRepo(t)
			if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
				t.Fatal(err)
			}
			if scenario == "index" {
				cpSh(t, wt, "git", "reset", "-q", "HEAD", "--", "staged.txt")
			} else if err := os.Chmod(filepath.Join(wt, "blob.bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := VerifyCheckpoint("t", "a1"); err == nil {
				t.Fatal("changed index or executable mode accepted")
			}
		})
	}
}

func TestCheckpointDanglingSymlink(t *testing.T) {
	wt := cpRepo(t)
	if err := os.Remove(filepath.Join(wt, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(wt, "link.txt")); err != nil {
		t.Fatal(err)
	}
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range cp.Files {
		if e.Path == "link.txt" && e.SHA == "" {
			t.Fatal("symlink target has no content hash")
		}
	}
	if err := VerifyCheckpoint("t", "a1"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRejectsDotIdentities(t *testing.T) {
	for _, ids := range [][2]string{{".", "a1"}, {"..", "a1"}, {"t", "."}, {"t", ".."}} {
		t.Run(ids[0]+"_"+ids[1], func(t *testing.T) {
			wt := cpRepo(t)
			if _, err := FinalizeCheckpoint(ids[0], ids[1], "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err == nil {
				t.Error("invalid identity published a checkpoint")
			}
			if err := atomic(checkpointPath(ids[0], ids[1]), &Checkpoint{Task: ids[0], Attempt: ids[1], Complete: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCheckpoint(ids[0], ids[1]); err == nil {
				t.Error("invalid identity loaded a checkpoint")
			}
		})
	}
}

func TestVerifyCheckpointDetectsBaseRefDrift(t *testing.T) {
	wt := cpRepo(t)
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	tree, err := cpGit(wt, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := cpGit(wt, "-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", strings.TrimSpace(tree), "-p", "HEAD", "-m", "new base")
	if err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "update-ref", "refs/remotes/origin/main", strings.TrimSpace(commit))
	if err := VerifyCheckpoint("t", "a1"); err == nil {
		t.Fatal("changed base ref accepted with unchanged HEAD and files")
	}
}

func TestProductionCheckpointRestoreWithoutSource(t *testing.T) {
	wt := cpRepo(t)
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(checkpointPath("t", "a1"))
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint("t", "a1", dest); err != nil {
		t.Fatal(err)
	}
	if got, err := cpGit(dest, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(got) != cp.HeadSHA {
		t.Fatalf("HEAD: %s %v", got, err)
	}
	if got, _ := cpGit(dest, "status", "--porcelain"); !strings.Contains(got, "M  staged.txt") || !strings.Contains(got, " M tracked.txt") {
		t.Fatalf("index not restored: %s", got)
	}
	after, _ := os.ReadFile(checkpointPath("t", "a1"))
	if string(before) != string(after) {
		t.Fatal("restore mutated evidence")
	}
	if err := RestoreCheckpoint("t", "a1", dest); err == nil {
		t.Fatal("overwrote destination")
	}
}

func TestProductionCheckpointRejectsTamperedPathsBeforeDestination(t *testing.T) {
	for _, bad := range []string{"../escaped", ".git/config", "nested/.GIT/config", "link.txt/escaped", "a/../../escaped", "C:/escaped"} {
		t.Run(bad, func(t *testing.T) {
			wt := cpRepo(t)
			cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
			if err != nil {
				t.Fatal(err)
			}
			cp.Files[0].Path = bad
			// Recompute to exercise semantic validation, beyond corruption checks.
			cp.Digest = checkpointDigest(cp)
			if err := atomic(checkpointPath("t", "a1"), cp); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "restored")
			if err := RestoreCheckpoint("t", "a1", dest); err == nil {
				t.Fatal("accepted unsafe manifest")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatalf("destination written before validation: %v", err)
			}
		})
	}
}

func TestProductionCheckpointRejectsMalformedGitTreeBeforeDestination(t *testing.T) {
	for _, kind := range []string{"symlink-parent", "file-parent", "duplicate-file"} {
		t.Run(kind, func(t *testing.T) {
			wt := cpRepo(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "owned")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitInput := func(input string, args ...string) string {
				t.Helper()
				out, err := cpGitInput(wt, strings.NewReader(input), args...)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(string(out))
			}
			blob := gitInput("overwrite", "hash-object", "-w", "--stdin")
			link := gitInput(outside, "hash-object", "-w", "--stdin")
			child := gitInput("100644 blob "+blob+"\towned\n", "mktree")
			entries := "120000 blob " + link + "\tdir\n040000 tree " + child + "\tdir\n"
			wantErr := "Git tree file is also a parent"
			if kind == "file-parent" {
				entries = "100644 blob " + blob + "\tdir\n040000 tree " + child + "\tdir\n"
			} else if kind == "duplicate-file" {
				entries = "100644 blob " + blob + "\towned\n100755 blob " + blob + "\towned\n"
				wantErr = "duplicate Git tree path"
			}
			root := gitInput(entries, "mktree")
			sha := gitInput("malformed", "-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", root)
			pack, err := cpGitInput(wt, strings.NewReader(sha+"\n"), "pack-objects", "--stdout", "--revs")
			if err != nil {
				t.Fatal(err)
			}
			cp := &Checkpoint{Task: "t", Attempt: "malformed", Run: "r", BaseSHA: sha, HeadSHA: sha, Base: sha, Complete: true, Pack: pack, Files: []FileEntry{}}
			cp.Digest = checkpointDigest(cp)
			if err := atomic(checkpointPath(cp.Task, cp.Attempt), cp); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "restore")
			err = RestoreCheckpoint(cp.Task, cp.Attempt, dest)
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Errorf("expected pre-write rejection %q, got %v", wantErr, err)
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Errorf("destination created: %v", err)
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "untouched" {
				t.Fatalf("outside sentinel changed: %q, %v", got, err)
			}
		})
	}
}

func TestProductionCheckpointRejectsCorruptCapturedData(t *testing.T) {
	for _, corrupt := range []string{"content", "pack", "head", "digest"} {
		t.Run(corrupt, func(t *testing.T) {
			wt := cpRepo(t)
			cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
			if err != nil {
				t.Fatal(err)
			}
			switch corrupt {
			case "content":
				cp.Files[0].Content = []byte("tampered")
			case "pack":
				cp.Pack[len(cp.Pack)/2] ^= 1
			case "head":
				cp.HeadSHA = strings.Repeat("0", 40)
			case "digest":
				cp.Digest = ""
			}
			if corrupt != "digest" {
				cp.Digest = checkpointDigest(cp)
			}
			if err := atomic(checkpointPath("t", "a1"), cp); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "restored")
			if err := RestoreCheckpoint("t", "a1", dest); err == nil {
				t.Fatal("accepted tampered checkpoint")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatalf("destination written: %v", err)
			}
		})
	}
}

func TestProductionCheckpointRestoresExecutableAndChangedSource(t *testing.T) {
	wt := cpRepo(t)
	if err := os.Chmod(filepath.Join(wt, "staged.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "add", "staged.txt")
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "staged.txt"), []byte("changed after finalize"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint("t", "a1", dest); err != nil {
		t.Fatal(err)
	}
	if err := verifyCheckpointAt(cp, dest, cp.BaseSHA); err != nil {
		t.Fatal(err)
	}
	t.Run("executable mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows chmod does not set POSIX execute mode bits")
		}
		fi, err := os.Stat(filepath.Join(dest, "staged.txt"))
		if err != nil || fi.Mode().Perm()&0o111 == 0 {
			t.Fatalf("executable mode lost: %v %v", fi, err)
		}
	})
}

func TestCheckpointRefusesHiddenIndexChanges(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			wt := cpRepo(t)
			cpSh(t, wt, "git", "update-index", flag, "tracked.txt")
			if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err == nil {
				t.Fatal("hidden index state accepted")
			}
			if _, err := os.Stat(checkpointPath("t", "a1")); !os.IsNotExist(err) {
				t.Fatalf("published unsupported snapshot: %v", err)
			}
		})
	}
}

func TestCheckpointRefusesUncapturedCheckoutConversion(t *testing.T) {
	wt := cpRepo(t)
	cpSh(t, wt, "git", "config", "core.autocrlf", "true")
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("base\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "add", "tracked.txt")
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err == nil {
		t.Fatal("accepted unchanged file whose raw bytes differ from Git snapshot")
	}
}

func TestCheckpointRestoresRawGitBlobsWithoutCheckoutConversion(t *testing.T) {
	wt := cpRepo(t)
	for p, content := range map[string]string{".gitattributes": "attr.txt text eol=crlf\n", "attr.txt": "raw LF bytes\n"} {
		if err := os.WriteFile(filepath.Join(wt, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cpSh(t, wt, "git", "add", ".gitattributes", "attr.txt")
	cpSh(t, wt, "git", "commit", "-qm", "attributes")
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint("t", "a1", dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "attr.txt"))
	if err != nil || string(b) != "raw LF bytes\n" {
		t.Fatalf("checkout conversion changed raw bytes: %q %v", b, err)
	}
}

func TestCheckpointRestoreVerificationFailureRemovesOnlyNewDestination(t *testing.T) {
	wt := cpRepo(t)
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err != nil {
		t.Fatal(err)
	}
	cp.Files[0].Status = "unstaged" // blob.bin has no index entry, so this state cannot verify.
	cp.Digest = checkpointDigest(cp)
	if err := atomic(checkpointPath("t", "a1"), cp); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	sentinel := filepath.Join(parent, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(parent, "restored")
	if err := RestoreCheckpoint("t", "a1", dest); err == nil {
		t.Fatal("accepted unverifiable restored state")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("partial destination remained: %v", err)
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "keep" {
		t.Fatalf("touched preexisting parent data: %q %v", b, err)
	}
}

func TestCheckpointGitEnvironmentCannotRedirectRepository(t *testing.T) {
	wt := cpRepo(t)
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err != nil {
		t.Fatal(err)
	}
	sourceHead, _ := os.ReadFile(filepath.Join(wt, ".git", "HEAD"))
	sourceIndex, _ := os.ReadFile(filepath.Join(wt, ".git", "index"))
	externalIndex := filepath.Join(t.TempDir(), "external-index")
	if err := os.WriteFile(externalIndex, []byte("untouched index sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"GIT_DIR": filepath.Join(wt, ".git"), "GIT_WORK_TREE": wt, "GIT_INDEX_FILE": externalIndex,
		"GIT_OBJECT_DIRECTORY": filepath.Join(wt, ".git", "objects"), "GIT_COMMON_DIR": filepath.Join(wt, ".git"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(wt, ".git", "objects"),
		"GIT_CONFIG_COUNT":                 "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": wt,
	} {
		t.Setenv(k, v)
	}
	// Both existing-source verification and restoration must ignore caller overrides.
	if err := VerifyCheckpoint("t", "a1"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint("t", "a1", dest); err != nil {
		t.Fatal(err)
	}
	if head, err := cpGit(dest, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(head) != cp.HeadSHA {
		t.Fatalf("destination HEAD %s: %v", head, err)
	}
	for p, want := range map[string]string{
		filepath.Join(wt, ".git", "HEAD"): string(sourceHead), filepath.Join(wt, ".git", "index"): string(sourceIndex), externalIndex: "untouched index sentinel",
	} {
		if got, err := os.ReadFile(p); err != nil || string(got) != want {
			t.Fatalf("caller environment redirected Git into %s: %v", p, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dest, ".git", "HEAD")); err != nil || strings.TrimSpace(string(b)) != cp.HeadSHA {
		t.Fatalf("destination not independent: %q %v", b, err)
	}
}

func TestCheckpointDirectorySyncFailureCannotReturnComplete(t *testing.T) {
	wt := cpRepo(t)
	old := checkpointSyncDir
	t.Cleanup(func() { checkpointSyncDir = old })
	checkpointSyncDir = func(string) error { return os.ErrPermission }
	cp, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch")
	if err == nil || cp != nil {
		t.Fatalf("directory sync failure returned complete checkpoint: %+v %v", cp, err)
	}
}

func TestCheckpointSyncsDirectoryAncestry(t *testing.T) {
	wt := cpRepo(t)
	old := checkpointSyncDir
	t.Cleanup(func() { checkpointSyncDir = old })
	var synced []string
	checkpointSyncDir = func(p string) error { synced = append(synced, p); return old(p) }
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(checkpointPath("t", "a1"))
	for _, got := range synced {
		if got != parent {
			t.Fatalf("sync %s want %s", got, parent)
		}
		parent = filepath.Dir(parent)
	}
	if len(synced) < 2 || synced[len(synced)-1] != filepath.Dir(synced[len(synced)-1]) {
		t.Fatalf("directory ancestry not durable: %v", synced)
	}
}

func TestCheckpointRefusesExternalFiltersWithoutExecuting(t *testing.T) {
	wt := cpRepo(t)
	if err := os.WriteFile(filepath.Join(wt, ".gitattributes"), []byte("tracked.txt filter=external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cpSh(t, wt, "git", "add", ".gitattributes")
	cpSh(t, wt, "git", "commit", "-qm", "attributes")
	sentinel := filepath.Join(t.TempDir(), "filter-executed")
	cpSh(t, wt, "git", "config", "filter.external.clean", "touch "+sentinel)
	if _, err := FinalizeCheckpoint("t", "a1", "r", wt, "origin/main", []string{"blob.bin", "link.txt"}, "ch"); err == nil {
		t.Fatal("accepted external filter")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("executed external clean filter: %v", err)
	}
}
