package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
)

func fixture(t *testing.T) (Plan, string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, b, err)
		}
	}
	t.Setenv("REIN_RUN_DIR", filepath.Join(dir, "run"))
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(dir, "contracts"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if err := os.MkdirAll(contract.IndexDir(), 0700); err != nil {
		t.Fatal(err)
	}
	c := contract.Contract{Name: "task", Worktree: repo, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(dir, "report")}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(contract.PathOf("task"), b, 0600); err != nil {
		t.Fatal(err)
	}
	// Fixture evidence represents a completed, exact dispatch handoff. Production
	// rejects legacy leases, which do not persist Orca task/dispatch identities.
	os.MkdirAll(filepath.Join(contract.IndexDir(), "routes", "decisions"), 0700)
	os.MkdirAll(filepath.Join(contract.IndexDir(), "routes", "leases"), 0700)
	os.MkdirAll(filepath.Join(contract.IndexDir(), "routes", "used"), 0700)
	os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "used", "a"), nil, 0600)
	d := routing.Decision{Run: "test", Task: "task", Attempt: "a", DecisionID: "decision", Worktree: repo, ContractHash: routing.ContractHash(&c)}
	b, _ = json.Marshal(d)
	os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "decisions", "task.json"), b, 0600)
	os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "leases", "task.json"), []byte(`{"task":"task","attempt":"a","decision":"decision","dispatched":true,"orca_task":"ot","dispatch":"d"}`), 0600)
	p := Plan{Schema: 1, Run: "test", Repo: repo, Terminal: "coord", Source: "orca-fixture", Version: "test", MaxChecks: 2, TimeoutMS: 1000, PollIntervalMS: 1, Tasks: []Task{{Task: "task", Attempt: "a", CurrentAttempt: "a", OrcaTask: "ot", Dispatch: "d"}}}
	b, _ = json.Marshal(p)
	p, hash, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartSupervised(repo, p.Run, nil, run.Owner{SessionID: "owner", PID: os.Getpid()}, hash); err != nil {
		t.Fatal(err)
	}
	return p, hash
}

func batch(id, dispatch string) []byte {
	b, _ := json.Marshal(map[string]any{"result": map[string]any{"deliveryId": "delivery", "messages": []any{map[string]any{"id": id, "type": "worker_done", "payload": map[string]string{"taskId": "ot", "dispatchId": dispatch, "outcome": "worker_done"}}}}})
	return b
}

func isAck(args []string) bool {
	for _, arg := range args {
		if arg == "--ack" {
			return true
		}
	}
	return false
}

func TestMonitorDurableBeforeAckAndReplay(t *testing.T) {
	p, hash := fixture(t)
	checks, acks := 0, 0
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "orca" {
			t.Fatal(name)
		}
		if !isAck(args) {
			checks++
			return batch("event", "d"), nil
		}
		acks++
		events, err := routing.ReadEvents()
		if err != nil || len(events) != 1 {
			t.Fatalf("ACK before durable event: %v %v", events, err)
		}
		if acks == 1 {
			return nil, errors.New("connection lost")
		}
		return []byte(`{"result":{}}`), nil
	}
	if out, err := Monitor(context.Background(), p, hash, runner); err == nil || out.State != routing.WaitBlockedReconciliation {
		t.Fatalf("first: %+v %v", out, err)
	}
	out, err := Monitor(context.Background(), p, hash, runner)
	if err != nil || out.State != routing.WaitHuman || checks != 1 || acks != 2 {
		t.Fatalf("replay: %+v %v checks=%d acks=%d", out, err, checks, acks)
	}
	events, _ := routing.ReadEvents()
	if len(events) != 1 {
		t.Fatalf("duplicate events: %+v", events)
	}
}

