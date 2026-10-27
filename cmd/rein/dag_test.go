package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

func localDAGWorkerFixture(t *testing.T) (*e2eEnv, string, supervisor.Plan, string) {
	t.Helper()
	e, path, p := localGraphFixture(t, "printf 'fixed\\n' > src/a.go", "test \"$(cat src/a.go)\" = fixed")
	t.Cleanup(func() {
		_ = filepath.WalkDir(e.root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = e.wt
	head, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	p.Schema = 3
	p.Execution = &supervisor.ExecutionPolicy{MaxParallel: 1, FailurePolicy: "isolate_known"}
	p.Tasks[0].Kind = "worker"
	p.Tasks[0].Base = strings.TrimSpace(string(head))
	p.Tasks[0].MaxAttempts = 1
	hash := bindDAGPlan(t, e, path, p)
	return e, path, p, hash
}
func bindDAGPlan(t *testing.T, e *e2eEnv, path string, p supervisor.Plan) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := supervisor.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := run.Locate(e.wt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartSupervised(e.wt, p.Run, nil, run.Owner{SessionID: "dag-cli-owner", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Allow(e.wt, "task", "approved DAG fixture"); err != nil {
		t.Fatal(err)
	}
	return hash
}
func TestDAGCLIWorkerRoutingCheckpointAndReplay(t *testing.T) {
	e, path, _, _ := localDAGWorkerFixture(t)
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	var result supervisor.GraphResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if code != 0 || result.Status != "verified" || len(result.Completed) != 1 || result.Completed[0] != "task" {
		t.Fatalf("CLI %d %+v", code, result)
	}
	holds, err := budget.Outstanding()
	if err != nil || len(holds) != 0 {
		t.Fatalf("unsettled %+v %v", holds, err)
	}
	raw, err := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt routing.Decision
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Hold == "" || !routing.ReceiptUsed(receipt.Attempt) {
		t.Fatal("worker did not use real routing receipt/reservation")
	}
	again, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 {
		t.Fatalf("replay %d %s", code, again)
	}
	after, err := os.ReadFile(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json"))
	if err != nil || string(after) != string(raw) {
		t.Fatal("replay prepared another worker")
	}
}

func TestDAGOwnershipRejectsWorktreeAliases(t *testing.T) {
	e, _, p, _ := localDAGWorkerFixture(t)
	alias := filepath.Join(e.root, "worktree-alias")
	if err := os.Symlink(e.wt, alias); err != nil {
		t.Fatal(err)
	}
	copy := *e.c
	copy.Name = "other"
	copy.Worktree = alias
	copy.ReportPath = filepath.Join(e.root, "other-report")
	if _, err := copy.Save(); err != nil {
		t.Fatal(err)
	}
	other := p.Tasks[0]
	other.Task = copy.Name
	other.ContractHash = routing.ContractHash(&copy)
	p.Tasks = append(p.Tasks, other)
	if err := validateLocalDAGPlan(p); err == nil || !strings.Contains(err.Error(), "worktree ownership overlaps") {
		t.Fatalf("ownership alias accepted or misclassified: %v", err)
	}
}

func policyFixture(t *testing.T) (supervisor.Plan, supervisor.Task, supervisor.PreparedAttempt, actionInspect) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(dir, "contracts"))
	p := supervisor.Plan{Run: "run"}
	task := supervisor.Task{Task: "task", Kind: "action", Action: &supervisor.ActionSpec{Argv: []string{"/bin/echo", "$literal; unchanged"}, TimeoutMS: 1000, MaxOutputBytes: 64, MaxOutputFiles: 10, MaxOutputTotalBytes: 4096, Sandbox: supervisor.ActionSandbox{Backend: "docker", Image: "sha256:" + strings.Repeat("a", 64)}}}
	a := supervisor.PreparedAttempt{Run: p.Run, PlanHash: strings.Repeat("b", 64), Task: task.Task, Attempt: strings.Repeat("c", 64), ReceiptID: strings.Repeat("d", 64)}
	for _, name := range []string{"source", "inputs", "out"} {
		if err := os.MkdirAll(filepath.Join(dagAttemptRoot(p, a), name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	var inspect actionInspect
	inspect.ID = strings.Repeat("e", 64)
	inspect.Name = "/rein-action-" + a.Attempt
	inspect.Image = task.Action.Sandbox.Image
	inspect.Config.Image = inspect.Image
	inspect.Config.User = "65532:65532"
	inspect.Config.WorkingDir = "/src"
	inspect.Config.Entrypoint = task.Action.Argv[:1]
	inspect.Config.Cmd = task.Action.Argv[1:]
	inspect.Config.Labels = map[string]string{"rein.receipt": a.ReceiptID, "rein.plan": a.PlanHash}
	h := &inspect.HostConfig
	h.NetworkMode = "none"
	h.IpcMode = "none"
	h.ReadonlyRootfs = true
	h.CapDrop = []string{"ALL"}
	h.SecurityOpt = []string{"no-new-privileges:true"}
	h.Memory = 268435456
	h.MemorySwap = 268435456
	h.NanoCpus = 1000000000
	pids := int64(64)
	h.PidsLimit = &pids
	h.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16777216,mode=1777"}
	h.Ulimits = append(h.Ulimits, struct {
		Name       string
		Soft, Hard int64
	}{"fsize", 4096, 4096}, struct {
		Name       string
		Soft, Hard int64
	}{"nofile", 128, 128})
	for _, mount := range []struct {
		name, target string
		rw           bool
	}{{"source", "/src", false}, {"inputs", "/inputs", false}, {"out", "/out", true}} {
		inspect.Mounts = append(inspect.Mounts, struct {
			Type, Source, Destination string
			RW                        bool
		}{"bind", filepath.Join(dagAttemptRoot(p, a), mount.name), mount.target, mount.rw})
	}
	return p, task, a, inspect
}
func TestActionPolicyLiteralBoundsAndInspection(t *testing.T) {
	p, task, a, inspection := policyFixture(t)
	argv, err := actionContainerArgs(p, task, a, "rein-action-"+a.Attempt, task.Action.Argv, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, required := range []string{"--pull=never", "--network none", "--read-only", "--user 65532:65532", "--cap-drop ALL", "--security-opt no-new-privileges:true", "--pids-limit 64", "--memory 268435456", "--cpus 1", "--entrypoint /bin/echo"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing fixed policy %q", required)
		}
	}
	if argv[len(argv)-1] != "$literal; unchanged" {
		t.Fatal("literal action argument was expanded")
	}
	if err := verifyActionPolicy(p, task, a, inspection, task.Action.Argv, false); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*actionInspect){
		"network":            func(e *actionInspect) { e.HostConfig.NetworkMode = "host" },
		"root":               func(e *actionInspect) { e.Config.User = "0" },
		"writable source":    func(e *actionInspect) { e.Mounts[0].RW = true },
		"wrong output mount": func(e *actionInspect) { e.Mounts[2].Source = "/tmp/unowned" },
		"privileged":         func(e *actionInspect) { e.HostConfig.Privileged = true },
		"caps":               func(e *actionInspect) { e.HostConfig.CapDrop = nil },
		"security":           func(e *actionInspect) { e.HostConfig.SecurityOpt = nil },
		"pids":               func(e *actionInspect) { e.HostConfig.PidsLimit = nil },
		"memory":             func(e *actionInspect) { e.HostConfig.Memory = 0 },
		"cpu":                func(e *actionInspect) { e.HostConfig.NanoCpus = 0 },
		"file limit":         func(e *actionInspect) { e.HostConfig.Ulimits = nil },
		"wrong image":        func(e *actionInspect) { e.Image = "sha256:" + strings.Repeat("f", 64) },
		"wrong argv":         func(e *actionInspect) { e.Config.Cmd = []string{"other"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var clone actionInspect
			if err := json.Unmarshal(dagEncode(inspection), &clone); err != nil {
				t.Fatal(err)
			}
			mutate(&clone)
			if err := verifyActionPolicy(p, task, a, clone, task.Action.Argv, false); err == nil {
				t.Fatal("unsafe inspection accepted")
			}
		})
	}
}

func TestDAGActionNoProviderPreparation(t *testing.T) {
	e, path, p, _ := localDAGWorkerFixture(t)
	p.Tasks[0] = supervisor.Task{Task: "task", Kind: "action", ContractHash: e.hash, Base: p.Tasks[0].Base, MaxAttempts: 1, Verify: [][]string{{"/bin/true"}}, Action: &supervisor.ActionSpec{Argv: []string{"/bin/true"}, TimeoutMS: 1000, MaxOutputBytes: 64, MaxOutputFiles: 10, MaxOutputTotalBytes: 4096, Sandbox: supervisor.ActionSandbox{Backend: "docker", Image: "sha256:" + strings.Repeat("a", 64)}}}
	hash := bindDAGPlan(t, e, path, p)
	loaded, _, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Removing the provider config makes any accidental routing path fail.
	if err := os.Remove(e.cfgPath); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(os.Getenv("PIPELINE_LEDGER"))
	if err != nil {
		t.Fatal(err)
	}
	backend := actionBackend
	actionBackend = func() (actionDocker, error) { return actionDocker{}, errors.New("expected unavailable local backend") }
	t.Cleanup(func() { actionBackend = backend })
	c, err := run.AcquireController(e.wt, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = dagOperations(loaded, hash).Prepare(context.Background(), c, p.Tasks[0], []supervisor.ResolvedInput{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "expected unavailable local backend") {
		t.Fatalf("action did not use only action backend: %v", err)
	}
	holds, err := budget.Outstanding()
	if err != nil || len(holds) != 0 {
		t.Fatalf("action reserved quota %+v %v", holds, err)
	}
	ledgerAfter, err := os.ReadFile(os.Getenv("PIPELINE_LEDGER"))
	if err != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatal("action wrote model ledger rows")
	}
	if _, err := os.Stat(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json")); !os.IsNotExist(err) {
		t.Fatal("action produced model receipt")
	}
}
func TestActionOutputLimitsAndLinks(t *testing.T) {
	root := t.TempDir()
	a := &supervisor.ActionSpec{MaxOutputFiles: 2, MaxOutputTotalBytes: 3}
	if err := os.WriteFile(filepath.Join(root, "result"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateActionOutputs(root, a); err == nil {
		t.Fatal("oversized output accepted")
	}
	if err := os.Remove(filepath.Join(root, "result")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := validateActionOutputs(root, a); err == nil {
		t.Fatal("output symlink accepted")
	}
}

// This fixture exercises production orchestration with a fake local Docker
// control plane and a real bounded child process. It is not containment proof.
func TestDAGActionExecutionNoModelRouting(t *testing.T) {
	e, path, p, _ := localDAGWorkerFixture(t)
	p.Tasks[0] = supervisor.Task{Task: "task", Kind: "action", ContractHash: e.hash, Base: p.Tasks[0].Base, MaxAttempts: 1, Verify: [][]string{{"/bin/true"}}, Action: &supervisor.ActionSpec{Argv: []string{"/bin/true"}, TimeoutMS: 1000, MaxOutputBytes: 64, MaxOutputFiles: 10, MaxOutputTotalBytes: 4096, Sandbox: supervisor.ActionSandbox{Backend: "docker", Image: "sha256:" + strings.Repeat("a", 64)}}}
	bindDAGPlan(t, e, path, p)
	if err := os.Remove(e.cfgPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(os.Getenv("PIPELINE_LEDGER"))
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(e.root, "docker-fixture")
	// This fake CLI validates the actual production start argv. The old fake
	// ignored all arguments and could not detect Docker's unsupported flag.
	script := `#!/bin/sh
set -eu
test "$1" = --host
test "$2" = unix:///fixture/socket
test "$3" = --config
test "$4" = /fixture/config
shift 4
test "$1" = start
shift
test "$1" = --attach
shift
case "$1" in
  -*) printf 'unknown flag: %s\n' "$1" >&2; exit 125 ;;
esac
test "$#" -eq 1
test "${#1}" -eq 64
printf fixture > "$0.started"
printf 'bounded fixture output\n'
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// Positive execution below uses runActionContainer, not a duplicated start
	// command. First prove this fake rejects the independently reproduced bug.
	fakeCLI := actionDocker{binary: fake, socket: "/fixture/socket", config: "/fixture/config"}
	rejected, rejectErr := fakeCLI.command(context.Background(), "start", "--attach", "--sig-proxy=false", strings.Repeat("a", 64)).CombinedOutput()
	var rejectExit *exec.ExitError
	if !errors.As(rejectErr, &rejectExit) || rejectExit.ExitCode() != 125 || !strings.Contains(string(rejected), "unknown flag: --sig-proxy=false") {
		t.Fatalf("fake did not reject unsupported start flag: %q %v", rejected, rejectErr)
	}
	if _, err := os.Stat(fake + ".started"); !os.IsNotExist(err) {
		t.Fatalf("rejected fake start executed: %v", err)
	}
	marker := fake + ".started"
	var current actionInspect
	backend := actionBackend
	actionBackend = func() (actionDocker, error) {
		return actionDocker{binary: fake, socket: "/fixture/socket", config: "/fixture/config", callFn: func(ctx context.Context, args ...string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch args[0] {
			case "info":
				if args[len(args)-1] == "{{.ID}}" {
					return []byte("fixture-daemon\n"), nil
				}
				return []byte("linux\n"), nil
			case "image":
				return []byte(p.Tasks[0].Action.Sandbox.Image + "\n"), nil
			case "create":
				current = actionInspect{}
				get := func(key string) string {
					for i, arg := range args {
						if arg == key && i+1 < len(args) {
							return args[i+1]
						}
					}
					return ""
				}
				current.Name = "/" + get("--name")
				current.ID = graphHash([]byte(current.Name))
				current.Image = p.Tasks[0].Action.Sandbox.Image
				current.Config.Image = current.Image
				current.Config.User = get("--user")
				current.Config.WorkingDir = get("--workdir")
				current.Config.Entrypoint = []string{get("--entrypoint")}
				current.Config.Labels = map[string]string{}
				for i, arg := range args {
					if arg == "--label" {
						key, value, _ := strings.Cut(args[i+1], "=")
						current.Config.Labels[key] = value
					}
					if arg == "--mount" {
						fields := strings.Split(args[i+1], ",")
						var source, target string
						ro := false
						for _, field := range fields {
							key, value, _ := strings.Cut(field, "=")
							switch key {
							case "src":
								source = value
							case "dst":
								target = value
							case "readonly":
								ro = true
							}
						}
						current.Mounts = append(current.Mounts, struct {
							Type, Source, Destination string
							RW                        bool
						}{"bind", source, target, !ro})
					}
				}
				h := &current.HostConfig
				h.NetworkMode = get("--network")
				h.IpcMode = get("--ipc")
				h.ReadonlyRootfs = true
				h.Memory = 268435456
				h.MemorySwap = 268435456
				h.NanoCpus = 1000000000
				pids := int64(64)
				h.PidsLimit = &pids
				h.CapDrop = []string{get("--cap-drop")}
				h.SecurityOpt = []string{get("--security-opt")}
				h.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16777216,mode=1777"}
				h.Ulimits = append(h.Ulimits, struct {
					Name       string
					Soft, Hard int64
				}{"fsize", 4096, 4096}, struct {
					Name       string
					Soft, Hard int64
				}{"nofile", 128, 128})
				current.State.StartedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
				current.State.FinishedAt = time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
				return []byte(current.ID + "\n"), nil
			case "inspect":
				return dagEncode([]actionInspect{current}), nil
			case "rm", "ps":
				return nil, nil
			default:
				return nil, errors.New("unexpected Docker fixture operation")
			}
		}}, nil
	}
	t.Cleanup(func() { actionBackend = backend })
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	var result supervisor.GraphResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if code != 0 || result.Status != "verified" || len(result.Completed) != 1 {
		t.Fatalf("action %d %+v %s", code, result, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("external action fixture did not run")
	}
	after, err := os.ReadFile(os.Getenv("PIPELINE_LEDGER"))
	if err != nil || string(before) != string(after) {
		t.Fatal("action called model routing/accounting")
	}
	holds, err := budget.Outstanding()
	if err != nil || len(holds) != 0 {
		t.Fatalf("action paid holds %+v %v", holds, err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "contracts", "routes", "decisions", "task.json")); !os.IsNotExist(err) {
		t.Fatal("action created model receipt")
	}
}
