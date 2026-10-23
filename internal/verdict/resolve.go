package verdict

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// ResolveCommit freezes refs (including HEAD and tags) before recording or checking authority.
func ResolveCommit(repo, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	cmd.Dir = repo
	out, err := cmd.Output()
	sha := strings.TrimSpace(string(out))
	if err != nil || !immutableCommitID(sha) {
		return "", fmt.Errorf("cannot resolve commit %q", ref)
	}
	return sha, nil
}

func immutableCommitID(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}
