package routing

// The actual changes of a checkout, for the review phase: a task's tier follows what changed, not what the coordinator says
// changed, so the review's changed files are the ones git reports (plus whatever the coordinator listed: a claim can only add).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const defaultReviewBase = "origin/main"

// gitChangedFiles lists the paths the checkout changes against base: committed changes since the merge base (renames as a delete
// plus an add, so a moved file still shows its source path), and uncommitted or untracked ones.
func gitChangedFiles(worktree, base string) ([]string, error) {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
	set := map[string]bool{}
	names, err := git("diff", "--no-renames", "--name-only", "-z", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	for _, n := range strings.Split(string(names), "\x00") {
		if n != "" {
			set[n] = true
		}
	}
	status, err := git("status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(status), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		set[e[3:]] = true
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' { // -z: the original path follows as its own entry
			i++
			if i < len(entries) && entries[i] != "" {
				set[entries[i]] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// addActualDiff adds the checkout's real changes to a review's task profile and keeps the result as the profile of record, so the
// receipt hashes (and later re-validates) exactly what the classification saw.
func (a *auto) addActualDiff(tp *TaskProfile) error {
	diff, base := a.in.Diff, a.in.Base
	if diff == nil {
		diff = gitChangedFiles
	}
	if base == "" {
		base = defaultReviewBase
	}
	revision, err := reviewRevision(a.c.Worktree, base)
	if err != nil {
		return refuse(CodeClassification, "cannot bind review revision: %v", err)
	}
	actual, err := diff(a.c.Worktree, base)
	if err != nil {
		return refuse(CodeClassification, "cannot compute the actual diff against %s (the tier follows what changed; pass --base if the branch is based elsewhere): %v", base, err)
	}
	if len(actual) == 0 {
		return refuse(CodeClassification, "the checkout has no changes against %s: there is nothing to review", base)
	}
	if after, err := reviewRevision(a.c.Worktree, base); err != nil || after != revision {
		return refuse(CodeClassification, "checkout changed while computing review diff")
	}
	a.reviewBase, a.reviewRevision = base, revision
	tp.ChangedFiles = append(tp.ChangedFiles, actual...)
	raw, err := json.Marshal(tp)
	if err != nil {
		return err
	}
	a.profileRaw = raw
	return nil
}

// reviewRevision binds both committed and pending contents, including untracked files and same-path edits.
func reviewRevision(worktree, base string) (string, error) {
	h := sha256.New()
	git := func(args ...string) ([]byte, error) {
		out, err := exec.Command("git", append([]string{"-C", worktree}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("review revision git %s: %w", strings.Join(args, " "), err)
		}
		h.Write(out)
		h.Write([]byte{0})
		return out, nil
	}
	for _, args := range [][]string{{"rev-parse", "--verify", base + "^{commit}"}, {"rev-parse", "HEAD"}, {"diff", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", base + "...HEAD"}, {"diff", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "HEAD"}, {"diff", "--cached", "--no-ext-diff", "--no-textconv", "--binary", "--full-index"}} {
		if _, err := git(args...); err != nil {
			return "", err
		}
	}
	files, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	for _, name := range strings.Split(string(files), "\x00") {
		if name == "" {
			continue
		}
		path := filepath.Join(worktree, name)
		st, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		var b []byte
		if st.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(path)
			b, err = []byte(target), e
		} else if st.Mode().IsRegular() {
			b, err = os.ReadFile(path)
		} else {
			return "", fmt.Errorf("unsupported review file %s", name)
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", name, st.Mode(), len(b))
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
