package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
)

// dagCommitAuthor is the fixed identity of supervisor-made worker commits, so
// the commit never depends on the user's or the worker's git configuration.
const dagCommitAuthor = "rein-supervisor"
const dagCommitEmail = "rein-supervisor@localhost"

// commitDAGWorkerResult commits a finished worker's edits on its task branch
// before the attempt checkpoint is finalized. Agents differ in whether they
// commit at all, so the supervisor makes one commit itself: every agent then
// leaves the same shape (HEAD on top of base, clean tree) for checkpoint
// verification and `rein dag integrate`. Every changed path must match the
// contract's allow globs and none of its deny globs; anything else refuses the
// commit and leaves the tree untouched for the human. A worker that committed
// its own edits and left the tree clean is not committed again.
func commitDAGWorkerResult(c *contract.Contract, worktree, task, attempt string) (committed bool, err error) {
	status, err := dagGit(worktree, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return false, err
	}
	var paths []string
	for _, entry := range strings.Split(status, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 4 {
			return false, errors.New("worker commit refused: invalid git status")
		}
		xy, path := entry[:2], entry[3:]
		// Renames and copies carry a second NUL-separated path; refuse them
		// rather than mis-attribute ownership of either side.
		if xy[0] == 'R' || xy[0] == 'C' {
			return false, fmt.Errorf("worker commit refused: rename or copy of %s is unsupported", path)
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return false, nil
	}
	for _, path := range paths {
		if glob.Match(path, c.Deny) || !glob.Match(path, c.Allow) {
			return false, fmt.Errorf("worker commit refused: %s is outside the task contract", path)
		}
	}
	// Literal pathspecs: a worker-created file name must never act as a glob or
	// magic pathspec when it is staged.
	if _, err := dagGit(worktree, nil, append([]string{"--literal-pathspecs", "add", "-A", "--"}, paths...)...); err != nil {
		return false, err
	}
	msg := fmt.Sprintf("rein: %s result (%s)\n\nCommitted by the rein supervisor after the worker exited.", task, attempt)
	env := []string{
		"GIT_AUTHOR_NAME=" + dagCommitAuthor, "GIT_AUTHOR_EMAIL=" + dagCommitEmail,
		"GIT_COMMITTER_NAME=" + dagCommitAuthor, "GIT_COMMITTER_EMAIL=" + dagCommitEmail,
	}
	if _, err := dagGit(worktree, env, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--no-verify", "-q", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

func dagGit(worktree string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
