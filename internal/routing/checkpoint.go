package routing

// Durable checkpoint (Phase B slice B4). A checkpoint is finalized only at a
// safe boundary (writer stopped): it records base/head SHAs, worktree identity,
// the index/worktree state (staged, unstaged, allowed untracked, modes,
// symlinks, hashes) and the spec/contract/policy digests the attempt ran under.
// Finalize is atomic (temp file + rename, same as atomic in routing.go); a
// partial checkpoint is never returned as complete. Restore is verified by
// comparing the manifest against the checkout: any divergence fails closed.
//
// What a checkpoint is NOT: a rollback of external side effects, a permission
// to run destructive cleanup (no reset --hard, no stash-by-default), or a
// receipt to launch (it carries no execution authority).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// Checkpoint is the finalized, restorable record of one attempt's worktree.
type Checkpoint struct {
	Task         string      `json:"task"`
	Attempt      string      `json:"attempt"`
	Run          string      `json:"run"`
	Base         string      `json:"base"`     // ref the diff was taken against
	BaseSHA      string      `json:"base_sha"` // resolved base commit
	HeadSHA      string      `json:"head_sha"`
	Worktree     string      `json:"worktree"`
	ContractHash string      `json:"contract_hash"`
	Files        []FileEntry `json:"files"` // tracked + allowed untracked, sorted by path
	Complete     bool        `json:"complete"`
	Pack         []byte      `json:"pack"`
	Digest       string      `json:"digest"`
}

// FileEntry is one file's recorded state.
type FileEntry struct {
	Path    string `json:"path"`              // worktree-relative, slash-separated
	Status  string `json:"status"`            // staged | unstaged | untracked | unmodified
	SHA     string `json:"sha"`               // sha256 of worktree content (blobs for staged)
	Mode    string `json:"mode"`              // e.g. "100644", "120000" for symlinks
	Symlink string `json:"symlink,omitempty"` // link target when Mode is 120000
	Content []byte `json:"content"`
}

