package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

type actionDocker struct {
	binary, socket, config string
	callFn                 func(context.Context, ...string) ([]byte, error)
}

var actionBackend = localActionDocker

func localActionDocker() (actionDocker, error) {
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_CONFIG"} {
		if os.Getenv(key) != "" {
			return actionDocker{}, fmt.Errorf("action rejects runtime override %s", key)
		}
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		return actionDocker{}, errors.New("local Docker backend unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return actionDocker{}, err
	}
	for _, candidate := range []string{"/var/run/docker.sock", filepath.Join(home, ".docker", "run", "docker.sock")} {
		socket, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(socket)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			config := filepath.Join(contract.IndexDir(), "routes", "dag-docker-client-empty")
			if err := os.MkdirAll(config, 0700); err != nil {
				return actionDocker{}, err
			}
			entries, err := os.ReadDir(config)
			if err != nil || len(entries) != 0 {
				return actionDocker{}, errors.New("Docker client configuration must be empty")
			}
			return actionDocker{binary: binary, socket: socket, config: config}, nil
		}
	}
	return actionDocker{}, errors.New("action requires an existing local Unix Docker socket")
}
func (d actionDocker) command(ctx context.Context, args ...string) *exec.Cmd {
	argv := append([]string{"--host", "unix://" + d.socket, "--config", d.config}, args...)
	cmd := exec.CommandContext(ctx, d.binary, argv...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.WaitDelay = 200 * time.Millisecond
	return cmd
}
func (d actionDocker) call(ctx context.Context, args ...string) ([]byte, error) {
	if d.callFn != nil {
		return d.callFn(ctx, args...)
	}
	cmd := d.command(ctx, args...)
	var capture dispatchReceiptBuffer
	cmd.Stdout = &capture
	cmd.Stderr = &capture
	err := cmd.Run()
	if capture.overflow {
		return nil, errors.New("Docker control output exceeded bound")
	}
	if err != nil {
		return nil, fmt.Errorf("Docker %s: %w: %s", args[0], err, capture.String())
	}
	return []byte(capture.String()), nil
}
func preflightDAGActions(ctx context.Context, p supervisor.Plan) error {
	for _, t := range p.Tasks {
		if t.Kind == "action" {
			if _, err := preflightAction(ctx, t.Action); err != nil {
				return fmt.Errorf("task %s backend: %w", t.Task, err)
			}
		}
	}
	return nil
}

func preflightAction(ctx context.Context, a *supervisor.ActionSpec) (string, error) {
	d, err := actionBackend()
	if err != nil {
		return "", err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	osType, err := d.call(checkCtx, "info", "--format", "{{.OSType}}")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(osType)) != "linux" {
		return "", errors.New("action requires Linux container backend")
	}
	id, err := d.call(checkCtx, "image", "inspect", "--format", "{{.Id}}", a.Sandbox.Image)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(id)) != a.Sandbox.Image {
		return "", errors.New("pinned action image is not present locally")
	}
	daemon, err := d.call(checkCtx, "info", "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(daemon))
	if identity == "" || len(identity) > 256 || strings.ContainsAny(identity, "\n\r\x00") {
		return "", errors.New("local Docker daemon identity unavailable")
	}
	return identity, nil
}

func actionContainerArgs(p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt, name string, argv []string, outputReadOnly bool) ([]string, error) {
	if t.Action == nil || len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, errors.New("invalid action command")
	}
	dir := dagAttemptRoot(p, a)
	args := []string{"create", "--pull=never", "--name", name, "--label", "rein.receipt=" + a.ReceiptID, "--label", "rein.plan=" + a.PlanHash,
		"--network", "none", "--read-only", "--user", "65532:65532", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--pids-limit", "64", "--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1", "--ipc", "none",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16777216,mode=1777", "--ulimit", fmt.Sprintf("fsize=%d:%d", t.Action.MaxOutputTotalBytes, t.Action.MaxOutputTotalBytes), "--ulimit", "nofile=128:128",
		"--workdir", "/src", "--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"}
	for _, mount := range []struct {
		name, target string
		ro           bool
	}{{"source", "/src", true}, {"inputs", "/inputs", true}, {"out", "/out", outputReadOnly}} {
		src := filepath.Join(dir, mount.name)
		if strings.ContainsAny(src, ",\n\r") {
			return nil, errors.New("Docker bind path contains separator")
		}
		real, err := filepath.EvalSymlinks(src)
		if err != nil || real != src {
			return nil, errors.New("Docker mount roots must be canonical controller directories")
		}
		spec := "type=bind,src=" + src + ",dst=" + mount.target
		if mount.ro {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	args = append(args, "--entrypoint", argv[0], t.Action.Sandbox.Image)
	args = append(args, argv[1:]...)
	return args, nil
}

type actionInspect struct {
	ID     string `json:"Id"`
	Name   string
	Image  string
	Config struct {
		Image, User, WorkingDir string
		Entrypoint, Cmd         []string
		Labels                  map[string]string
	}
	State struct {
		Running, Paused, Restarting, Dead, OOMKilled bool
		ExitCode                                     int
		Error                                        string
		StartedAt, FinishedAt                        string
	}
	HostConfig struct {
		NetworkMode, IpcMode, PidMode                           string
		ReadonlyRootfs, Privileged, PublishAllPorts, AutoRemove bool
		CapDrop, CapAdd, SecurityOpt, Binds                     []string
		Memory, MemorySwap, NanoCpus                            int64
		PidsLimit                                               *int64
		Devices                                                 []json.RawMessage
		PortBindings                                            map[string]json.RawMessage
		Tmpfs                                                   map[string]string
		Ulimits                                                 []struct {
			Name       string
			Soft, Hard int64
		}
	}
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
}

func inspectOwnedAction(ctx context.Context, d actionDocker, id, name string, a supervisor.PreparedAttempt) (actionInspect, error) {
	var empty actionInspect
	if a.Action == nil {
		return empty, errors.New("action receipt missing")
	}
	daemon, err := d.call(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return empty, err
	}
	if strings.TrimSpace(string(daemon)) != a.Action.DaemonID {
		return empty, errors.New("local Docker daemon identity changed")
	}
	raw, err := d.call(ctx, "inspect", id)
	if err != nil {
		return empty, err
	}
	var list []actionInspect
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
		return empty, errors.New("invalid owned-container inspection")
	}
	e := list[0]
	if e.ID != id || e.Name != "/"+name || e.Config.Labels["rein.receipt"] != a.ReceiptID || e.Config.Labels["rein.plan"] != a.PlanHash {
		return empty, errors.New("container ownership mismatch")
	}
	return e, nil
}
func verifyActionPolicy(p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt, e actionInspect, argv []string, ro bool) error {
	h := e.HostConfig
	if e.Image != t.Action.Sandbox.Image || e.Config.Image != t.Action.Sandbox.Image || e.Config.User != "65532:65532" || e.Config.WorkingDir != "/src" || !bytes.Equal(dagEncode(e.Config.Entrypoint), dagEncode(argv[:1])) || (len(e.Config.Cmd) != len(argv)-1) {
		return errors.New("container command/image identity changed")
	}
	for i, arg := range e.Config.Cmd {
		if arg != argv[i+1] {
			return errors.New("container argv changed")
		}
	}
	if h.NetworkMode != "none" || h.IpcMode != "none" || h.PidMode != "" || !h.ReadonlyRootfs || h.Privileged || h.PublishAllPorts || h.AutoRemove || len(h.Devices) != 0 || len(h.CapAdd) != 0 || len(h.Binds) != 0 || len(h.PortBindings) != 0 || h.Memory != 268435456 || h.MemorySwap != 268435456 || h.NanoCpus != 1000000000 || h.PidsLimit == nil || *h.PidsLimit != 64 || len(h.CapDrop) != 1 || strings.ToUpper(h.CapDrop[0]) != "ALL" || len(h.SecurityOpt) != 1 || (h.SecurityOpt[0] != "no-new-privileges:true" && h.SecurityOpt[0] != "no-new-privileges") {
		return errors.New("container containment policy mismatch")
	}
	if len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != "rw,noexec,nosuid,nodev,size=16777216,mode=1777" {
		return errors.New("container scratch policy mismatch")
	}
	limits := map[string]int64{"fsize": t.Action.MaxOutputTotalBytes, "nofile": 128}
	for _, limit := range h.Ulimits {
		want, ok := limits[limit.Name]
		if !ok || limit.Soft != want || limit.Hard != want {
			return errors.New("container file bounds changed")
		}
		delete(limits, limit.Name)
	}
	if len(limits) != 0 {
		return errors.New("container file bounds missing")
	}
	if len(e.Mounts) != 3 {
		return errors.New("container has unexpected mounts")
	}
	expected := map[string]struct {
		source string
		rw     bool
	}{"/src": {filepath.Join(dagAttemptRoot(p, a), "source"), false}, "/inputs": {filepath.Join(dagAttemptRoot(p, a), "inputs"), false}, "/out": {filepath.Join(dagAttemptRoot(p, a), "out"), !ro}}
	for _, m := range e.Mounts {
		want, ok := expected[m.Destination]
		if !ok || m.Type != "bind" || m.Source != want.source || m.RW != want.rw {
			return errors.New("container mount identity mismatch")
		}
		delete(expected, m.Destination)
	}
	if len(expected) != 0 {
		return errors.New("container missing fixed mounts")
	}
	return nil
}

type actionCapture struct {
	mu       sync.Mutex
	b        bytes.Buffer
	max      int64
	overflow bool
	stop     context.CancelFunc
}

func (b *actionCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.max - int64(b.b.Len())
	if int64(n) > left {
		if left > 0 {
			b.b.Write(p[:int(left)])
		}
		b.overflow = true
		b.stop()
		return n, nil
	}
	b.b.Write(p)
	return n, nil
}

type actionExecution struct {
	Run, PlanHash, Task, Attempt, ReceiptID, SourceDigest, InputDigest string
	Name, ContainerID                                                  string
	Argv                                                               []string
	ExitCode                                                           int
	Output                                                             string
	AttachError                                                        string
	Truncated, TimedOut, Cancelled, Gone, Removed                      bool
	Inspection                                                         actionInspect
}

func runActionContainer(ctx context.Context, control *run.Controller, p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt, name string, argv []string, ro bool) (e actionExecution, err error) {
	e = actionExecution{Run: p.Run, PlanHash: a.PlanHash, Task: t.Task, Attempt: a.Attempt, ReceiptID: a.ReceiptID, SourceDigest: a.SourceDigest, InputDigest: a.InputDigest, Name: name, Argv: argv}
	c, err := contract.Load(t.Task)
	if err != nil {
		return e, err
	}
	if err := graphLaunchAuthority(control, c, p.Run); err != nil {
		return e, err
	}
	d, err := actionBackend()
	if err != nil {
		return e, err
	}
	args, err := actionContainerArgs(p, t, a, name, argv, ro)
	if err != nil {
		return e, err
	}
	if err := dagWrite(filepath.Join(dagAttemptRoot(p, a), name+"-intent.json"), args); err != nil {
		return e, err
	}
	// Create may have succeeded despite a lost response. A failure leaves the
	// outer execute intent fenced; never guess an ID or start by container name.
	raw, err := d.call(ctx, args...)
	if err != nil {
		return e, err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return e, errors.New("Docker returned invalid container ID")
	}
	e.ContainerID = id
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		inspection, cleanupErr := inspectOwnedAction(cleanupCtx, d, id, name, a)
		if cleanupErr == nil && (inspection.State.Running || inspection.State.Restarting || inspection.State.Paused) {
			_, cleanupErr = d.call(cleanupCtx, "kill", "--signal", "KILL", id)
		}
		if cleanupErr == nil {
			inspection, cleanupErr = inspectOwnedAction(cleanupCtx, d, id, name, a)
		}
		if cleanupErr == nil && (inspection.State.Running || inspection.State.Paused || inspection.State.Restarting) {
			cleanupErr = errors.New("owned container still running")
		}
		if cleanupErr == nil {
			e.Gone = true
			e.Inspection = inspection
			e.ExitCode = inspection.State.ExitCode
			// Preserve cleanup even when final inspection invalidates success.
			err = errors.Join(err, verifyActionPolicy(p, t, a, inspection, argv, ro), actionTerminated(inspection))
			_, cleanupErr = d.call(cleanupCtx, "rm", id)
		}
		if cleanupErr == nil {
			var found []byte
			found, cleanupErr = d.call(cleanupCtx, "ps", "-a", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
			if cleanupErr == nil && strings.TrimSpace(string(found)) != "" {
				cleanupErr = errors.New("owned container remains after removal")
			}
			if cleanupErr == nil {
				e.Removed = true
			}
		}
		err = errors.Join(err, cleanupErr)
		if e.AttachError != "" && e.ExitCode == 0 && !e.TimedOut && !e.Truncated && !e.Cancelled {
			err = errors.Join(err, errors.New("Docker attach failed without a failed container exit"))
		}
	}()
	if err := dagWrite(filepath.Join(dagAttemptRoot(p, a), name+"-container.json"), struct{ ID, Name, Receipt string }{id, name, a.ReceiptID}); err != nil {
		return e, err
	}
	inspectCtx, inspectCancel := context.WithTimeout(ctx, 5*time.Second)
	inspection, err := inspectOwnedAction(inspectCtx, d, id, name, a)
	inspectCancel()
	if err != nil {
		return e, err
	}
	if err := verifyActionPolicy(p, t, a, inspection, argv, ro); err != nil {
		return e, err
	}
	outBefore, err := os.Stat(filepath.Join(dagAttemptRoot(p, a), "out"))
	if err != nil {
		return e, err
	}
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(t.Action.TimeoutMS)*time.Millisecond)
	defer cancel()
	capture := &actionCapture{max: t.Action.MaxOutputBytes, stop: cancel}
	cmd := d.command(execCtx, "start", "--attach", id)
	cmd.Stdout = capture
	cmd.Stderr = capture
	runErr := cmd.Run()
	if runErr != nil {
		e.AttachError = runErr.Error()
	}
	e.Output = capture.b.String()
	e.Truncated = capture.overflow
	e.TimedOut = errors.Is(execCtx.Err(), context.DeadlineExceeded)
	e.Cancelled = ctx.Err() != nil
	outAfter, statErr := os.Stat(filepath.Join(dagAttemptRoot(p, a), "out"))
	if statErr != nil || !os.SameFile(outBefore, outAfter) {
		return e, errors.New("owned output mount root replaced")
	}
	// Nonzero command exits are known only after deferred inspection proves death.
	if runErr != nil && execCtx.Err() == nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			return e, runErr
		}
	}
	return e, nil
}
func actionTerminated(e actionInspect) error {
	start, err := time.Parse(time.RFC3339Nano, e.State.StartedAt)
	if err != nil || start.IsZero() {
		return errors.New("action container start is unproven")
	}
	finish, err := time.Parse(time.RFC3339Nano, e.State.FinishedAt)
	if err != nil || finish.IsZero() || finish.Before(start) || e.State.Running || e.State.Paused || e.State.Restarting {
		return errors.New("action container termination is unproven")
	}
	return nil
}
func validateActionOutputs(root string, a *supervisor.ActionSpec) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	files := 0
	var total int64
	pending := []string{"."}
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		dir, err := r.Open(name)
		if err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(a.MaxOutputFiles - files + 1)
		closeErr := dir.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		files += len(entries)
		if files > a.MaxOutputFiles {
			return errors.New("action output entry count exceeded")
		}
		for _, entry := range entries {
			relative := entry.Name()
			if name != "." {
				relative = name + "/" + relative
			}
			info, err := r.Lstat(relative)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("action output link forbidden")
			}
			if info.IsDir() {
				pending = append(pending, relative)
				continue
			}
			total += info.Size()
			if total > a.MaxOutputTotalBytes {
				return errors.New("action output byte bound exceeded")
			}
			if _, err := supervisor.ReadArtifact(root, relative, a.MaxOutputTotalBytes); err != nil {
				return err
			}
		}
	}
	return nil
}
func executeAction(ctx context.Context, control *run.Controller, p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt) (supervisor.ExecutionResult, error) {
	e, err := runActionContainer(ctx, control, p, t, a, "rein-action-"+a.Attempt, t.Action.Argv, false)
	if err != nil {
		return supervisor.ExecutionResult{}, err
	}
	if !e.Gone || !e.Removed {
		return supervisor.ExecutionResult{}, errors.New("action cleanup unproven")
	}
	if err := verifyActionPolicy(p, t, a, e.Inspection, t.Action.Argv, false); err != nil {
		return supervisor.ExecutionResult{}, err
	}
	if err := dagWrite(filepath.Join(dagAttemptRoot(p, a), "action-execution.json"), e); err != nil {
		return supervisor.ExecutionResult{}, err
	}
	state := supervisor.Completed
	if e.ExitCode != 0 || e.Truncated || e.TimedOut || e.Cancelled || e.Inspection.State.OOMKilled || e.Inspection.State.Error != "" {
		state = supervisor.Failed
	}
	d := supervisor.Dispatch{Task: t.Task, Attempt: a.Attempt, Decision: a.ReceiptID, Dispatch: a.ReceiptID, Worktree: a.Worktree, OrcaTask: "docker:" + e.ContainerID}
	return supervisor.ExecutionResult{Dispatch: d, Observation: supervisor.Observation{State: state, Evidence: graphHash(dagEncode(e))}, Evidence: dagEncode(e), Cancelled: e.Cancelled}, nil
}
func loadActionExecution(p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt) (actionExecution, []byte, error) {
	var e actionExecution
	raw, err := supervisor.ReadArtifact(dagAttemptRoot(p, a), "action-execution.json", 8<<20)
	if err != nil {
		return e, nil, err
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, nil, err
	}
	if e.Run != p.Run || e.PlanHash != a.PlanHash || e.Task != t.Task || e.Attempt != a.Attempt || e.ReceiptID != a.ReceiptID || e.SourceDigest != a.SourceDigest || e.InputDigest != a.InputDigest || !e.Gone || !e.Removed || e.Name != "rein-action-"+a.Attempt || e.ContainerID != e.Inspection.ID || e.Inspection.State.Running || e.Inspection.State.Paused || e.Inspection.State.Restarting || !bytes.Equal(dagEncode(e.Argv), dagEncode(t.Action.Argv)) {
		return e, nil, errors.New("action evidence identity or cleanup mismatch")
	}
	if err := verifyActionPolicy(p, t, a, e.Inspection, e.Argv, false); err != nil {
		return e, nil, err
	}
	if err := actionTerminated(e.Inspection); err != nil {
		return e, nil, err
	}
	return e, raw, nil
}
func finalizeAction(ctx context.Context, control *run.Controller, p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt, r supervisor.ExecutionResult) (supervisor.Outcome, error) {
	e, raw, err := loadActionExecution(p, t, a)
	if err != nil {
		return supervisor.Outcome{}, err
	}
	out := supervisor.Outcome{ExecutionDigest: graphHash(raw), CheckpointDigest: a.SourceDigest}
	if r.Observation.Evidence != out.ExecutionDigest || !bytes.Equal(raw, r.Evidence) {
		return out, errors.New("action observation changed")
	}
	if e.ExitCode != 0 || e.Truncated || e.TimedOut || r.Cancelled || r.Observation.State != supervisor.Completed {
		out.State = routing.WaitHuman
		out.Reason = "action failed, exceeded bounds or was cancelled"
		return out, nil
	}
	if err := validateActionOutputs(filepath.Join(dagAttemptRoot(p, a), "out"), t.Action); err != nil {
		out.State = routing.WaitHuman
		out.Reason = err.Error()
		return out, nil
	}
	checks := make([]actionExecution, 0, len(t.Verify))
	for i, argv := range t.Verify {
		check, err := runActionContainer(ctx, control, p, t, a, fmt.Sprintf("rein-action-%s-verify-%d", a.Attempt, i), argv, true)
		if err != nil {
			return out, err
		}
		checks = append(checks, check)
		if check.ExitCode != 0 || check.Truncated || check.TimedOut || check.Cancelled || !check.Gone || !check.Removed {
			out.State = routing.WaitHuman
			out.Reason = "action objective verification failed"
			break
		}
	}
	verification := dagEncode(checks)
	if err := supervisor.WriteImmutable(filepath.Join(dagAttemptRoot(p, a), "action-verification.json"), verification, 0400); err != nil {
		return out, err
	}
	out.VerificationDigest = graphHash(verification)
	return out, nil
}
func revalidateAction(p supervisor.Plan, t supervisor.Task, a supervisor.PreparedAttempt, out supervisor.Outcome) error {
	e, raw, err := loadActionExecution(p, t, a)
	if err != nil {
		return err
	}
	if graphHash(raw) != out.ExecutionDigest || out.CheckpointDigest != a.SourceDigest || e.ExitCode != 0 || e.Truncated || e.TimedOut || e.Cancelled || e.Inspection.State.OOMKilled || e.Inspection.State.Error != "" {
		return errors.New("successful action execution evidence changed")
	}
	raw, err = supervisor.ReadArtifact(dagAttemptRoot(p, a), "action-verification.json", 4<<20)
	if err != nil {
		return err
	}
	if graphHash(raw) != out.VerificationDigest {
		return errors.New("action verification digest changed")
	}
	var checks []actionExecution
	if err := json.Unmarshal(raw, &checks); err != nil {
		return err
	}
	if len(checks) != len(t.Verify) {
		return errors.New("action verification count changed")
	}
	for i, check := range checks {
		if check.Run != p.Run || check.PlanHash != a.PlanHash || check.Task != t.Task || check.Attempt != a.Attempt || check.ReceiptID != a.ReceiptID || check.SourceDigest != a.SourceDigest || check.InputDigest != a.InputDigest || check.ExitCode != 0 || check.Truncated || check.TimedOut || check.Cancelled || !check.Gone || !check.Removed || check.ContainerID != check.Inspection.ID || check.Name != fmt.Sprintf("rein-action-%s-verify-%d", a.Attempt, i) || check.Inspection.State.Running || check.Inspection.State.OOMKilled || check.Inspection.State.Error != "" {
			return errors.New("action verification identity changed")
		}
		if err := verifyActionPolicy(p, t, a, check.Inspection, t.Verify[i], true); err != nil {
			return err
		}
	}
	return nil
}

var _ io.Writer = (*actionCapture)(nil)
