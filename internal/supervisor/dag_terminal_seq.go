package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/voravitl/rein/internal/run"
)

// ReadDAGStatusAt projects the strict journal prefix of complete entries
// 1..seq, counting the header and lifecycle rows. It never retries, never
// reports "changing", and fails when the journal is missing or entry seq is
// absent or incomplete. Content after entry seq is not interpreted. Elapsed
// time is unknown because timing telemetry describes the live journal only.
// Like ReadDAGStatus, it is read-only and needs no controller authority.
func ReadDAGStatusAt(ctx context.Context, p Plan, hash string, seq int) (DAGStatus, error) {
	var empty DAGStatus
	if seq < 1 {
		return empty, errors.New("status sequence must be a positive integer")
	}
	authorized, h, err := Decode(p.raw)
	if err != nil {
		return empty, err
	}
	if p.Schema != 3 || h != hash || !bytes.Equal(mustJSON(p), mustJSON(authorized)) {
		return empty, errors.New("status requires unchanged schema 3 plan")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return empty, err
	}
	raw, err := readDAGStatusFile(dagJournalPath(loc.Marker, hash), 0)
	if errors.Is(err, os.ErrNotExist) {
		return empty, fmt.Errorf("status sequence %d requires an existing DAG journal", seq)
	}
	if err != nil {
		return empty, err
	}
	prefix, ok := dagJournalPrefix(raw, seq)
	if !ok {
		return empty, fmt.Errorf("status sequence %d exceeds the DAG journal's %d complete entries", seq, bytes.Count(raw, []byte{'\n'}))
	}
	snapshot, err := parseDAGStatus(p, hash, prefix)
	if err != nil {
		return empty, err
	}
	return projectDAGStatus(p, hash, snapshot), nil
}

// dagJournalPrefix returns the first seq newline-terminated rows of raw. An
// unterminated final row never counts as a complete entry.
func dagJournalPrefix(raw []byte, seq int) ([]byte, bool) {
	end := 0
	for i := 0; i < seq; i++ {
		n := bytes.IndexByte(raw[end:], '\n')
		if n < 0 {
			return nil, false
		}
		end += n + 1
	}
	return raw[:end], true
}
