package supervisor

import (
	"bytes"
	"errors"
	"os"

	"github.com/voravitl/rein/internal/run"
)

func dagInvocations(p Plan, raw []byte) (map[string]*GraphInvocation, error) {
	var lifecycle bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e dagEntry
		if err := strictDecode(line, &e); err != nil {
			return nil, err
		}
		if e.Phase != "lifecycle" {
			continue
		}
		if e.Task != "" || e.Attempt != "" || e.OpID != "" || e.Error != "" {
			return nil, errors.New("invalid DAG lifecycle envelope")
		}
		b := mustJSON(graphEntry{Phase: e.Phase, Operation: e.Operation, Data: e.Data})
		lifecycle.Write(b)
		lifecycle.WriteByte('\n')
	}
	return (graphLog{}).invocations(p, lifecycle.Bytes())
}
func (l *dagLog) startInvocation(p Plan, hash, id string, c *run.Controller) (*GraphInvocationEvent, error) {
	if id == "" {
		return nil, nil
	}
	if !validDigest(id) {
		return nil, errors.New("invocation ID must be 64 lowercase hex characters")
	}
	if err := c.Check(); err != nil {
		return nil, err
	}
	repo, marker, _, err := invocationBinding(p, hash)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err
	}
	found, err := dagInvocations(p, raw)
	if err != nil {
		return nil, err
	}
	if found[id] != nil {
		return nil, errors.New("DAG invocation ID reused")
	}
	birth, err := run.ProcStart(os.Getpid())
	if err != nil || birth <= 0 {
		return nil, errors.New("DAG controller process identity unavailable")
	}
	event := &GraphInvocationEvent{GraphInvocationIdentity: GraphInvocationIdentity{id, p.Run, hash, repo, marker, os.Getpid(), birth}}
	if err := l.append(dagEntry{Phase: "lifecycle", Operation: "invocation_start", Data: mustJSON(event)}); err != nil {
		return nil, err
	}
	return event, nil
}
func (l *dagLog) returnInvocation(start *GraphInvocationEvent, c *run.Controller, out GraphResult, returned error) error {
	if start == nil {
		return nil
	}
	if err := c.Check(); err != nil {
		return err
	}
	event := *start
	event.Result = &out
	event.Outcome = "graph_returned"
	code := 0
	if returned != nil {
		code = 1
		event.Error = returned.Error()
	}
	event.CLIExit = &code
	return l.append(dagEntry{Phase: "lifecycle", Operation: "invocation_return", Data: mustJSON(event)})
}
func readDAGInvocation(p Plan, hash, id string) (GraphInvocation, error) {
	var empty GraphInvocation
	authorized, h, err := Decode(p.raw)
	if err != nil {
		return empty, err
	}
	if !validDigest(id) || h != hash || !bytes.Equal(mustJSON(p), mustJSON(authorized)) {
		return empty, errors.New("invocation requires exact unchanged schema 3 plan and ID")
	}
	_, _, graph, err := invocationBinding(p, hash)
	if err != nil {
		return empty, err
	}
	log := &dagLog{path: graph.path}
	before, err := os.ReadFile(log.path)
	if err != nil {
		return empty, err
	}
	if _, err := log.read(p, hash, true); err != nil {
		return empty, err
	}
	after, err := os.ReadFile(log.path)
	if err != nil {
		return empty, err
	}
	if !bytes.Equal(before, after) {
		return empty, errors.New("DAG journal changed during observation")
	}
	found, err := dagInvocations(p, after)
	if err != nil {
		return empty, err
	}
	if found[id] == nil {
		return empty, errors.New("DAG invocation not recorded")
	}
	return *found[id], nil
}
