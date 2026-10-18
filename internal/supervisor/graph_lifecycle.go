package supervisor

// Invocation telemetry is correlation only. Neither START nor graph_returned
// proves process exit, descendant termination, settlement or controller authority.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

type GraphInvocationIdentity struct {
	InvocationID string `json:"invocation_id"`
	Run          string `json:"run"`
	PlanHash     string `json:"plan_hash"`
	Repo         string `json:"repo"`
	MarkerDigest string `json:"marker_digest"`
	PID          int    `json:"pid"`
	PIDStart     int64  `json:"pid_start"`
}

type GraphInvocationEvent struct {
	GraphInvocationIdentity
	Result  *GraphResult `json:"result,omitempty"`
	Error   string       `json:"error,omitempty"`
	CLIExit *int         `json:"cli_exit,omitempty"`
	Outcome string       `json:"outcome,omitempty"`
}

type GraphInvocation struct {
	Start  GraphInvocationEvent  `json:"start"`
	Return *GraphInvocationEvent `json:"return,omitempty"`
}

func invocationBinding(p Plan, hash string) (repo, markerDigest string, log graphLog, err error) {
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return "", "", log, err
	}
	raw, err := os.ReadFile(loc.Marker)
	if err != nil {
		return "", "", log, err
	}
	m, err := run.Load(loc.Marker)
	if err != nil {
		return "", "", log, err
	}
	repo, err = filepath.EvalSymlinks(p.Repo)
	if err != nil {
		return "", "", log, err
	}
	root, err := filepath.EvalSymlinks(m.Root)
	if err != nil || root != repo || m.Schema != run.SchemaSupervised || m.Run != p.Run || m.SupervisorPlanHash != hash {
		return "", "", log, errors.New("invocation marker identity mismatch")
	}
	// Reject concurrent marker replacement between the two reads.
	current, err := os.ReadFile(loc.Marker)
	if err != nil || !bytes.Equal(raw, current) {
		return "", "", log, errors.New("invocation marker changed during observation")
	}
	return repo, digestBytes(raw), graphLog{path: loc.Marker + ".graph-" + hash + ".jsonl"}, nil
}

func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (l graphLog) invocations(p Plan, raw []byte) (map[string]*GraphInvocation, error) {
	found := map[string]*GraphInvocation{}
	var repo, marker string
	hash := digestBytes(p.raw)
	active := ""
	for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e graphEntry
		if err := strictDecode(line, &e); err != nil {
			return nil, fmt.Errorf("graph journal line %d: %w", i+1, err)
		}
		if e.Phase != "lifecycle" {
			continue
		}
		if repo == "" {
			var err error
			repo, marker, _, err = invocationBinding(p, hash)
			if err != nil {
				return nil, err
			}
		}
		if e.Task != "" || e.Error != "" {
			return nil, errors.New("lifecycle row cannot name a task or outer error")
		}
		var event GraphInvocationEvent
		if err := strictDecode(e.Data, &event); err != nil {
			return nil, err
		}
		id := event.GraphInvocationIdentity
		if !validDigest(id.InvocationID) || id.Run != p.Run || id.PlanHash != hash || id.Repo != repo || id.MarkerDigest != marker || id.PID <= 0 || id.PIDStart <= 0 {
			return nil, errors.New("invalid invocation identity")
		}
		switch e.Operation {
		case "invocation_start":
			if found[id.InvocationID] != nil || event.Result != nil || event.CLIExit != nil || event.Error != "" || event.Outcome != "" {
				return nil, errors.New("duplicate or malformed invocation START")
			}
			found[id.InvocationID] = &GraphInvocation{Start: event}
			active = id.InvocationID
		case "invocation_return":
			prior := found[id.InvocationID]
			if prior == nil || prior.Return != nil || active != id.InvocationID || prior.Start.GraphInvocationIdentity != id || event.Result == nil || event.CLIExit == nil || event.Outcome != "graph_returned" {
				return nil, errors.New("invocation RETURN has no exact unmatched START")
			}
			wantExit := 0
			if event.Error != "" {
				wantExit = 1
			}
			if *event.CLIExit != wantExit {
				return nil, errors.New("invocation CLI exit contradicts returned error")
			}
			if err := validateInvocationResult(p, *event.Result); err != nil {
				return nil, err
			}
			prior.Return = &event
			active = ""
		default:
			return nil, errors.New("unknown invocation lifecycle operation")
		}
	}
	return found, nil
}