func TestMonitorAuthorityAndBindingFailures(t *testing.T) {
	for _, test := range []string{"competing", "changed-marker", "unbound", "corrupt-events", "receipt", "mutated-plan", "legacy", "missing-receipt", "missing-lease", "missing-used", "legacy-lease", "heartbeat"} {
		t.Run(test, func(t *testing.T) {
			p, hash := fixture(t)
			calls := 0
			loc, _ := run.Locate(p.Repo)
			switch test {
			case "competing":
				c, err := run.AcquireController(p.Repo, hash)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
			case "corrupt-events":
				os.MkdirAll(filepath.Join(contract.IndexDir(), "routes"), 0700)
				os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "events.jsonl"), []byte("broken\n"), 0600)
			case "receipt":
				os.MkdirAll(filepath.Join(contract.IndexDir(), "routes", "decisions"), 0700)
				os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "decisions", "task.json"), []byte(`{"attempt":"other"}`), 0600)
			case "missing-receipt":
				os.Remove(filepath.Join(contract.IndexDir(), "routes", "decisions", "task.json"))
			case "missing-lease":
				os.Remove(filepath.Join(contract.IndexDir(), "routes", "leases", "task.json"))
			case "missing-used":
				os.Remove(filepath.Join(contract.IndexDir(), "routes", "used", "a"))
			case "legacy-lease":
				os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "leases", "task.json"), []byte(`{"task":"task","attempt":"a","decision":"decision","dispatched":true}`), 0600)
			case "mutated-plan":
				p.Terminal = "foreign"
			case "legacy":
				b, _ := os.ReadFile(loc.Marker)
				var m run.Marker
				json.Unmarshal(b, &m)
				m.Schema = 1
				m.SupervisorPlanHash = ""
				b, _ = json.Marshal(m)
				os.WriteFile(loc.Marker, b, 0600)
			}
			runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if isAck(args) {
					t.Fatal("unsafe ACK")
				}
				if test == "changed-marker" {
					b, _ := os.ReadFile(loc.Marker)
					os.WriteFile(loc.Marker, append(b, ' '), 0600)
				}
				dispatch := "d"
				if test == "unbound" {
					dispatch = "foreign"
				}
				if test == "heartbeat" {
					return []byte(`{"result":{"deliveryId":"heartbeat","messages":[{"id":"hb","type":"heartbeat","payload":{}}]}}`), nil
				}
				return batch("event", dispatch), nil
			}
			out, err := Monitor(context.Background(), p, hash, runner)
			if err == nil || out.State != routing.WaitBlockedReconciliation {
				t.Fatalf("%+v %v", out, err)
			}
			if (test == "competing" || test == "receipt" || test == "mutated-plan" || test == "legacy") && calls != 0 {
				t.Fatalf("external call before validation: %d", calls)
			}
			if strings.HasPrefix(test, "missing-") || test == "legacy-lease" {
				if calls != 0 {
					t.Fatal("missing dispatch evidence permitted check")
				}
			}
		})
	}
}

func TestMonitorCheckCrashIsNeverBlindlyRetried(t *testing.T) {
	p, hash := fixture(t)
	calls := 0
	runner := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("unknown dispatch state")
	}
	if _, err := Monitor(context.Background(), p, hash, runner); err == nil {
		t.Fatal("missing error")
	}
	if _, err := Monitor(context.Background(), p, hash, runner); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("%v", err)
	}
	if calls != 1 {
		t.Fatal("repeated ambiguous check")
	}
}

func TestMonitorIdleBudgetCancelAndQuota(t *testing.T) {
	for _, test := range []string{"budget", "cancel", "quota", "corrupt-cooldown", "stale"} {
		t.Run(test, func(t *testing.T) {
			p, hash := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			if test == "quota" || test == "corrupt-cooldown" {
				// Change the pinned plan through a fresh, deliberately authorized run.
				p.Pools = []string{"pool"}
				b, _ := json.Marshal(p)
				p, hash, _ = Decode(b)
				loc, _ := run.Locate(p.Repo)
				m, _ := run.Load(loc.Marker)
				m.SupervisorPlanHash = hash
				b, _ = json.Marshal(m)
				os.WriteFile(loc.Marker, b, 0600)
				os.MkdirAll(filepath.Join(contract.IndexDir(), "routes"), 0700)
				b = []byte(`{"pool":{"reason":"quota"}}`)
				if test == "corrupt-cooldown" {
					b = []byte("bad")
				}
				os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "cooldowns.json"), b, 0600)
			}
			if test == "stale" {
				p.Tasks[0].CurrentAttempt = "new"
				b, _ := json.Marshal(p)
				p, hash, _ = Decode(b)
				loc, _ := run.Locate(p.Repo)
				m, _ := run.Load(loc.Marker)
				m.SupervisorPlanHash = hash
				b, _ = json.Marshal(m)
				os.WriteFile(loc.Marker, b, 0600)
			}
			runner := func(context.Context, string, ...string) ([]byte, error) {
				calls++
				if test == "cancel" {
					cancel()
				}
				if test == "stale" {
					return batch("old", "d"), nil
				}
				return []byte(`{"result":{"messages":[]}}`), nil
			}
			out, err := Monitor(ctx, p, hash, runner)
			switch test {
			case "budget":
				if err != nil || out.Checks != 2 || out.State != routing.WaitHuman {
					t.Fatalf("%+v %v", out, err)
				}
			case "quota":
				if err != nil || calls != 0 || out.State != routing.WaitQuota {
					t.Fatalf("%+v %v calls=%d", out, err, calls)
				}
			case "corrupt-cooldown":
				if err == nil || calls != 0 {
					t.Fatalf("%+v %v", out, err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%v", err)
				}
			case "stale":
				if err == nil || calls != 0 {
					t.Fatalf("stale attempt consumed: %+v %v calls=%d", out, err, calls)
				}
			}
		})
	}
}

