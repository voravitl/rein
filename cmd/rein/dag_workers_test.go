package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/guard"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

type dagWorkerArrival struct {
	Task, Worktree string
	PID            int
}

// This is a simulated local provider, not a model call. The agy wrapper execs
// this child test process, which must remain alive at the parent-owned barrier.
func TestDAGSimulatedWorkerProcess(t *testing.T) {
	if os.Getenv("REIN_DAG_SIMULATED_WORKER") != "1" {
		return
	}
	wt, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	task := os.Getenv("ADVISE_TASK")
	conn, err := net.DialTimeout("tcp", os.Getenv("REIN_DAG_WORKER_BARRIER"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(dagWorkerArrival{task, wt, os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "release\n" {
		t.Fatalf("barrier did not release both workers: %q %v", line, err)
	}
	// Exercise the real bound worker judge before the simulated edit. Installed
	// hook configuration is created by hooks.Install in this disposable fixture.
	event := dagEncode(map[string]any{"workspacePaths": []string{wt}, "toolCall": map[string]any{
		"name": "write_to_file", "args": map[string]any{"TargetFile": filepath.Join(wt, "src", "a.go"), "CodeContent": "fixed\n", "Overwrite": true},
	}})
	var output, stderr bytes.Buffer
	if code := guard.RunTask("agy", task, bytes.NewReader(event), &output, &stderr); code != 0 || output.Len() != 0 {
		t.Fatalf("real worker guard refused edit: exit=%d %s %s", code, output.String(), stderr.String())
	}
	if err := os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("fixed\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDAGConcurrentGovernedWorkers(t *testing.T) {
	e, path, p, _ := localDAGWorkerFixture(t)
	second := filepath.Join(e.root, "C")
	gitIn(t, e.wt, "worktree", "add", "--detach", second, "HEAD")
	// No environment/global mutation or capture() occurs after RunDAG starts.
	t.Setenv("REIN_SUPERVISOR_INVOCATION", "")
	t.Setenv("PIPELINE_LOGDIR", filepath.Join(e.root, "guard-logs"))
	t.Setenv("REIN_DAG_SIMULATED_WORKER", "1")
	t.Setenv("REIN_DAG_TEST_BINARY", os.Args[0])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("REIN_DAG_WORKER_BARRIER", listener.Addr().String())
	provider := filepath.Join(e.root, "agy")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\nexec \"$REIN_DAG_TEST_BINARY\" -test.run '^TestDAGSimulatedWorkerProcess$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	template := p.Tasks[0]
	p.Execution.MaxParallel = 2
	p.MaxChecks = 2
	p.TimeoutMS = 30000
	p.Tasks = nil
	for i, name := range []string{"B", "C"} {
		c := *e.c
		c.Name, c.RunCopy = name, ""
		c.ReportPath = filepath.Join(e.root, "reports", name+".md")
		if i == 1 {
			c.Worktree = second
		}
		if _, err := c.Save(); err != nil {
			t.Fatal(err)
		}
		results, _, err := hooks.Install(&c, os.Args[0], []string{"agy"})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatalf("install fixture hooks: %+v %v", results, err)
		}
		var profile routing.TaskProfile
		if err := json.Unmarshal(e.pj, &profile); err != nil {
			t.Fatal(err)
		}
		profile.ContractHash = routing.ContractHash(&c)
		task := template
		task.Task, task.ContractHash = name, profile.ContractHash
		task.Profile = dagEncode(profile)
		p.Tasks = append(p.Tasks, task)
	}
	// The second worktree also contains controller-created read-only snapshots.
	t.Cleanup(func() {
		_ = filepath.WalkDir(e.root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	hash := bindDAGPlan(t, e, path, p)
	for _, name := range []string{"B", "C"} {
		if _, err := run.Allow(e.wt, name, "approved simulated parallel worker fixture"); err != nil {
			t.Fatal(err)
		}
	}
	loaded, gotHash, err := supervisor.Load(path)
	if err != nil || gotHash != hash {
		t.Fatalf("exact bound plan: %s %v", gotHash, err)
	}
	if err := validateLocalDAGPlan(loaded); err != nil {
		t.Fatal(err)
	}
	type accepted struct {
		conn    net.Conn
		arrival dagWorkerArrival
		err     error
	}
	arrivals := make(chan accepted, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				arrivals <- accepted{err: err}
				return
			}
			var a dagWorkerArrival
			err = conn.SetDeadline(time.Now().Add(20 * time.Second))
			if err == nil {
				err = json.NewDecoder(conn).Decode(&a)
			}
			arrivals <- accepted{conn, a, err}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	type completed struct {
		result supervisor.GraphResult
		err    error
	}
	done := make(chan completed, 1)
	go func() {
		r, err := supervisor.RunDAG(ctx, loaded, hash, dagOperations(loaded, hash))
		done <- completed{r, err}
	}()
	joined := false
	var connections []net.Conn
	var observed []dagWorkerArrival
	defer func() {
		cancel()
		_ = listener.Close()
		for _, conn := range connections {
			_ = conn.Close()
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Error("production worker cancellation/drain did not return")
			}
		}
		for _, a := range observed {
			if _, err := run.ProcStart(a.PID); !errors.Is(err, run.ErrNoProc) {
				t.Errorf("simulated worker pid %d not proven gone after bounded drain: %v", a.PID, err)
			}
		}
	}()
	decisions := map[string]routing.Decision{}
	for len(observed) < 2 {
		select {
		case item := <-arrivals:
			if item.conn != nil {
				connections = append(connections, item.conn)
			}
			if item.err != nil {
				t.Fatal(item.err)
			}
			observed = append(observed, item.arrival)
		case ended := <-done:
			joined = true
			t.Fatalf("DAG returned before two live workers rendezvoused: %+v %v", ended.result, ended.err)
		case <-ctx.Done():
			t.Fatal("two independent workers never reached the barrier")
		}
	}
	if observed[0].PID == observed[1].PID || observed[0].Task == observed[1].Task || observed[0].Worktree == observed[1].Worktree {
		t.Fatalf("workers do not have independent process/ownership identities: %+v", observed)
	}
	for _, a := range observed {
		if a.Task != "B" && a.Task != "C" {
			t.Fatalf("unexpected barrier task: %+v", a)
		}
		c, err := contract.Load(a.Task)
		if err != nil || contract.Real(c.Worktree) != contract.Real(a.Worktree) {
			t.Fatalf("worker contract/worktree: %+v %v", a, err)
		}
		raw, err := os.ReadFile(filepath.Join(contract.IndexDir(), "routes", "decisions", a.Task+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var d routing.Decision
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if d.Task != a.Task || d.Run != p.Run || d.Hold == "" || !routing.ReceiptUsed(d.Attempt) {
			t.Fatalf("missing governed receipt/reservation: %+v", d)
		}
		decisions[a.Task] = d
		// Child registration follows Start; poll only for the durable lease
		// identity, never as elapsed-time evidence of parallelism.
		ticker := time.NewTicker(5 * time.Millisecond)
		for {
			raw, err := os.ReadFile(filepath.Join(contract.IndexDir(), "routes", "leases", a.Task+".json"))
			var lease routing.Lease
			if err == nil && json.Unmarshal(raw, &lease) == nil && lease.Child == a.PID && lease.ChildGroup == a.PID && lease.Attempt == d.Attempt && lease.Decision == d.DecisionID {
				birth, err := run.ProcStart(a.PID)
				if err != nil || birth != lease.ChildStart {
					t.Fatalf("live child birth identity mismatch: %+v %v", lease, err)
				}
				break
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				ticker.Stop()
				t.Fatal("worker never registered exact lease identity")
			}
		}
		ticker.Stop()
		holds, err := budget.HoldsFor(d.Attempt)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hold := range holds {
			if hold.ID == d.Hold && hold.State == "reserved" && hold.Task == a.Task && hold.Run == p.Run && hold.Decision == d.DecisionID {
				found = true
			}
		}
		if !found {
			t.Fatalf("no exact reserve-before-launch hold: %+v", holds)
		}
	}
	if decisions["B"].Attempt == decisions["C"].Attempt || decisions["B"].Hold == decisions["C"].Hold {
		t.Fatal("parallel workers shared attempt/hold identity")
	}
	for _, conn := range connections {
		if _, err := fmt.Fprintln(conn, "release"); err != nil {
			t.Fatal(err)
		}
	}
	var ended completed
	select {
	case ended = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("workers did not finalize within the controller bound")
	}
	if ended.err != nil || ended.result.Status != "verified" || len(ended.result.Completed) != 2 {
		t.Fatalf("production parallel workers: %+v %v", ended.result, ended.err)
	}
	for _, a := range observed {
		d := decisions[a.Task]
		if err := routing.VerifyCheckpoint(a.Task, d.Attempt); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(graphEvidencePath(d.Attempt, "execution"))
		var execution graphExecution
		if err != nil || json.Unmarshal(raw, &execution) != nil || execution.PID != a.PID || execution.ExitCode != 0 || !execution.Gone {
			t.Fatalf("exact execution evidence: %s %v", raw, err)
		}
		if _, err := os.Stat(graphEvidencePath(d.Attempt, "verification")); err != nil {
			t.Fatal(err)
		}
		seen, err := os.ReadFile(contract.SeenPath(a.Task))
		if err != nil || !strings.Contains(string(seen), "agy pretool write_to_file") {
			t.Fatalf("worker guard did not observe simulated edit: %q %v", seen, err)
		}
		if why := routing.TaskBusy(a.Task); why != "" {
			t.Fatal(why)
		}
		holds, err := budget.HoldsFor(d.Attempt)
		if err != nil {
			t.Fatal(err)
		}
		settled := false
		for _, hold := range holds {
			if hold.ID == d.Hold && hold.State == "settled" && hold.Task == a.Task && hold.Run == p.Run && hold.Decision == d.DecisionID {
				settled = true
			}
		}
		if !settled {
			t.Fatalf("exact worker hold did not settle: %+v", holds)
		}
	}
	if holds, err := budget.Outstanding(); err != nil || len(holds) != 0 {
		t.Fatalf("unsettled worker accounting: %+v %v", holds, err)
	}
	ledgerBefore, err := os.ReadFile(os.Getenv("PIPELINE_LEDGER"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := supervisor.RunDAG(context.Background(), loaded, hash, dagOperations(loaded, hash))
	if err != nil || again.Status != "verified" || len(again.Completed) != 2 {
		t.Fatalf("completed replay: %+v %v", again, err)
	}
	ledgerAfter, err := os.ReadFile(os.Getenv("PIPELINE_LEDGER"))
	if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("replay launched/probed/accounted another worker: %v", err)
	}
	for task, d := range decisions {
		raw, err := os.ReadFile(filepath.Join(contract.IndexDir(), "routes", "decisions", task+".json"))
		var after routing.Decision
		if err != nil || json.Unmarshal(raw, &after) != nil || after.Attempt != d.Attempt || after.Hold != d.Hold {
			t.Fatalf("replay replaced %s receipt: %s %v", task, raw, err)
		}
	}
}