var checkpointSyncDir = func(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func checkpointPath(task, attempt string) string {
	return filepath.Join(dir(), "checkpoints", task, attempt+".json")
}

func cpGit(wt string, args ...string) (string, error) {
	out, err := cpGitInput(wt, nil, args...)
	return string(out), err
}

// FinalizeCheckpoint records supported worktree states for task/attempt.
// Mixed index/worktree changes, deletions, renames and conflicts fail closed
// because this manifest cannot restore them. base is the
// ref (default "origin/main"); extraUntracked names untracked paths that are
// allowed to exist (anything else untracked fails the checkpoint, so stray
// files can never sneak into a restore). contractHash binds the attempt's
// policy (use ContractHash(c)).
func FinalizeCheckpoint(task, attempt, run, worktree, base string, extraUntracked []string, contractHash string) (*Checkpoint, error) {
	cp, err := snapshotCheckpoint(task, attempt, run, worktree, base, extraUntracked, contractHash)
	if err != nil {
		return nil, err
	}
	pack, err := cpGitInput(worktree, strings.NewReader(cp.HeadSHA+"\n"+cp.BaseSHA+"\n"), "pack-objects", "--stdout", "--revs")
	if err != nil {
		return nil, err
	}
	cp.Pack = pack
	cp.Digest = checkpointDigest(cp)
	// Recheck after collection: a concurrent writer must not publish a mixed snapshot.
	if err := verifyCheckpointAt(cp, worktree, cp.Base); err != nil {
		return nil, err
	}
	if err := atomic(checkpointPath(task, attempt), cp); err != nil {
		return nil, err
	}
	// Persist the rename and any newly created ancestor directory entries.
	for parent := filepath.Dir(checkpointPath(task, attempt)); ; parent = filepath.Dir(parent) {
		if err := checkpointSyncDir(parent); err != nil {
			return nil, fmt.Errorf("checkpoint directory durability: %w", err)
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return cp, nil
}

func snapshotCheckpoint(task, attempt, run, worktree, base string, extraUntracked []string, contractHash string) (*Checkpoint, error) {
	if task == "" || task == "." || task == ".." || filepath.Base(task) != task || attempt == "" || attempt == "." || attempt == ".." || filepath.Base(attempt) != attempt || run == "" {
		return nil, errors.New("checkpoint needs task, attempt and run")
	}
	if worktree == "" || base == "" {
		return nil, errors.New("checkpoint needs worktree and base")
	}
	head, err := cpGit(worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	baseSHA, err := cpGit(worktree, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("unresolvable base %q: %w", base, err)
	}
	config, err := cpGit(worktree, "config", "--null", "--list")
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(config, "\x00") {
		key, _, _ := strings.Cut(entry, "\n")
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process") || strings.HasSuffix(key, ".smudge")) {
			return nil, errors.New("checkpoint refused: external Git filters are unsupported")
		}
	}
	status, err := cpGit(worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, u := range extraUntracked {
		allowed[filepath.ToSlash(u)] = true
	}
	staged, unstaged, untracked := map[string]bool{}, map[string]bool{}, []string{}
	for _, f := range strings.Split(status, "\x00") {
		if f == "" {
			continue
		}
		if len(f) < 4 {
			return nil, errors.New("checkpoint refused: invalid git status")
		}
		xy, path := f[:2], filepath.ToSlash(f[3:])
		// shortcut: one manifest entry cannot represent deletions or two file versions; refuse until restore supports them.
		switch xy {
		case "M ", "A ":
			staged[path] = true
		case " M":
			unstaged[path] = true
		case "??":
			untracked = append(untracked, path)
		default:
			return nil, fmt.Errorf("checkpoint refused: unsupported git status %q at %s", xy, path)
		}
	}
	for _, u := range untracked {
		if !allowed[u] {
			return nil, fmt.Errorf("checkpoint refused: untracked %s is not allow-listed", u)
		}
	}
	ls, err := cpGit(worktree, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	flags, err := cpGit(worktree, "ls-files", "-v", "-z")
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(flags, "\x00") {
		if entry != "" && entry[0] != 'H' {
			return nil, fmt.Errorf("checkpoint refused: unsupported index flags %q", entry)
		}
	}
	modes := map[string]string{} // path -> "mode blob"
	for _, f := range strings.Split(ls, "\x00") {
		parts := strings.SplitN(f, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) >= 2 {
			if len(fields) != 3 || fields[2] != "0" || !safeCheckpointPath(filepath.ToSlash(parts[1])) || (fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
				return nil, fmt.Errorf("checkpoint refused: unsupported index entry %q", f)
			}
			modes[filepath.ToSlash(parts[1])] = fields[0] + " " + fields[1]
		}
	}
	cp := &Checkpoint{
		Task: task, Attempt: attempt, Run: run,
		Base:    base,
		BaseSHA: strings.TrimSpace(baseSHA), HeadSHA: strings.TrimSpace(head),
		Worktree: worktree, ContractHash: contractHash, Complete: true,
	}
	add := func(path, status string) error {
		if !safeCheckpointPath(path) {
			return fmt.Errorf("checkpoint refused: unsafe path %q", path)
		}
		full := filepath.Join(worktree, filepath.FromSlash(path))
		fi, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("checkpoint file %s: %w", path, err)
		}
		e := FileEntry{Path: path, Status: status, Mode: "100644"}
		var content []byte
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			e.Mode, e.Symlink = "120000", target
			content = []byte(target)
		} else {
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("checkpoint refused: unsupported file type at %s", path)
			}
			content, err = os.ReadFile(full)
			if err != nil {
				return fmt.Errorf("checkpoint file %s: %w", path, err)
			}
			if fi.Mode().Perm()&0o111 != 0 {
				e.Mode = "100755"
			}
		}
		e.Content = content
		h := sha256.Sum256(content)
		e.SHA = hex.EncodeToString(h[:])
		if status == "staged" || status == "unmodified" {
			parts := strings.Fields(modes[path])
			if len(parts) != 2 || parts[0] != e.Mode {
				return fmt.Errorf("checkpoint refused: index mode differs at %s", path)
			}
			blob, err := cpGit(worktree, "cat-file", "-p", parts[1])
			if err != nil {
				return err
			}
			if !bytes.Equal([]byte(blob), content) {
				return fmt.Errorf("checkpoint refused: index content differs at %s", path)
			}
		}
		if status == "unmodified" {
			return nil
		}
		cp.Files = append(cp.Files, e)
		return nil
	}
	// Unrecorded checkout conversions must not silently change bytes on restore.
	for p := range modes {
		if !staged[p] && !unstaged[p] {
			if err := add(p, "unmodified"); err != nil {
				return nil, err
			}
		}
	}
	for p := range staged {
		if err := add(p, "staged"); err != nil {
			return nil, err
		}
	}
	for p := range unstaged {
		if err := add(p, "unstaged"); err != nil {
			return nil, err
		}
	}
	for _, p := range untracked {
		if err := add(p, "untracked"); err != nil {
			return nil, err
		}
	}
	sort.Slice(cp.Files, func(i, j int) bool { return cp.Files[i].Path < cp.Files[j].Path })
	if cp.Files == nil {
		cp.Files = []FileEntry{}
	}
	return cp, nil
}

// LoadCheckpoint reads a finalized checkpoint. A missing file, corrupt JSON,
// or Complete=false is an error: partial checkpoints are never usable.
func LoadCheckpoint(task, attempt string) (*Checkpoint, error) {
	if task == "" || task == "." || task == ".." || filepath.Base(task) != task || attempt == "" || attempt == "." || attempt == ".." || filepath.Base(attempt) != attempt {
		return nil, errors.New("invalid task or attempt")
	}
	b, err := os.ReadFile(checkpointPath(task, attempt))
	if err != nil {
		return nil, fmt.Errorf("no checkpoint for %s/%s: %w", task, attempt, err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, fmt.Errorf("corrupt checkpoint for %s/%s: %w", task, attempt, err)
	}
	if !cp.Complete {
		return nil, fmt.Errorf("checkpoint for %s/%s is partial, not restorable", task, attempt)
	}
	if cp.Task != task || cp.Attempt != attempt {
		return nil, fmt.Errorf("checkpoint identity mismatch for %s/%s", task, attempt)
	}
	if cp.Digest == "" || checkpointDigest(&cp) != cp.Digest {
		return nil, errors.New("checkpoint digest mismatch or missing durable snapshot")
	}
	if err := validateCheckpoint(&cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// VerifyCheckpoint compares the checkpoint manifest against the live checkout.
// It returns nil only when HEAD, base, every recorded file hash/mode/symlink and the
// untracked allow-list all match. It changes nothing.
func VerifyCheckpoint(task, attempt string) error {
	cp, err := LoadCheckpoint(task, attempt)
	if err != nil {
		return err
	}
	return verifyCheckpointAt(cp, cp.Worktree, cp.Base)
}

func verifyCheckpointAt(cp *Checkpoint, worktree, base string) error {
	var allowed []string
	for _, e := range cp.Files {
		if e.Status == "untracked" {
			allowed = append(allowed, e.Path)
		}
	}
	live, err := snapshotCheckpoint(cp.Task, cp.Attempt, cp.Run, worktree, base, allowed, cp.ContractHash)
	if err != nil {
		return err
	}
	if live.HeadSHA != cp.HeadSHA {
		return fmt.Errorf("HEAD moved since checkpoint (%s != %s): re-finalize", live.HeadSHA, cp.HeadSHA)
	}
	if live.BaseSHA != cp.BaseSHA {
		return fmt.Errorf("base moved since checkpoint (%s != %s): re-finalize", live.BaseSHA, cp.BaseSHA)
	}
	if !reflect.DeepEqual(live.Files, cp.Files) {
		return errors.New("index or worktree changed since checkpoint: re-finalize")
	}
	return nil
}

func cpGitInput(wt string, input io.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", wt, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	// Repository/config overrides can redirect even git -C into an unrelated repo.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = input
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out.Bytes(), nil
}

func checkpointDigest(cp *Checkpoint) string {
	copy := *cp
	copy.Digest, copy.Worktree = "", ""
	b, _ := json.Marshal(copy)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func safeCheckpointPath(p string) bool {
	if p == "" || p == "." || path.Clean(p) != p || path.IsAbs(p) || strings.ContainsAny(p, "\\\x00:") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.EqualFold(strings.TrimRight(part, ". "), ".git") || strings.EqualFold(part, "git~1") || strings.HasPrefix(strings.ToLower(part), ".git~") {
			return false
		}
	}
	return true
}

func validateCheckpoint(cp *Checkpoint) error {
	if cp.Run == "" || len(cp.Pack) == 0 {
		return errors.New("checkpoint missing run or Git object snapshot")
	}
	for _, sha := range []string{cp.HeadSHA, cp.BaseSHA} {
		b, err := hex.DecodeString(sha)
		if err != nil || (len(b) != 20 && len(b) != 32) {
			return errors.New("checkpoint has invalid commit identity")
		}
	}
	paths := map[string]bool{}
	for _, e := range cp.Files {
		if !safeCheckpointPath(e.Path) || paths[e.Path] {
			return fmt.Errorf("checkpoint unsafe or duplicate path %q", e.Path)
		}
		paths[e.Path] = true
		if e.Status != "staged" && e.Status != "unstaged" && e.Status != "untracked" {
			return fmt.Errorf("checkpoint unsupported status %q", e.Status)
		}
		if e.Mode != "100644" && e.Mode != "100755" && e.Mode != "120000" {
			return fmt.Errorf("checkpoint unsupported mode %q", e.Mode)
		}
		h := sha256.Sum256(e.Content)
		if hex.EncodeToString(h[:]) != e.SHA || (e.Mode == "120000" && string(e.Content) != e.Symlink) {
			return fmt.Errorf("checkpoint content mismatch at %s", e.Path)
		}
	}
	for p := range paths {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if paths[parent] {
				return fmt.Errorf("checkpoint file is also a parent: %s", parent)
			}
		}
	}
	return nil
}

// RestoreCheckpoint creates a fresh, independent repository from durable data.
// Failed operations remove only their own newly created destination.
func RestoreCheckpoint(task, attempt, dest string) (err error) {
	cp, err := LoadCheckpoint(task, attempt)
	if err != nil {
		return err
	}
	if dest == "" {
		return errors.New("checkpoint restore needs destination")
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(dest); !os.IsNotExist(err) {
		return fmt.Errorf("restore destination must not exist: %s", dest)
	}
	// Inspect objects in an isolated temporary repository before touching destination.
	scratch, err := os.MkdirTemp("", "rein-checkpoint-objects-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	initArgs := []string{"init", "--quiet", "--template="}
	if len(cp.HeadSHA) == 64 {
		initArgs = append(initArgs, "--object-format=sha256")
	}
	if _, err = cpGitInput(scratch, nil, initArgs...); err != nil {
		return err
	}
	if _, err = cpGitInput(scratch, bytes.NewReader(cp.Pack), "unpack-objects", "-r"); err != nil {
		return err
	}
	for _, sha := range []string{cp.HeadSHA, cp.BaseSHA} {
		if _, err = cpGitInput(scratch, nil, "cat-file", "-e", sha+"^{commit}"); err != nil {
			return err
		}
	}
	tree, err := cpGitInput(scratch, nil, "ls-tree", "-rz", "--full-tree", cp.HeadSHA)
	if err != nil {
		return err
	}
	treePaths := map[string]string{}
	treeBlobs := map[string]string{}
	for _, line := range bytes.Split(tree, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		pieces := bytes.SplitN(line, []byte{'\t'}, 2)
		if len(pieces) != 2 {
			return errors.New("invalid checkpoint Git tree")
		}
		fields := strings.Fields(string(pieces[0]))
		p := string(pieces[1])
		if len(fields) != 3 || !safeCheckpointPath(p) || (fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
			return fmt.Errorf("unsafe or unsupported Git tree path %q", p)
		}
		if _, exists := treePaths[p]; exists {
			return fmt.Errorf("duplicate Git tree path %q", p)
		}
		treePaths[p] = fields[0]
		treeBlobs[p] = fields[2]
	}
	for p := range treePaths {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if _, exists := treePaths[parent]; exists {
				return fmt.Errorf("Git tree file is also a parent: %s", parent)
			}
		}
	}
	for _, e := range cp.Files {
		for parent := path.Dir(e.Path); parent != "."; parent = path.Dir(parent) {
			if _, exists := treePaths[parent]; exists {
				return fmt.Errorf("checkpoint path crosses Git file or symlink %s", parent)
			}
		}
		for p := range treePaths {
			if strings.HasPrefix(p, e.Path+"/") {
				return fmt.Errorf("checkpoint file replaces Git directory %s", e.Path)
			}
		}
	}
	// Resolve the caller-selected parent once (macOS /tmp and /var are symlinks).
	parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err != nil {
		return fmt.Errorf("restore destination parent: %w", err)
	}
	dest = filepath.Join(parent, filepath.Base(dest))
	if err = os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dest)
		}
	}()
	if _, err = cpGitInput(dest, nil, initArgs...); err != nil {
		return err
	}
	if _, err = cpGitInput(dest, bytes.NewReader(cp.Pack), "unpack-objects", "-r"); err != nil {
		return err
	}
	// Write raw blobs: checkout filters could alter captured bytes or run commands.
	if _, err = cpGitInput(dest, nil, "update-ref", "--no-deref", "HEAD", cp.HeadSHA); err != nil {
		return err
	}
	if _, err = cpGitInput(dest, nil, "read-tree", cp.HeadSHA); err != nil {
		return err
	}
	writeFile := func(e FileEntry) error {
		full := filepath.Join(dest, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		if e.Mode == "120000" {
			return os.Symlink(string(e.Content), full)
		}
		mode := os.FileMode(0o644)
		if e.Mode == "100755" {
			mode = 0o755
		}
		return os.WriteFile(full, e.Content, mode)
	}
	for p, mode := range treePaths {
		content, readErr := cpGitInput(dest, nil, "cat-file", "blob", treeBlobs[p])
		if readErr != nil {
			return readErr
		}
		if err = writeFile(FileEntry{Path: p, Mode: mode, Content: content}); err != nil {
			return err
		}
	}
	for _, e := range cp.Files {
		if err = writeFile(e); err != nil {
			return err
		}
		if e.Status == "staged" {
			var blob []byte
			blob, err = cpGitInput(dest, bytes.NewReader(e.Content), "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			if _, err = cpGitInput(dest, nil, "update-index", "--add", "--cacheinfo", e.Mode+","+strings.TrimSpace(string(blob))+","+e.Path); err != nil {
				return err
			}
		}
	}
	return verifyCheckpointAt(cp, dest, cp.BaseSHA)
}
