package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

// Operations is implemented by the production adapter, which supplies actual
// routing, dispatch, observation and checkpoint/accounting evidence.
type Operations struct {
	// InvocationID is correlation telemetry only; it never grants authority.
	InvocationID string
	Resume       func(context.Context, *run.Controller, Task, Dispatch, Outcome) error
	Revalidate   func(context.Context, *run.Controller, Task, Dispatch, Outcome) error
	Prepare      func(context.Context, *run.Controller, Task) (*routing.AutoResult, error)
	Launch       func(context.Context, *run.Controller, Task, *routing.Decision, []string) (Dispatch, error)
	Observe      func(context.Context, *run.Controller, Task, Dispatch) (Observation, error)
	Finish       func(context.Context, *run.Controller, Task, Dispatch, Observation) (Outcome, error)
}

type Dispatch struct {
	Task     string `json:"task"`
	Attempt  string `json:"attempt"`
	Decision string `json:"decision"`
	OrcaTask string `json:"orca_task"`
	Dispatch string `json:"dispatch"`
	Worktree string `json:"worktree"`
	Pool     string `json:"pool"`
}

type ObservationState string

const (
	Running   ObservationState = "RUNNING"
	Completed ObservationState = "COMPLETED"
	Quota     ObservationState = "QUOTA"
	Auth      ObservationState = "AUTH"
	Failed    ObservationState = "FAILED"
)

type Observation struct {
	State    ObservationState `json:"state"`
	Evidence string           `json:"evidence"`
}

type Outcome struct {
	ExecutionDigest    string            `json:"execution_digest,omitempty"`
	CheckpointDigest   string            `json:"checkpoint_digest"`
	VerificationDigest string            `json:"verification_digest,omitempty"`
	State              routing.WaitState `json:"state,omitempty"`
	Reason             string            `json:"reason,omitempty"`
}

type GraphResult struct {
	Status    string            `json:"status,omitempty"`
	Failed    []TaskStatus      `json:"failed,omitempty"`
	Blocked   []TaskStatus      `json:"blocked,omitempty"`
	Cancelled []TaskStatus      `json:"cancelled,omitempty"`
	State     routing.WaitState `json:"state"`
	Reason    string            `json:"reason"`
	Checks    int               `json:"checks"`
	Completed []string          `json:"completed"`
}

func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

func validateGraph(p Plan) error {
	tasks := map[string]Task{}
	for _, t := range p.Tasks {
		if t.ProcessPolicy != "" && t.ProcessPolicy != "non_detaching" {
			return errors.New("unsupported process policy")
		}
		if !run.ValidName(t.Task) || tasks[t.Task].Task != "" {
			return errors.New("graph requires unique valid task names")
		}
		if t.Attempt != "" || t.CurrentAttempt != "" || t.OrcaTask != "" || t.Dispatch != "" {
			return errors.New("graph tasks must not prebind attempts or dispatches")
		}
		if !validDigest(t.ContractHash) || !validDigest(t.ConfigDigest) || !filepath.IsAbs(t.ConfigPath) || filepath.Clean(t.ConfigPath) != t.ConfigPath || strings.TrimSpace(t.Base) == "" || strings.HasPrefix(t.Base, "-") {
			return fmt.Errorf("task %s requires exact contract/config digests, clean absolute config path and base", t.Task)
		}
		if t.MaxAttempts < 1 || t.MaxAttempts > 3 || len(t.Launches) == 0 || len(t.Verify) == 0 {
			return errors.New("graph tasks require 1..3 attempts, launch argv and verification argv")
		}
		var profile map[string]json.RawMessage
		if err := json.Unmarshal(t.Profile, &profile); err != nil || len(profile) == 0 {
			return errors.New("graph task profile must be a nonempty JSON object")
		}
		for provider, argv := range t.Launches {
			if strings.TrimSpace(provider) == "" {
				return errors.New("empty launch provider")
			}
			if err := validateArgv(argv, true); err != nil {
				return err
			}
		}
		for _, argv := range t.Verify {
			if err := validateArgv(argv, false); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, path := range t.AllowedUntracked {
			if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || strings.Split(filepath.ToSlash(path), "/")[0] == ".git" || seen[path] {
				return errors.New("invalid or duplicate allowed untracked path")
			}
			seen[path] = true
		}
		tasks[t.Task] = t
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return errors.New("task dependency graph contains a cycle")
		}
		if done[name] {
			return nil
		}
		task, ok := tasks[name]
		if !ok {
			return fmt.Errorf("unknown dependency %s", name)
		}
		visiting[name] = true
		seen := map[string]bool{}
		for _, dep := range task.DependsOn {
			if seen[dep] {
				return errors.New("duplicate dependency")
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		done[name] = true
		return nil
	}
	for name := range tasks {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func validateArgv(argv []string, templates bool) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" || strings.ContainsAny(argv[0], "{}") {
		return errors.New("argv requires a literal executable")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return errors.New("argv contains NUL")
		}
		if !strings.ContainsAny(arg, "{}") {
			continue
		}
		if templates && (arg == "{model}" || arg == "{effort}" || arg == "{run}" || arg == "{worktree}" || arg == "{model}#{effort}") {
			continue
		}
		return errors.New("unsupported or partial argv placeholder")
	}
	return nil
}

func graphAuthority(ctx context.Context, c *run.Controller, p Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Check(); err != nil {
		return err
	}
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return err
	}
	common, err := filepath.EvalSymlinks(loc.Common)
	if err != nil {
		return err
	}
	for _, t := range p.Tasks {
		ct, err := contract.Load(t.Task)
		if err != nil {
			return err
		}
		if ct.Name != t.Task || routing.ContractHash(ct) != t.ContractHash {
			return fmt.Errorf("task %s contract changed", t.Task)
		}
		if !filepath.IsAbs(ct.Worktree) || filepath.Clean(ct.Worktree) != ct.Worktree {
			return errors.New("contract worktree must be absolute and clean")
		}
		work, err := run.Locate(ct.Worktree)
		if err != nil {
			return err
		}
		wc, err := filepath.EvalSymlinks(work.Common)
		if err != nil || wc != common {
			return errors.New("task contract belongs to another repository")
		}
		if p.Schema == 3 && t.Kind == "action" {
			continue
		}
		b, err := os.ReadFile(t.ConfigPath)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != t.ConfigDigest {
			return fmt.Errorf("task %s provider config changed", t.Task)
		}
	}
	return nil
}

