package run

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/voravitl/rein/internal/glob"
)

// ErrNoProc means the process does not exist.
var ErrNoProc = errors.New("no such process")

// Finding is one audit result.
type Finding struct {
	Kind    string `json:"kind"` // COORDINATOR_DRIFT
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

// AuditOptions tune Audit.
type AuditOptions struct {
	// Pinned are commits the drift check approved (a worker's final sha). A commit reachable from one of them, or
	// equal to one of their commits by per-commit patch-id (a rebase keeps the patch-id), is worker output.
	Pinned []string
}

// git runs git in dir with explicit flags and a clean environment (ADR 0002 rule 4: no user config surprises).
func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, nil, args...)
	return strings.TrimSpace(out), err
}

func gitRaw(dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, e := range os.Environ() {
		switch {
		case strings.HasPrefix(e, "GIT_DIR="), strings.HasPrefix(e, "GIT_WORK_TREE="), strings.HasPrefix(e, "GIT_COMMON_DIR="),
			strings.HasPrefix(e, "GIT_INDEX_FILE="):
		default:
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// mainRoot is the main checkout of the repository that holds dir (the first entry of git worktree list).
func mainRoot(dir string) (string, error) {
	out, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	root, ok := strings.CutPrefix(first, "worktree ")
	if !ok || root == "" {
		return "", errors.New("cannot find the main checkout (bare repositories are out of scope)")
	}
	return root, nil
}

// patchID returns the patch-id (--verbatim) of one commit, "" for an empty diff (which never matches).
func patchID(root, sha string) (string, error) {
	diff, err := gitRaw(root, nil, "diff-tree", "-p", "--root", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", sha)
	if err != nil || strings.TrimSpace(diff) == "" {
		return "", err
	}
	out, err := gitRaw(root, []byte(diff), "patch-id", "--verbatim")
	if err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out), " ")
	return id, nil
}

// Audit reports COORDINATOR_DRIFT for the run of the repository that holds repo: every non-merge commit in
// start..base that is none of: reachable from a pinned sha, equal by patch-id to a pinned commit, touching only
// coordinator_writable paths, or covered by a commit exception the user recorded.
func Audit(repo string, o AuditOptions) ([]Finding, error) {
	_, m, err := load(repo)
	if err != nil {
		return nil, err
	}
	return auditMarker(m, o)
}

func auditMarker(m *Marker, o AuditOptions) ([]Finding, error) {
	args := []string{"rev-list", "--no-merges", "--reverse", m.BaseRef, "^" + m.StartSHA}
	for _, p := range o.Pinned {
		if _, err := git(m.Root, "rev-parse", "--verify", "--quiet", p+"^{commit}"); err != nil {
			return nil, fmt.Errorf("pinned %q is not a commit in %s", p, m.Root)
		}
		args = append(args, "^"+p)
	}
	out, err := git(m.Root, args...)
	if err != nil {
		return nil, err
	}
	pinnedIDs := map[string]bool{}
	if len(o.Pinned) > 0 {
		ps, err := git(m.Root, append(append([]string{"rev-list", "--no-merges"}, o.Pinned...), "^"+m.StartSHA)...)
		if err != nil {
			return nil, err
		}
		for _, sha := range strings.Fields(ps) {
			if id, err := patchID(m.Root, sha); err != nil {
				return nil, err
			} else if id != "" {
				pinnedIDs[id] = true
			}
		}
	}
	var res []Finding
	for _, sha := range strings.Fields(out) {
		if m.Allows("commit", sha) {
			continue
		}
		if len(pinnedIDs) > 0 {
			if id, err := patchID(m.Root, sha); err != nil {
				return nil, err
			} else if pinnedIDs[id] {
				continue
			}
		}
		names, err := gitRaw(m.Root, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--root", "--no-renames", sha)
		if err != nil {
			return nil, err
		}
		var outside []string
		for _, f := range strings.Split(names, "\x00") {
			if f != "" && !glob.Match(f, m.CoordinatorWritable) {
				outside = append(outside, f)
			}
		}
		if len(outside) == 0 {
			continue
		}
		subj, _ := git(m.Root, "log", "-1", "--format=%s", sha)
		res = append(res, Finding{Kind: "COORDINATOR_DRIFT", Commit: sha, Subject: subj,
			Detail: fmt.Sprintf("commit %.12s %q is not worker output and changes files outside coordinator_writable: %s", sha, subj, strings.Join(outside, ", "))})
	}
	return res, nil
}
