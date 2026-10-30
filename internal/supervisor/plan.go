// Package supervisor executes explicitly authorized bounded run plans.
package supervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/run"
)

type Task struct {
	Kind             string              `json:"kind,omitempty"`
	Action           *ActionSpec         `json:"action,omitempty"`
	Inputs           []ArtifactInput     `json:"inputs,omitempty"`
	Outputs          []ArtifactOutput    `json:"outputs,omitempty"`
	Task             string              `json:"task"`
	Attempt          string              `json:"attempt"`
	CurrentAttempt   string              `json:"current_attempt"`
	OrcaTask         string              `json:"orca_task"`
	Dispatch         string              `json:"dispatch,omitempty"`
	ProcessPolicy    string              `json:"process_policy,omitempty"`
	ContractHash     string              `json:"contract_hash,omitempty"`
	DependsOn        []string            `json:"depends_on,omitempty"`
	Profile          json.RawMessage     `json:"profile,omitempty"`
	ConfigPath       string              `json:"config_path,omitempty"`
	ConfigDigest     string              `json:"config_digest,omitempty"`
	Base             string              `json:"base,omitempty"`
	AllowedUntracked []string            `json:"allowed_untracked,omitempty"`
	Launches         map[string][]string `json:"launches,omitempty"`
	MaxAttempts      int                 `json:"max_attempts,omitempty"`
	Verify           [][]string          `json:"verify,omitempty"`
	// Calibration pins a schema-3 worker to its single launch provider through
	// the owner-authorized calibration lane (unscored, capped), for workers that
	// have no qualifying ledger evidence yet. Nil means scored selection.
	Calibration *TaskCalibration `json:"calibration,omitempty"`
}

// TaskCalibration is the declared worst case of the one worker call, in the
// provider's billing unit. The owner's selection.calibration caps bound it.
type TaskCalibration struct {
	Amount float64 `json:"amount"`
}

type Plan struct {
	raw            []byte
	Execution      *ExecutionPolicy `json:"execution,omitempty"`
	Schema         int              `json:"schema"`
	Run            string           `json:"run"`
	Repo           string           `json:"repo"`
	Terminal       string           `json:"terminal"`
	Source         string           `json:"source"`
	Version        string           `json:"version"`
	MaxChecks      int              `json:"max_checks"`
	TimeoutMS      int              `json:"timeout_ms"`
	PollIntervalMS int              `json:"poll_interval_ms"`
	Pools          []string         `json:"pools"`
	Tasks          []Task           `json:"tasks"`
}

// Load binds authorization to the exact file bytes, including whitespace.
func Load(path string) (Plan, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, "", err
	}
	return Decode(b)
}

func Decode(b []byte) (Plan, string, error) {
	var p Plan
	if len(b) > 1<<20 {
		return p, "", errors.New("plan exceeds 1 MiB")
	}
	// encoding/json otherwise silently accepts duplicate object keys.
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(b))); err != nil {
		return p, "", err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, "", err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, "", errors.New("plan must contain one JSON object")
	}
	if (p.Schema != 1 && p.Schema != 2 && p.Schema != 3) || !run.ValidName(p.Run) || !filepath.IsAbs(p.Repo) || filepath.Clean(p.Repo) != p.Repo {
		return p, "", errors.New("plan requires schema 1, 2 or 3, valid run and absolute clean repository")
	}
	if strings.TrimSpace(p.Terminal) == "" || strings.TrimSpace(p.Source) == "" || strings.TrimSpace(p.Version) == "" {
		return p, "", errors.New("terminal, source and version are required")
	}
	if p.MaxChecks < 1 || p.MaxChecks > 10000 || p.TimeoutMS < 1 || p.TimeoutMS > 3600000 || p.PollIntervalMS < 1 || p.PollIntervalMS > p.TimeoutMS {
		return p, "", errors.New("invalid bounded check, timeout or poll budget")
	}
	seen := map[string]bool{}
	for _, pool := range p.Pools {
		if strings.TrimSpace(pool) == "" || seen[pool] {
			return p, "", errors.New("pools must be nonempty and unique")
		}
		seen[pool] = true
	}
	if len(p.Tasks) == 0 || len(p.Tasks) > 1000 {
		return p, "", errors.New("plan requires 1 to 1000 task bindings")
	}
	if p.Schema != 3 {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(b, &fields)
		if _, present := fields["execution"]; present {
			return p, "", errors.New("execution policy requires schema 3")
		}
		var taskFields []map[string]json.RawMessage
		_ = json.Unmarshal(fields["tasks"], &taskFields)
		for _, task := range taskFields {
			for _, key := range []string{"kind", "action", "inputs", "outputs", "calibration"} {
				if _, present := task[key]; present {
					return p, "", errors.New("action and artifact fields require schema 3")
				}
			}
		}
		for _, t := range p.Tasks {
			if t.Kind != "" || t.Action != nil || len(t.Inputs) != 0 || len(t.Outputs) != 0 || t.Calibration != nil {
				return p, "", errors.New("action and artifact fields require schema 3")
			}
		}
	}
	seen = map[string]bool{}
	for _, t := range p.Tasks {
		if p.Schema >= 2 {
			break
		}
		if t.ProcessPolicy != "" || t.ContractHash != "" || t.ConfigPath != "" || len(t.Profile) > 0 || len(t.DependsOn) > 0 || len(t.Launches) > 0 || t.MaxAttempts != 0 || len(t.Verify) > 0 || t.ConfigDigest != "" || t.Base != "" || len(t.AllowedUntracked) > 0 {
			return p, "", errors.New("graph fields require schema 2")
		}
		if !run.ValidName(t.Task) || !run.ValidName(t.Attempt) || !run.ValidName(t.CurrentAttempt) || strings.TrimSpace(t.OrcaTask) == "" || strings.TrimSpace(t.Dispatch) == "" {
			return p, "", errors.New("invalid task/attempt/dispatch binding")
		}
		for _, id := range []string{"task:" + t.Task, "dispatch:" + t.Dispatch, "orca:" + t.OrcaTask} {
			if seen[id] {
				return p, "", fmt.Errorf("duplicate binding %s", id)
			}
			seen[id] = true
		}
	}
	if p.Schema == 2 {
		if err := validateGraph(p); err != nil {
			return p, "", err
		}
	}
	if p.Schema == 3 {
		if err := validateDAGFields(b); err != nil {
			return p, "", err
		}
		if err := validateDAG(p); err != nil {
			return p, "", err
		}
	}
	h := sha256.Sum256(b)
	p.raw = append([]byte(nil), b...)
	return p, hex.EncodeToString(h[:]), nil
}

func uniqueKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("unexpected JSON delimiter")
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
		}
		if err := uniqueKeys(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