func validateInvocationResult(p Plan, r GraphResult) error {
	switch r.State {
	case routing.WaitNone, routing.WaitQuota, routing.WaitAuth, routing.WaitBudget, routing.WaitReview, routing.WaitHuman, routing.WaitBlockedReconciliation:
	default:
		return errors.New("invalid invocation graph result state")
	}
	if r.Checks < 0 || r.Checks > p.MaxChecks {
		return errors.New("invalid invocation graph check count")
	}
	tasks := map[string]bool{}
	for _, t := range p.Tasks {
		tasks[t.Task] = true
	}
	seen := map[string]bool{}
	for _, task := range r.Completed {
		if !tasks[task] || seen[task] {
			return errors.New("invalid invocation completed tasks")
		}
		seen[task] = true
	}
	return nil
}

func (l graphLog) startInvocation(p Plan, hash, id string, c *run.Controller) (*GraphInvocationEvent, error) {
	if id == "" {
		return nil, nil
	}
	if err := c.Check(); err != nil {
		return nil, err
	}
	repo, marker, _, err := invocationBinding(p, hash)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(l.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	previous, err := l.invocations(p, raw)
	if err != nil {
		return nil, err
	}
	if previous[id] != nil {
		return nil, errors.New("supervisor invocation ID was already used")
	}
	start, err := run.ProcStart(os.Getpid())
	if err != nil || start <= 0 {
		return nil, errors.New("supervisor process start identity unavailable")
	}
	event := &GraphInvocationEvent{GraphInvocationIdentity: GraphInvocationIdentity{id, p.Run, hash, repo, marker, os.Getpid(), start}}
	if err := c.Check(); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(event)
	if err = l.append(graphEntry{Phase: "lifecycle", Operation: "invocation_start", Data: data}); err != nil {
		return nil, err
	}
	return event, nil
}

func (l graphLog) returnInvocation(start *GraphInvocationEvent, c *run.Controller, result GraphResult, returned error) error {
	if start == nil {
		return nil
	}
	if err := c.Check(); err != nil {
		return err
	}
	event := *start
	event.Result = &result
	event.Outcome = "graph_returned"
	exit := 0
	if returned != nil {
		event.Error = returned.Error()
		if event.Error == "" {
			event.Error = "supervisor returned an error"
		}
		exit = 1
	}
	event.CLIExit = &exit
	data, _ := json.Marshal(event)
	return l.append(graphEntry{Phase: "lifecycle", Operation: "invocation_return", Data: data})
}

// ReadGraphInvocation observes the existing journal without taking authority or
// writing/fsyncing it. A returned record is not a claim that the process is gone.
func ReadGraphInvocation(p Plan, hash, id string) (GraphInvocation, error) {
	var empty GraphInvocation
	if !validDigest(id) {
		return empty, errors.New("invocation ID must be 64 lowercase hex characters")
	}
	authorized, h, err := Decode(p.raw)
	if err != nil {
		return empty, err
	}
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(authorized)
	if p.Schema != 2 || h != hash || !bytes.Equal(a, b) {
		return empty, errors.New("invocation requires unchanged graph plan")
	}
	_, _, log, err := invocationBinding(p, hash)
	if err != nil {
		return empty, err
	}
	before, err := os.ReadFile(log.path)
	if err != nil {
		return empty, err
	}
	if _, _, err = log.loadForRead(p, true); err != nil {
		return empty, err
	}
	raw, err := os.ReadFile(log.path)
	if err != nil {
		return empty, err
	}
	if !bytes.Equal(before, raw) {
		return empty, errors.New("graph journal changed during observation")
	}
	invocations, err := log.invocations(p, raw)
	if err != nil {
		return empty, err
	}
	found := invocations[id]
	if found == nil {
		return empty, errors.New("supervisor invocation not recorded")
	}
	return *found, nil
}
