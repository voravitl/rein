package supervisor

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"

	"github.com/voravitl/rein/internal/run"
)

type ExecutionPolicy struct {
	MaxParallel   int    `json:"max_parallel"`
	FailurePolicy string `json:"failure_policy"`
}
type ActionSandbox struct {
	Backend string `json:"backend"`
	Image   string `json:"image"`
}
type ActionSpec struct {
	Argv                []string      `json:"argv"`
	TimeoutMS           int           `json:"timeout_ms"`
	MaxOutputBytes      int64         `json:"max_output_bytes"`
	MaxOutputFiles      int           `json:"max_output_files"`
	MaxOutputTotalBytes int64         `json:"max_output_total_bytes"`
	Idempotent          bool          `json:"idempotent"`
	Sandbox             ActionSandbox `json:"sandbox"`
}
type ArtifactOutput struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}
type ArtifactInput struct {
	Producer string `json:"producer"`
	Name     string `json:"name"`
	As       string `json:"as"`
}

func cleanArtifactPath(s string) bool {
	return s != "" && s != "." && !path.IsAbs(s) && path.Clean(s) == s && !strings.ContainsAny(s, "\\:") && !strings.ContainsRune(s, 0) && s != ".." && !strings.HasPrefix(s, "../") && strings.Split(s, "/")[0] != ".git"
}
func fullRevision(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 20 && s == strings.ToLower(s)
}
func validateDAG(p Plan) error {
	if p.Execution == nil || p.Execution.MaxParallel < 1 || p.Execution.MaxParallel > 32 || p.Execution.MaxParallel > len(p.Tasks) || p.Execution.FailurePolicy != "isolate_known" {
		return errors.New("schema 3 requires max_parallel 1..32 within task count and isolate_known")
	}
	tasks := map[string]Task{}
	for _, t := range p.Tasks {
		if len(t.Verify) > 32 {
			return errors.New("DAG verification command bound is 32")
		}
		if !run.ValidName(t.Task) || tasks[t.Task].Task != "" || !validDigest(t.ContractHash) || !fullRevision(t.Base) || t.MaxAttempts < 1 || t.MaxAttempts > 3 {
			return errors.New("DAG requires unique tasks, contract hashes, full source commit SHA and 1..3 attempts")
		}
		if t.Attempt != "" || t.CurrentAttempt != "" || t.OrcaTask != "" || t.Dispatch != "" {
			return errors.New("DAG attempts must be allocated by controller")
		}
		switch t.Kind {
		case "worker":
			if t.Action != nil || t.ProcessPolicy != "non_detaching" {
				return errors.New("worker requires non_detaching policy and no action")
			}
			if c := t.Calibration; c != nil && (len(t.Launches) != 1 || !(c.Amount > 0) || math.IsInf(c.Amount, 0)) {
				return errors.New("calibration worker requires exactly one launch provider and a finite amount > 0")
			}
			legacy := p
			legacy.Tasks = []Task{t}
			legacy.Tasks[0].DependsOn = nil
			legacy.Tasks[0].Calibration = nil
			if err := validateGraph(legacy); err != nil {
				return err
			}
		case "action":
			if t.Action == nil || len(t.Profile) != 0 || t.ConfigPath != "" || t.ConfigDigest != "" || len(t.Launches) != 0 || t.ProcessPolicy != "" || len(t.AllowedUntracked) != 0 || t.Calibration != nil {
				return errors.New("action forbids worker routing fields")
			}
			a := t.Action
			if len(a.Argv) == 0 || !path.IsAbs(a.Argv[0]) || path.Clean(a.Argv[0]) != a.Argv[0] {
				return errors.New("action requires absolute literal executable")
			}
			for _, arg := range a.Argv {
				if strings.ContainsRune(arg, 0) {
					return errors.New("NUL in action argv")
				}
			}
			if a.TimeoutMS < 1 || a.TimeoutMS > p.TimeoutMS || a.MaxOutputBytes < 1 || a.MaxOutputBytes > 1<<20 || a.MaxOutputFiles < 1 || a.MaxOutputFiles > 1000 || a.MaxOutputTotalBytes < 1 || a.MaxOutputTotalBytes > 64<<20 || (t.MaxAttempts > 1 && !a.Idempotent) {
				return errors.New("invalid action bounds or nonidempotent retry")
			}
			if a.Sandbox.Backend != "docker" || !strings.HasPrefix(a.Sandbox.Image, "sha256:") || !validDigest(strings.TrimPrefix(a.Sandbox.Image, "sha256:")) {
				return errors.New("action requires Docker and immutable local image ID")
			}
			if len(t.Verify) == 0 {
				return errors.New("action requires objective verification argv")
			}
			for _, argv := range t.Verify {
				if len(argv) == 0 || !path.IsAbs(argv[0]) {
					return errors.New("action verifier requires absolute executable")
				}
				for _, arg := range argv {
					if strings.ContainsRune(arg, 0) {
						return errors.New("NUL in verifier")
					}
				}
			}
		default:
			return errors.New("schema 3 task kind must be worker or action")
		}
		names, paths := map[string]bool{}, map[string]bool{}
		var total int64
		for _, o := range t.Outputs {
			if !run.ValidName(o.Name) || o.Name == "manifest.json" || names[o.Name] || paths[o.Path] || !cleanArtifactPath(o.Path) || o.MaxBytes < 1 || o.MaxBytes > 64<<20 {
				return errors.New("invalid or duplicate artifact output")
			}
			names[o.Name], paths[o.Path] = true, true
			total += o.MaxBytes
		}
		if total > 64<<20 {
			return errors.New("artifact output sum exceeds 64 MiB")
		}
		if t.Action != nil && (total > t.Action.MaxOutputTotalBytes || len(t.Outputs) > t.Action.MaxOutputFiles) {
			return errors.New("declared artifacts exceed action bounds")
		}
		tasks[t.Task] = t
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return errors.New("DAG contains cycle")
		}
		if done[name] {
			return nil
		}
		t, ok := tasks[name]
		if !ok {
			return fmt.Errorf("unknown dependency %s", name)
		}
		visiting[name] = true
		deps := map[string]bool{}
		for _, dep := range t.DependsOn {
			if deps[dep] {
				return errors.New("duplicate dependency")
			}
			deps[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		aliases := map[string]bool{}
		for _, in := range t.Inputs {
			if !deps[in.Producer] || !run.ValidName(in.As) || aliases[in.As] {
				return errors.New("artifact input requires direct dependency and unique alias")
			}
			aliases[in.As] = true
			found := false
			for _, o := range tasks[in.Producer].Outputs {
				if o.Name == in.Name {
					found = true
				}
			}
			if !found {
				return errors.New("artifact input references undeclared output")
			}
		}
		visiting[name], done[name] = false, true
		return nil
	}
	for name := range tasks {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func validateDAGFields(raw []byte) error {
	var plan struct {
		Tasks []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	for _, fields := range plan.Tasks {
		var kind string
		_ = json.Unmarshal(fields["kind"], &kind)
		forbidden := []string{"action"}
		if kind == "action" {
			forbidden = []string{"profile", "config_path", "config_digest", "launches", "process_policy", "allowed_untracked"}
		}
		for _, key := range forbidden {
			if _, present := fields[key]; present {
				return fmt.Errorf("%s task forbids field %s", kind, key)
			}
		}
	}
	return nil
}
