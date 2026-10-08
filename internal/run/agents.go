package run

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Subagent binding (ADR 0002 B0 rule 7). PreToolUse(Agent) cannot know the agent_id the new subagent will get, and
// SubagentStart carries no prompt or tool_use_id, so the two are paired first-in first-out. State lives in files
// beside the marker, one per entry, so concurrent hooks never rewrite a shared file.

// pendingTTL drops a pending entry whose Agent call never produced a SubagentStart (denied by a permission prompt,
// or failed to start); without it one lost start would block every later task binding.
const pendingTTL = 2 * time.Minute

// ErrAmbiguous means two bindings are pending at once and at least one names a task: FIFO pairing would guess.
var ErrAmbiguous = errors.New("another subagent start is still pending")

type pending struct {
	Session string `json:"session"`
	Task    string `json:"task"` // "" for a read-only subagent
	At      int64  `json:"at"`   // unix nanoseconds
}

// AgentsDir is the directory of binding files for the marker at markerPath.
func AgentsDir(markerPath string) string {
	return filepath.Join(filepath.Dir(markerPath), "rein-run-agents")
}

func listPending(markerPath string, at time.Time) []pendingFile {
	dir := AgentsDir(markerPath)
	ents, _ := os.ReadDir(dir)
	var out []pendingFile
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "pending-") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		var v pending
		if err != nil || json.Unmarshal(b, &v) != nil || at.Sub(time.Unix(0, v.At)) > pendingTTL {
			os.Remove(p) // unreadable or expired
			continue
		}
		out = append(out, pendingFile{path: p, p: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

type pendingFile struct {
	path string
	p    pending
}

// RecordPending notes that an Agent call is about to start a subagent for task ("" = read-only subagent). It fails
// with ErrAmbiguous when another start is pending and either of the two names a task.
func RecordPending(markerPath, session, task string) error {
	at := time.Now()
	conflict := func(list []pendingFile, self string) bool {
		for _, f := range list {
			if f.path == self {
				continue
			}
			if task != "" || f.p.Task != "" {
				return true
			}
		}
		return false
	}
	if conflict(listPending(markerPath, at), "") {
		return ErrAmbiguous
	}
	dir := AgentsDir(markerPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var r [4]byte
	_, _ = rand.Read(r[:])
	path := filepath.Join(dir, fmt.Sprintf("pending-%020d-%s.json", at.UnixNano(), hex.EncodeToString(r[:])))
	b, _ := json.Marshal(pending{Session: session, Task: task, At: at.UnixNano()})
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	if conflict(listPending(markerPath, at), path) { // a concurrent call slipped in between the check and the write
		os.Remove(path)
		return ErrAmbiguous
	}
	return nil
}

func safeID(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// BindStart pairs a started subagent with the oldest pending entry and remembers agentID -> task. bound is false
// when nothing was pending (the subagent was not started through a judged Agent call).
func BindStart(markerPath, agentID string) (task string, bound bool, err error) {
	if agentID == "" {
		return "", false, errors.New("SubagentStart without agent_id")
	}
	for _, f := range listPending(markerPath, time.Now()) {
		if os.Remove(f.path) != nil {
			continue // another start took it
		}
		b, _ := json.Marshal(map[string]string{"agent_id": agentID, "task": f.p.Task})
		return f.p.Task, true, os.WriteFile(filepath.Join(AgentsDir(markerPath), "bound-"+safeID(agentID)+".json"), b, 0o644)
	}
	return "", false, nil
}

// BoundTask returns the task a subagent was bound to ("" and true for a read-only one).
func BoundTask(markerPath, agentID string) (task string, ok bool) {
	b, err := os.ReadFile(filepath.Join(AgentsDir(markerPath), "bound-"+safeID(agentID)+".json"))
	if err != nil {
		return "", false
	}
	var v struct {
		Task string `json:"task"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", false
	}
	return v.Task, true
}

// ClearAgents removes every binding file of the run.
func ClearAgents(markerPath string) { os.RemoveAll(AgentsDir(markerPath)) }
