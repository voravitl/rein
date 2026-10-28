package supervisor

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"
)

type dagTimeEntry struct {
	Seq int    `json:"seq"`
	At  string `json:"at"`
}

func dagTimesPath(journal string) string {
	return strings.TrimSuffix(journal, ".jsonl") + ".times.jsonl"
}

// Display telemetry must never affect authoritative append success or replay.
func appendDAGTime(journal string, seq int) {
	raw, err := json.Marshal(dagTimeEntry{Seq: seq, At: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return
	}
	path := dagTimesPath(journal)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return
	} else if err != nil && !os.IsNotExist(err) {
		return
	}
	f, err := openDAGTimingFile(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !dagTimingSingleLink(f, info) {
		return
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return
	}
	// A display sidecar must not alias authoritative state, even when it was
	// replaced by a symlink or hard link before the append.
	protected := []string{journal}
	if index := strings.LastIndex(journal, ".graph3-"); index >= 0 {
		protected = append(protected, journal[:index])
	}
	for _, name := range protected {
		other, err := os.Stat(name)
		if err != nil || os.SameFile(info, other) {
			return
		}
	}
	_, _ = f.Write(append(raw, '\n'))
}

func applyDAGTimes(view *DAGStatus, entries []dagEntry, path string, now time.Time) {
	if len(entries) == 0 {
		return
	}
	raw, err := readDAGStatusFile(path, dagStatusTimingReadLimit(len(entries)))
	if err != nil || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return
	}
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	// Missing or extra telemetry cannot be matched confidently to this snapshot.
	if len(lines) != len(entries) {
		return
	}
	times := make(map[int]time.Time, len(lines))
	for i, line := range lines {
		var e dagTimeEntry
		if err := strictDecode(line, &e); err != nil || e.Seq != i+1 {
			return
		}
		at, err := time.Parse(time.RFC3339Nano, e.At)
		if err != nil || at.IsZero() {
			return
		}
		times[e.Seq] = at
	}
	starts := map[string]int{}
	ends := map[string]dagEntry{}
	for _, e := range entries {
		if e.Phase == "intent" && e.Operation == "prepare" && starts[e.Task] == 0 {
			starts[e.Task] = e.Seq
		}
		if e.Phase == "error" || (e.Phase == "result" && (e.Operation == "finalize" || e.Operation == "prepare")) {
			ends[e.Task] = e
		}
	}
	for i := range view.Nodes {
		n := &view.Nodes[i]
		start, ok := times[starts[n.Task]]
		if !ok {
			continue
		}
		end := now
		// A failed global operation does not establish a terminal time for another
		// task. Only that task's own terminal/error entry can do so.
		if dagStatusTerminal(n.State) {
			e := ends[n.Task]
			matched := e.Phase == "error"
			if e.Phase == "result" && e.Operation == "finalize" {
				var terminal TerminalResult
				matched = strictDecode(e.Data, &terminal) == nil && !terminal.Retry
			} else if e.Phase == "result" && e.Operation == "prepare" {
				var prepared PreparedAttempt
				matched = strictDecode(e.Data, &prepared) == nil && prepared.Park != nil
			}
			if !matched {
				continue
			}
			var ok bool
			end, ok = times[e.Seq]
			if !ok {
				continue
			}
		}
		elapsed := end.Sub(start).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		n.ElapsedMS = &elapsed
	}
}

// Generated rows use fewer than 128 bytes; allow that budget per authoritative
// entry, capped at 4 MiB. Oversized advisory telemetry is treated as unknown.
func dagStatusTimingReadLimit(entries int) int64 {
	const perEntry = 128
	const maximum = 4 << 20
	if entries > maximum/perEntry {
		return maximum
	}
	return int64(entries) * perEntry
}