func TestMonitorAfterCoordinatorDeathDoesNotRebindMarker(t *testing.T) {
	p, hash := fixture(t)
	loc, _ := run.Locate(p.Repo)
	b, _ := os.ReadFile(loc.Marker)
	var m run.Marker
	json.Unmarshal(b, &m)
	m.PID = 99999999
	m.StartTime = 1
	b, _ = json.Marshal(m)
	os.WriteFile(loc.Marker, b, 0600)
	if m.OwnerAlive() {
		t.Fatal("fixture owner alive")
	}
	out, err := Monitor(context.Background(), p, hash, func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"result":{"messages":[]}}`), nil
	})
	if err != nil || out.State != routing.WaitHuman {
		t.Fatalf("%+v %v", out, err)
	}
	after, _ := os.ReadFile(loc.Marker)
	if string(after) != string(b) {
		t.Fatal("marker rebound")
	}
}

func TestPlanRejectsUnknownDuplicateAndUnbounded(t *testing.T) {
	p, _ := fixture(t)
	b, _ := json.Marshal(p)
	for _, bad := range [][]byte{[]byte(`{"schema":1,"schema":1}`), append(b, []byte(` {}`)...), []byte(strings.Replace(string(b), `"max_checks":2`, `"max_checks":0`, 1)), []byte(strings.Replace(string(b), `"schema":1`, `"schema":1,"unknown":true`, 1))} {
		if _, _, err := Decode(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestMixedBatchDoesNotPersistOrAck(t *testing.T) {
	p, hash := fixture(t)
	var e map[string]any
	json.Unmarshal(batch("event", "d"), &e)
	r := e["result"].(map[string]any)
	r["messages"] = append(r["messages"].([]any), map[string]any{"id": "other", "type": "worker_done", "payload": map[string]string{"taskId": "foreign", "dispatchId": "foreign"}})
	b, _ := json.Marshal(e)
	_, err := Monitor(context.Background(), p, hash, func(context.Context, string, ...string) ([]byte, error) { return b, nil })
	if err == nil {
		t.Fatal("accepted mixed delivery")
	}
	events, _ := routing.ReadEvents()
	if len(events) != 0 {
		t.Fatal("partial write")
	}
}

func TestJournalRejectsCorruption(t *testing.T) {
	for _, s := range []string{"broken\n", `{"phase":"check"}`, "{\"phase\":\"consumed\"}\n"} {
		j := journal{path: filepath.Join(t.TempDir(), "journal")}
		os.WriteFile(j.path, []byte(s), 0600)
		if _, err := j.load(); err == nil {
			t.Fatal("accepted corrupt journal")
		}
	}
}

func TestExplicitOrcaRefusalNeverClaimsConsumption(t *testing.T) {
	for _, phase := range []string{"check", "ack"} {
		t.Run(phase, func(t *testing.T) {
			p, hash := fixture(t)
			checks, acks := 0, 0
			runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if isAck(args) {
					acks++
					if acks == 1 {
						return []byte(`{"ok":false,"result":{}}`), nil
					}
					return []byte(`{"ok":true,"result":{}}`), nil
				}
				checks++
				raw := batch("event", "d")
				if phase == "check" {
					raw = []byte(strings.Replace(string(raw), `{"result":`, `{"ok":false,"result":`, 1))
				}
				return raw, nil
			}
			if _, err := Monitor(context.Background(), p, hash, runner); err == nil {
				t.Fatal("false success accepted")
			}
			events, _ := routing.ReadEvents()
			if phase == "check" {
				if len(events) != 0 || acks != 0 {
					t.Fatal("refused check wrote event or acknowledged")
				}
				return
			}
			out, err := Monitor(context.Background(), p, hash, runner)
			if err != nil || out.State != routing.WaitHuman || checks != 1 || acks != 2 {
				t.Fatalf("refused ACK lost replay: %+v %v checks=%d acks=%d", out, err, checks, acks)
			}
		})
	}
}

func TestIdleTimerCancellation(t *testing.T) {
	p, hash := fixture(t)
	p.PollIntervalMS = 500
	b, _ := json.Marshal(p)
	p, hash, _ = Decode(b)
	loc, _ := run.Locate(p.Repo)
	m, _ := run.Load(loc.Marker)
	m.SupervisorPlanHash = hash
	b, _ = json.Marshal(m)
	os.WriteFile(loc.Marker, b, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := Monitor(ctx, p, hash, func(context.Context, string, ...string) ([]byte, error) {
		calls++
		time.AfterFunc(20*time.Millisecond, cancel)
		return []byte(`{"result":{"messages":[]}}`), nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("idle timer not canceled: calls=%d err=%v", calls, err)
	}
}

func TestExecuteBoundsOutputAndContext(t *testing.T) {
	var output limitedOutput
	if _, err := output.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte{'x'}); err == nil {
		t.Fatal("unbounded output")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Execute(ctx, "sh", "-c", "sleep 2 & wait")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("unbounded external wait: %s %v", time.Since(started), err)
	}
}
