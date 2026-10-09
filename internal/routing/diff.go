package routing

// The actual changes of a checkout, for the review phase: a task's tier follows what changed, not what the coordinator says
// changed, so the review's changed files are the ones git reports (plus whatever the coordinator listed: a claim can only add).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
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
	actual, err := diff(a.c.Worktree, base)
	if err != nil {
		return refuse(CodeClassification, "cannot compute the actual diff against %s (the tier follows what changed; pass --base if the branch is based elsewhere): %v", base, err)
	}
	if len(actual) == 0 {
		return refuse(CodeClassification, "the checkout has no changes against %s: there is nothing to review", base)
	}
	tp.ChangedFiles = append(tp.ChangedFiles, actual...)
	raw, err := json.Marshal(tp)
	if err != nil {
		return err
	}
	a.profileRaw = raw
	return nil
}