func expandLaunch(t Task, d *routing.Decision, runID string) ([]string, error) {
	argv, ok := t.Launches[d.Provider]
	if !ok {
		return nil, fmt.Errorf("selected provider %s has no authorized launch argv", d.Provider)
	}
	values := map[string]string{"{model}": d.Model, "{effort}": d.Effort, "{run}": runID, "{worktree}": d.Worktree}
	out := append([]string(nil), argv...)
	for i, arg := range out {
		if arg == "{model}#{effort}" {
			provider, model, slash := strings.Cut(d.Model, "/")
			if !slash || provider == "" || model == "" || d.Effort == "" || strings.ContainsAny(d.Model+d.Effort, "#{}") || strings.IndexFunc(d.Model+d.Effort, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
				return nil, errors.New("model/effort placeholder requires provider/model and a nonempty variant")
			}
			out[i] = d.Model + "#" + d.Effort
			continue
		}
		if value, ok := values[arg]; ok {
			if value == "" {
				return nil, fmt.Errorf("empty value for %s", arg)
			}
			out[i] = value
		}
	}
	return out, nil
}

// RunGraph never repeats an operation with an unresolved durable intent. A
// controller restart can resume only from a completely recorded result.
func RunGraph(ctx context.Context, p Plan, hash string, ops Operations) (out GraphResult, err error) {
	out.State = routing.WaitBlockedReconciliation
	if ops.InvocationID != "" && !validDigest(ops.InvocationID) {
		return out, errors.New("invocation ID must be 64 lowercase hex characters")
	}
	authorized, h, err := Decode(p.raw)
	if err != nil {
		return out, err
	}
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(authorized)
	if p.Schema != 2 || h != hash || !bytes.Equal(a, b) {
		return out, errors.New("graph requires unchanged schema 2 authorization")
	}
	p = authorized
	if ops.Resume == nil || ops.Revalidate == nil || ops.Prepare == nil || ops.Launch == nil || ops.Observe == nil || ops.Finish == nil {
		return out, errors.New("graph production operations are required")
	}
	c, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		return out, err
	}
	defer c.Close()
	m := c.Marker()
	repo, err := filepath.EvalSymlinks(p.Repo)
	if err != nil {
		return out, err
	}
	root, err := filepath.EvalSymlinks(m.Root)
	if err != nil || repo != root || m.Run != p.Run {
		return out, errors.New("graph marker identity mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
	defer cancel()
	loc, err := run.Locate(p.Repo)
	if err != nil {
		return out, err
	}
	log := graphLog{path: loc.Marker + ".graph-" + hash + ".jsonl"}
	states, checks, err := log.load(p)
	if err != nil {
		return out, err
	}
	start, err := log.startInvocation(p, hash, ops.InvocationID, c)
	if err != nil {
		return out, err
	}
	// Only ordinary return reaches telemetry. Panic/Goexit leaves START unresolved.
	out, err = runGraphOwned(ctx, p, ops, c, log, states, checks)
	if telemetryErr := log.returnInvocation(start, c, out, err); telemetryErr != nil {
		out.State = routing.WaitBlockedReconciliation
		err = errors.Join(err, fmt.Errorf("persist supervisor invocation return: %w", telemetryErr))
	}
	return out, err
}

func runGraphOwned(ctx context.Context, p Plan, ops Operations, c *run.Controller, log graphLog, states map[string]*graphTaskState, checks int) (out GraphResult, err error) {
	out.State = routing.WaitBlockedReconciliation
	out.Checks = checks
	guard := func() error {
		if err := graphAuthority(ctx, c, p); err != nil {
			return err
		}
		for name, s := range states {
			if s.Stage == "done" {
				var task Task
				for _, t := range p.Tasks {
					if t.Task == name {
						task = t
						break
					}
				}
				if err := ops.Revalidate(ctx, c, task, s.Dispatch, s.Outcome); err != nil {
					return fmt.Errorf("completed task %s evidence invalid: %w", name, err)
				}
			}
			if s.Prepared.Receipt != nil {
				ct, err := contract.Load(name)
				if err != nil {
					return err
				}
				if s.Prepared.Receipt.Worktree != ct.Worktree {
					return errors.New("prepared receipt worktree differs from contract")
				}
			}
		}
		return nil
	}
	invoke := func(t Task, op string, fn func() (any, error)) error {
		if err := guard(); err != nil {
			return err
		}
		if err := log.append(graphEntry{Task: t.Task, Phase: "intent", Operation: op}); err != nil {
			return err
		}
		if err := guard(); err != nil {
			return err
		}
		value, err := fn()
		if err != nil {
			_ = log.append(graphEntry{Task: t.Task, Phase: "error", Operation: op, Error: err.Error()})
			return err
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		entry := graphEntry{Task: t.Task, Phase: "result", Operation: op, Data: raw}
		// Validate before persisting, so invalid adapter evidence leaves the intent blocked.
		next := *states[t.Task]
		if err := next.apply(t, p.Run, entry); err != nil {
			return err
		}
		if op == "prepare" {
			ct, err := contract.Load(t.Task)
			if err != nil {
				return err
			}
			if next.Prepared.Receipt.Worktree != ct.Worktree {
				return errors.New("prepared receipt worktree differs from contract")
			}
			for name, other := range states {
				if name != t.Task && other.SeenAttempts[next.Prepared.Attempt] {
					return errors.New("attempt identity reused across tasks")
				}
			}
		}
		if err := log.append(entry); err != nil {
			return err
		}
		*states[t.Task] = next
		return nil
	}
	for {
		if err := guard(); err != nil {
			return out, err
		}
		out.Completed = nil
		allDone := true
		for _, t := range p.Tasks {
			s := states[t.Task]
			if s.Stage == "done" {
				out.Completed = append(out.Completed, t.Task)
			} else {
				allDone = false
			}
			if s.Stage == "wait" {
				out.State = s.Outcome.State
				out.Reason = s.Outcome.Reason
				return out, nil
			}
		}
		if allDone {
			out.State = routing.WaitHuman
			out.Reason = "all tasks verified; independent review and human approval required"
			return out, nil
		}
		progressed := false
		for _, t := range p.Tasks {
			s := states[t.Task]
			if s.Stage == "done" || s.Stage == "wait" {
				continue
			}
			ready := true
			for _, dep := range t.DependsOn {
				if states[dep].Stage != "done" {
					ready = false
				}
			}
			if !ready {
				continue
			}
			if out.Checks >= p.MaxChecks && s.Stage != "observed" {
				continue
			}
			switch s.Stage {
			case "", "retry":
				if s.Attempts >= t.MaxAttempts {
					out.State = routing.WaitHuman
					out.Reason = "task attempt budget exhausted: " + t.Task
					return out, nil
				}
				if s.Stage == "retry" && ops.Resume != nil {
					if err := ops.Resume(ctx, c, t, s.Dispatch, s.Outcome); err != nil {
						return out, err
					}
				}
				pools, poolErr := routing.Status()
				if poolErr != nil {
					return out, poolErr
				}
				if allExhausted(p.Pools, pools, time.Now()) {
					out.State = routing.WaitQuota
					out.Reason = "all configured pools exhausted"
					return out, nil
				}
				err = invoke(t, "prepare", func() (any, error) { return ops.Prepare(ctx, c, t) })
			case "prepared":
				if s.PreviousDispatch.Attempt != "" && ops.Resume != nil {
					if err := ops.Resume(ctx, c, t, s.PreviousDispatch, s.PreviousOutcome); err != nil {
						return out, err
					}
				}
				argv, e := expandLaunch(t, s.Prepared.Receipt, p.Run)
				if e != nil {
					return out, e
				}
				err = invoke(t, "launch", func() (any, error) { return ops.Launch(ctx, c, t, s.Prepared.Receipt, argv) })
			case "launched":
				err = invoke(t, "observe", func() (any, error) { return ops.Observe(ctx, c, t, s.Dispatch) })
				out.Checks++
			case "observed":
				err = invoke(t, "finish", func() (any, error) { return ops.Finish(ctx, c, t, s.Dispatch, s.Observation) })
			default:
				return out, errors.New("invalid graph stage")
			}
			if err != nil {
				return out, err
			}
			progressed = true
		}
		if !progressed {
			if out.Checks >= p.MaxChecks {
				out.State = routing.WaitHuman
				out.Reason = "bounded graph observation budget exhausted"
				return out, nil
			}
			return out, errors.New("graph has no runnable tasks")
		}
		// Poll only after a full pass, while launch/finish transitions advance immediately.
		pending := false
		for _, s := range states {
			if s.Stage == "launched" && s.Observation.State == Running {
				pending = true
			}
		}
		if pending {
			timer := time.NewTimer(time.Duration(p.PollIntervalMS) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return out, ctx.Err()
			case <-timer.C:
			}
		}
	}
}
