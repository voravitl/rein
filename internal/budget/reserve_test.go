package budget

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/run"
)

const testRun = "r1"

// reserveEnv points every rein state location at temp dirs (nothing here may reach the real home), writes a run marker that
// started long enough ago for any time zone, and returns the marker path.
func reserveEnv(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(d, "ledger.jsonl"))
	t.Setenv("REIN_RUN_DIR", filepath.Join(d, "rundir"))
	if err := os.MkdirAll(filepath.Join(d, "rundir"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(d, "rundir", run.MarkerFile)
	m := &run.Marker{Schema: run.Schema, Run: testRun, StartedAt: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
		Root: d, SessionID: "session", PID: os.Getpid(), StartTime: time.Now().Unix()}
	if err := writeTestMarker(marker, m); err != nil {
		t.Fatal(err)
	}
	return marker
}

func capProfile(pool string, taskCap, runCap float64) *contract.Profile {
	return &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{pool: {TaskCap: taskCap, RunCap: runCap}}}}
}

func fund(pool string, amount float64) Item {
	return Item{Pool: pool, Unit: "tokens", Amount: amount, Kind: "funding"}
}

func calib(pool, unit string, amount float64, calls int) Item {
	return Item{Pool: pool, Unit: unit, Amount: amount, Calls: calls, Kind: "calibration"}
}

func request(task string, items ...Item) ReserveRequest {
	return ReserveRequest{Run: testRun, Task: task, Attempt: "a-" + task, Decision: "d-" + task, Items: items}
}

func mustReserve(t *testing.T, marker string, p *contract.Profile, r ReserveRequest) *Hold {
	t.Helper()
	h, err := Reserve(marker, p, r)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return h
}

func wantInsufficient(t *testing.T, err error, pool, scope string) *ErrInsufficient {
	t.Helper()
	var ei *ErrInsufficient
	if !errors.As(err, &ei) {
		t.Fatalf("error = %v, want *ErrInsufficient for %s/%s", err, scope, pool)
	}
	if ei.Pool != pool || ei.Scope != scope {
		t.Fatalf("insufficient %s/%s, want %s/%s (%v)", ei.Scope, ei.Pool, scope, pool, ei)
	}
	return ei
}

func remaining(t *testing.T, marker string, p *contract.Profile, task string) map[string]float64 {
	t.Helper()
	m, err := Remaining(marker, p, task)
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	return m
}

// allHolds reads the store the way the code under test does (under the lock).
func allHolds(t *testing.T) map[string]*Hold {
	t.Helper()
	var out map[string]*Hold
	if err := withStore(func(h map[string]*Hold) (bool, error) { out = h; return false, nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// inject writes a hold straight into the store, for owners the code under test would never produce.
func inject(t *testing.T, h Hold) {
	t.Helper()
	if err := withStore(func(holds map[string]*Hold) (bool, error) { holds[h.ID] = &h; return true, nil }); err != nil {
		t.Fatal(err)
	}
}

func ledgerRows(t *testing.T) []ledger.Row {
	t.Helper()
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// addCost records a cost row the way "someone else" (a launcher) would.
func addCost(t *testing.T, r ledger.Row, amount float64) {
	t.Helper()
	r.Kind, r.Amount = "cost", &amount
	if r.Run == "" {
		r.Run = testRun
	}
	if err := ledger.Append(r); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("timed out")
		}
	}
}

// helperCmd re-executes this test binary as TestReserveHelperProcess in the given mode (the pattern of routing's TestProbeProcess).
func helperCmd(mode string, env ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestReserveHelperProcess$", "-test.count=1")
	cmd.Env = append(append(os.Environ(), "REIN_RESERVE_HELPER="+mode), env...)
	out := new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd, out
}

// TestReserveHelperProcess is the body of the child processes of the multi-process tests; an ordinary run skips it.
func TestReserveHelperProcess(t *testing.T) {
	switch os.Getenv("REIN_RESERVE_HELPER") {
	case "reserve": // wait at the barrier, then try to reserve 4 of 10 and exit WITHOUT settling (a crash)
		dir := os.Getenv("REIN_RESERVE_SYNC")
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("ready-%d", os.Getpid())), nil, 0o600)
		for end := time.Now().Add(time.Minute); ; time.Sleep(time.Millisecond) {
			if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
				break
			}
			if time.Now().After(end) {
				fmt.Println("ERROR barrier timeout")
				os.Exit(3)
			}
		}
		_, err := Reserve(os.Getenv("REIN_RESERVE_MARKER"), capProfile("pool-a", 0, 10), request("t1", fund("pool-a", 4)))
		var ei *ErrInsufficient
		switch {
		case err == nil:
			fmt.Println("RESERVED")
		case errors.As(err, &ei):
			fmt.Println("INSUFFICIENT")
		default:
			fmt.Println("ERROR", err)
			os.Exit(3)
		}
		os.Exit(0)
	case "wait": // live until the parent closes stdin
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

func TestReserveAtomicGoroutines(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	const n = 12
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = Reserve(marker, p, request("t1", fund("p", 4)))
		}()
	}
	close(start)
	wg.Wait()
	ok, short := 0, 0
	for _, err := range errs {
		var ei *ErrInsufficient
		switch {
		case err == nil:
			ok++
		case errors.As(err, &ei):
			short++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 2 || short != n-2 {
		t.Fatalf("%d reserved, %d insufficient; want exactly 2 and %d", ok, short, n-2)
	}
	if got := allHolds(t); len(got) != 2 {
		t.Fatalf("store holds %d holds, want 2", len(got))
	}
	if r := remaining(t, marker, p, ""); r["p"] != 2 {
		t.Fatalf("remaining %v, want 2", r["p"])
	}
}

func TestReserveAtomicAcrossProcesses(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("pool-a", 0, 10)
	dir := t.TempDir()
	const n = 6
	cmds, outs := make([]*exec.Cmd, n), make([]*bytes.Buffer, n)
	defer func() {
		for _, c := range cmds {
			if c != nil && c.Process != nil {
				_ = c.Process.Kill()
			}
		}
	}()
	for i := range n {
		cmds[i], outs[i] = helperCmd("reserve", "REIN_RESERVE_MARKER="+marker, "REIN_RESERVE_SYNC="+dir)
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	// Release the children together, once every one of them is up and waiting.
	waitFor(t, time.Minute, func() bool { m, _ := filepath.Glob(filepath.Join(dir, "ready-*")); return len(m) == n })
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ok, short := 0, 0
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outs[i])
		}
		switch strings.TrimSpace(outs[i].String()) {
		case "RESERVED":
			ok++
		case "INSUFFICIENT":
			short++
		default:
			t.Fatalf("child %d said %q", i, outs[i])
		}
	}
	if ok != 2 || short != n-2 {
		t.Fatalf("%d reserved, %d insufficient; want exactly 2 and %d", ok, short, n-2)
	}
	// The reserving processes are gone and never settled: their holds are retained and still count.
	holds := allHolds(t)
	if len(holds) != 2 {
		t.Fatalf("store holds %d holds, want 2", len(holds))
	}
	pids := map[int]bool{}
	for _, c := range cmds {
		pids[c.Process.Pid] = true
	}
	for _, h := range holds {
		if !pids[h.OwnerPid] || h.State != "reserved" {
			t.Fatalf("hold %+v is not an unsettled hold of a child", h)
		}
	}
	if r := remaining(t, marker, p, ""); r["pool-a"] != 2 {
		t.Fatalf("remaining %v, want 2", r["pool-a"])
	}
}

func TestReserveAllOrNothing(t *testing.T) {
	marker := reserveEnv(t)
	p := &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"a": {RunCap: 10}, "b": {RunCap: 5}}}}
	for _, items := range [][]Item{{fund("a", 4), fund("b", 6)}, {fund("b", 6), fund("a", 4)}} {
		_, err := Reserve(marker, p, request("", items...)) // no task: only the run scope applies
		ei := wantInsufficient(t, err, "b", "run")
		if ei.Need != 6 || ei.Remaining != 5 {
			t.Fatalf("need/remaining = %v/%v, want 6/5", ei.Need, ei.Remaining)
		}
		for _, part := range []string{`"b"`, "run", "need 6", "remaining 5"} {
			if !strings.Contains(ei.Error(), part) {
				t.Fatalf("error %q lacks %q", ei, part)
			}
		}
		if len(allHolds(t)) != 0 {
			t.Fatal("a hold was recorded although the second item did not fit")
		}
		if r := remaining(t, marker, p, ""); r["a"] != 10 || r["b"] != 5 {
			t.Fatalf("a refused request changed the remaining budget: %v", r)
		}
	}
	mustReserve(t, marker, p, request("", fund("a", 4), fund("b", 5))) // filling a cap exactly fits
	if _, err := Reserve(marker, p, request("", fund("a", 4), fund("b", 5))); err == nil {
		t.Fatal("second request fit")
	}
	if r := remaining(t, marker, p, ""); r["a"] != 6 || r["b"] != 0 {
		t.Fatalf("partial hold left behind: %v", r)
	}
}

func TestReserveValidationBeforeStore(t *testing.T) {
	reserveEnv(t)
	nan, inf := math.NaN(), math.Inf(1)
	cal := &CalibrationCaps{MaxCalls: 5, PoolCaps: map[string]float64{"p": 5}}
	for name, r := range map[string]ReserveRequest{
		"no items":                 {Run: testRun},
		"empty pool":               request("t", Item{Unit: "tokens", Kind: "funding"}),
		"empty unit":               request("t", Item{Pool: "p", Kind: "funding"}),
		"empty kind":               request("t", Item{Pool: "p", Unit: "tokens"}),
		"unknown kind":             request("t", Item{Pool: "p", Unit: "tokens", Kind: "gift"}),
		"NaN amount":               request("t", fund("p", nan)),
		"Inf amount":               request("t", fund("p", inf)),
		"negative amount":          request("t", fund("p", -1)),
		"negative calls":           request("t", Item{Pool: "p", Unit: "tokens", Kind: "probe", Calls: -1}),
		"one pool in two units":    request("t", fund("p", 1), Item{Pool: "p", Unit: "usd", Amount: 1, Kind: "probe"}),
		"calibration without caps": request("t", calib("p", "tokens", 1, 1)),
		"NaN calibration cap": func() ReserveRequest {
			x := request("t", calib("p", "tokens", 1, 1))
			x.Calibration = &CalibrationCaps{MaxCalls: 5, PoolCaps: map[string]float64{"p": nan}}
			return x
		}(),
	} {
		if _, err := Reserve("", nil, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Reserve("", nil, ReserveRequest{Run: testRun, Items: []Item{calib("p", "tokens", 1, 1)}, Calibration: cal}); err != nil {
		t.Fatalf("a valid calibration request was refused: %v", err) // control: the table above fails for its own reason
	}
	// A budget with pools needs a marker, and that is also decided before the store is touched.
	if err := os.RemoveAll(holdsDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve("", capProfile("p", 0, 10), request("t", fund("p", 1))); err == nil {
		t.Error("capped budget accepted without a run marker")
	}
	if _, err := os.Stat(holdsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused request touched the store: %v", err)
	}
}

func TestReserveRunMustMatchMarker(t *testing.T) {
	marker := reserveEnv(t)
	r := request("t1", fund("p", 1))
	r.Run = "someone-else"
	if _, err := Reserve(marker, nil, r); err == nil {
		t.Fatal("request for another run accepted")
	}
	if _, err := Reserve(filepath.Join(filepath.Dir(marker), "missing.json"), nil, request("t1", fund("p", 1))); err == nil {
		t.Fatal("missing marker accepted")
	}
	if len(allHolds(t)) != 0 {
		t.Fatal("a refused request left a hold")
	}
}

func TestReserveHoldShape(t *testing.T) {
	marker := reserveEnv(t)
	items := []Item{fund("p", 3), {Pool: "q", Unit: "usd", Amount: 0.5, Calls: 2, Kind: "probe"}}
	h := mustReserve(t, marker, nil, request("t1", items...))
	if !regexp.MustCompile(`^h-[0-9a-f]{16}$`).MatchString(h.ID) {
		t.Fatalf("id %q", h.ID)
	}
	items[0].Amount = 99 // the caller's slice is not the hold's
	got := allHolds(t)[h.ID]
	if got == nil || got.Items[0].Amount != 3 || h.Items[0].Amount != 3 || len(got.Items) != 2 || got.Items[1].Calls != 2 {
		t.Fatalf("stored hold %+v", got)
	}
	if got.State != "reserved" || got.Run != testRun || got.Task != "t1" || got.Attempt != "a-t1" || got.Decision != "d-t1" || got.SettledAt != nil {
		t.Fatalf("stored hold %+v", got)
	}
	if got.OwnerPid != os.Getpid() || time.Since(got.CreatedAt) > time.Minute {
		t.Fatalf("owner/created: %+v", got)
	}
	if st, err := run.ProcStart(os.Getpid()); err == nil && got.OwnerStart != st {
		t.Fatalf("owner start %d, want %d", got.OwnerStart, st)
	}
	if h2 := mustReserve(t, marker, nil, request("t1", fund("p", 1))); h2.ID == h.ID {
		t.Fatal("hold ids collide")
	}
}

func TestOutstandingAndHoldsFor(t *testing.T) {
	marker := reserveEnv(t)
	a := mustReserve(t, marker, nil, request("one", fund("p", 1)))
	b := mustReserve(t, marker, nil, request("two", fund("p", 1)))
	c := mustReserve(t, marker, nil, request("one", fund("p", 1)))
	if _, err := Settle(b.ID, map[string]float64{"p": 1}); err != nil {
		t.Fatal(err)
	}
	out, err := Outstanding()
	if err != nil || len(out) != 2 || out[0].ID != a.ID || out[1].ID != c.ID {
		t.Fatalf("Outstanding = %+v, %v; want the two unsettled holds, oldest first", out, err)
	}
	for attempt, want := range map[string][]string{"a-one": {a.ID, c.ID}, "a-two": {b.ID}, "a-none": nil} {
		got, err := HoldsFor(attempt)
		if err != nil || len(got) != len(want) {
			t.Fatalf("HoldsFor(%s) = %+v, %v", attempt, got, err)
		}
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("HoldsFor(%s)[%d] = %s, want %s", attempt, i, got[i].ID, want[i])
			}
		}
	}
	if got, _ := HoldsFor("a-two"); got[0].State != "settled" {
		t.Fatalf("HoldsFor must include settled holds: %+v", got)
	}
}

func TestReserveCrashRetention(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	h := mustReserve(t, marker, p, request("t1", fund("p", 7))) // the work that held it vanishes without settling
	if r := remaining(t, marker, p, ""); r["p"] != 3 {
		t.Fatalf("remaining %v, want 3: an unsettled hold must keep counting", r["p"])
	}
	_, err := Reserve(marker, p, request("t2", fund("p", 4)))
	if ei := wantInsufficient(t, err, "p", "run"); ei.Remaining != 3 {
		t.Fatalf("remaining in error = %v, want 3", ei.Remaining)
	}
	mustReserve(t, marker, p, request("t2", fund("p", 3)))
	if out, _ := Outstanding(); len(out) != 2 || out[0].ID != h.ID {
		t.Fatalf("outstanding %+v", out)
	}
}

func TestReserveFailsClosedWhenLockBusy(t *testing.T) {
	marker := reserveEnv(t)
	old := lockWait
	lockWait = 60 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	unlock, err := run.LockFile(filepath.Join(holdsDir(), "holds.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(marker, nil, request("t1", fund("p", 1))); !errors.Is(err, run.ErrLockBusy) {
		t.Fatalf("Reserve while the store is locked: %v, want ErrLockBusy", err)
	}
	unlock()
	if len(allHolds(t)) != 0 {
		t.Fatal("a refused Reserve left a hold")
	}
	mustReserve(t, marker, nil, request("t1", fund("p", 1)))
}

func TestSettleUnknownUsageChargesBound(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	h := mustReserve(t, marker, p, request("t1", fund("p", 4)))
	if r := remaining(t, marker, p, ""); r["p"] != 6 {
		t.Fatalf("remaining with the hold %v, want 6", r["p"])
	}
	got, err := Settle(h.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "settled" || got.SettledAt == nil || got.Used["p"] != 4 {
		t.Fatalf("settled hold %+v", got)
	}
	rows := ledgerRows(t)
	if len(rows) != 1 {
		t.Fatalf("%d ledger rows, want exactly 1", len(rows))
	}
	r := rows[0]
	if r.Kind != "cost" || r.Run != testRun || r.Task != "t1" || r.Attempt != "a-t1" || r.Decision != "d-t1" || r.Pool != "p" || r.Unit != "tokens" ||
		r.Amount == nil || *r.Amount != 4 || r.Component != "worker" || r.ChargeID != h.ID+":p" || !r.Approx || r.Notes != "charged at reserved bound: usage unknown" {
		t.Fatalf("ledger row %+v", r)
	}
	if out, _ := Outstanding(); len(out) != 0 {
		t.Fatalf("outstanding after settle: %+v", out)
	}
	if rem := remaining(t, marker, p, ""); rem["p"] != 6 {
		t.Fatalf("remaining after settle %v, want 6: spend, not the hold, accounts for it and there is no double count", rem["p"])
	}
	// The same ledger charge is now spend: a task-scoped view sees it too.
	if rem := remaining(t, marker, capProfile("p", 5, 10), "t1"); rem["p"] != 1 {
		t.Fatalf("task remaining %v, want 1", rem["p"])
	}
}

func TestSettleChargesPerItemKind(t *testing.T) {
	reserveEnv(t)
	caps := &CalibrationCaps{MaxCalls: 9, PoolCaps: map[string]float64{"z": 50}}
	r := request("t1", Item{Pool: "x", Unit: "tokens", Amount: 1, Kind: "probe"}, fund("y", 2), calib("z", "tokens", 3, 1),
		fund("m", 1), calib("m", "tokens", 1, 0)) // a pool with mixed kinds charges as calibration
	r.Calibration = caps
	r.Calibration.PoolCaps["m"] = 5
	h := mustReserve(t, "", nil, r)
	if _, err := Settle(h.ID, map[string]float64{"y": 2}); err != nil { // y is measured: no row
		t.Fatal(err)
	}
	got := map[string]ledger.Row{}
	for _, row := range ledgerRows(t) {
		got[row.Pool] = row
	}
	for pool, want := range map[string]struct {
		component string
		amount    float64
	}{"x": {"probe", 1}, "z": {"calibration", 3}, "m": {"calibration", 2}} {
		row, ok := got[pool]
		if !ok || row.Component != want.component || *row.Amount != want.amount || !row.Approx {
			t.Errorf("pool %s: row %+v, want component %s amount %v", pool, row, want.component, want.amount)
		}
	}
	if _, ok := got["y"]; ok || len(got) != 3 {
		t.Errorf("a measured pool got a ledger row: %+v", got)
	}
}

func TestSettleMeasuredAppendsNothing(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	h := mustReserve(t, marker, p, request("t1", fund("p", 4)))
	addCost(t, ledger.Row{Task: "t1", Attempt: "a-t1", Pool: "p", Unit: "tokens", Component: "worker", ChargeID: "c1"}, 3)
	if r := remaining(t, marker, p, ""); r["p"] != 3 { // 10 - 3 recorded - 4 held: conservative until settled
		t.Fatalf("remaining before settle %v, want 3", r["p"])
	}
	got, err := Settle(h.ID, map[string]float64{"p": 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Used["p"] != 3 || got.State != "settled" {
		t.Fatalf("settled hold %+v", got)
	}
	if rows := ledgerRows(t); len(rows) != 1 {
		t.Fatalf("Settle appended rows for measured usage: %+v", rows)
	}
	if r := remaining(t, marker, p, ""); r["p"] != 7 {
		t.Fatalf("remaining after settle %v, want 7", r["p"])
	}
	// A measured amount above the bound is real and accepted.
	h2 := mustReserve(t, marker, p, request("t1", fund("p", 1)))
	addCost(t, ledger.Row{Task: "t1", Attempt: "a-t1", Pool: "p", Unit: "tokens", Component: "worker", ChargeID: "c2"}, 5)
	if got, err := Settle(h2.ID, map[string]float64{"p": 5}); err != nil || got.Used["p"] != 5 {
		t.Fatalf("measured above the bound: %+v %v", got, err)
	}
	if r := remaining(t, marker, p, ""); r["p"] != 2 {
		t.Fatalf("remaining %v, want 2", r["p"])
	}
}

func TestSettleIdempotent(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	h := mustReserve(t, marker, p, request("t1", fund("p", 4)))
	first, err := Settle(h.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second settle, even with different usage, returns the closed hold unchanged and charges nothing more.
	second, err := Settle(h.ID, map[string]float64{"p": 1})
	if err != nil || second.State != "settled" || !second.SettledAt.Equal(*first.SettledAt) || second.Used["p"] != 4 {
		t.Fatalf("second settle: %+v %v", second, err)
	}
	if len(ledgerRows(t)) != 1 {
		t.Fatalf("second settle appended rows: %+v", ledgerRows(t))
	}
	if r := remaining(t, marker, p, ""); r["p"] != 6 {
		t.Fatalf("remaining %v, want 6", r["p"])
	}
	if _, err := Reconcile(h.ID, nil, "late"); err == nil {
		t.Fatal("Reconcile of a settled hold accepted")
	}
}

// A Settle that died after appending its ledger row is retried: the retry writes the same ChargeID again and spend counts it once.
func TestSettleRetryDoesNotDoubleCount(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	h := mustReserve(t, marker, p, request("t1", fund("p", 4)))
	addCost(t, ledger.Row{Task: "t1", Attempt: "a-t1", Decision: "d-t1", Pool: "p", Unit: "tokens", Component: "worker", ChargeID: h.ID + ":p", Approx: true}, 4)
	if _, err := Settle(h.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(ledgerRows(t)) != 2 {
		t.Fatalf("want the replayed row to exist twice, got %+v", ledgerRows(t))
	}
	if r := remaining(t, marker, p, ""); r["p"] != 6 {
		t.Fatalf("remaining %v, want 6: the same charge recorded twice counts once", r["p"])
	}
}

func TestSettleRejectsBadUsage(t *testing.T) {
	marker := reserveEnv(t)
	h := mustReserve(t, marker, nil, request("t1", fund("p", 4)))
	if _, err := Settle("h-0000000000000000", nil); err == nil {
		t.Fatal("unknown hold settled")
	}
	for name, usage := range map[string]map[string]float64{
		"unknown pool": {"nope": 1}, "negative": {"p": -1}, "NaN": {"p": math.NaN()}, "Inf": {"p": math.Inf(1)},
	} {
		if _, err := Settle(h.ID, usage); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got := allHolds(t)[h.ID]; got.State != "reserved" || len(ledgerRows(t)) != 0 {
		t.Fatalf("a refused Settle changed state: %+v, %d rows", got, len(ledgerRows(t)))
	}
}

func TestReconcileRefusals(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	live := mustReserve(t, marker, p, request("t1", fund("p", 1))) // owned by this very process
	self, selfStart := os.Getpid(), int64(0)
	if st, err := run.ProcStart(self); err == nil {
		selfStart = st
	}
	inject(t, Hold{ID: "h-nopid", Run: testRun, Items: []Item{fund("p", 1)}, State: "reserved", CreatedAt: time.Now()})
	inject(t, Hold{ID: "h-nostart", Run: testRun, Items: []Item{fund("p", 1)}, State: "reserved", CreatedAt: time.Now(), OwnerPid: self})
	for id, why := range map[string]string{live.ID: "owner is this live process", "h-nopid": "no owner recorded", "h-nostart": "alive pid, unknown start"} {
		if _, err := Reconcile(id, nil, "owner crashed"); err == nil {
			t.Errorf("%s: reconcile accepted", why)
		}
	}
	if selfStart != 0 { // the same pid with the same start is the same process
		inject(t, Hold{ID: "h-same", Run: testRun, Items: []Item{fund("p", 1)}, State: "reserved", CreatedAt: time.Now(), OwnerPid: self, OwnerStart: selfStart})
		if _, err := Reconcile("h-same", nil, "owner crashed"); err == nil {
			t.Error("same pid and start: reconcile accepted")
		}
	}
	for _, reason := range []string{"", "  \t"} {
		if _, err := Reconcile(live.ID, nil, reason); err == nil {
			t.Errorf("reason %q accepted", reason)
		}
	}
	if _, err := Reconcile("h-missing", nil, "x"); err == nil {
		t.Error("unknown hold reconciled")
	}
	for id, h := range allHolds(t) {
		if h.State != "reserved" {
			t.Errorf("a refused Reconcile changed %s to %s", id, h.State)
		}
	}
	if len(ledgerRows(t)) != 0 {
		t.Fatalf("a refused Reconcile appended rows: %+v", ledgerRows(t))
	}
	if r := remaining(t, marker, p, ""); r["p"] != 10-float64(len(allHolds(t))) { // every one of them still holds 1
		t.Fatalf("remaining %v with %d holds of 1 outstanding", r["p"], len(allHolds(t)))
	}
}

func TestReconcileAfterOwnerExit(t *testing.T) {
	marker := reserveEnv(t)
	p := &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"a": {RunCap: 20}, "b": {RunCap: 20}}}}

	// A short-lived child: learn its start time while it lives, let it exit, and make it the owner of a hold.
	cmd, out := helperCmd("wait")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	start, err := run.ProcStart(pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Skipf("no process start time on this platform: %v", err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if _, err := run.ProcStart(pid); !errors.Is(err, run.ErrNoProc) {
		t.Skipf("pid %d still resolves after its exit (reused?): %v", pid, err)
	}
	inject(t, Hold{ID: "h-dead", Run: testRun, Task: "t1", Attempt: "a-t1", Decision: "d-t1", State: "reserved", CreatedAt: time.Now().UTC(),
		Items:    []Item{fund("a", 4), {Pool: "b", Unit: "tokens", Amount: 3, Kind: "probe"}},
		OwnerPid: pid, OwnerStart: start})
	if r := remaining(t, marker, p, ""); r["a"] != 16 || r["b"] != 17 {
		t.Fatalf("remaining with the dead owner's hold %v", r)
	}
	if _, err := Reconcile("h-dead", nil, ""); err == nil {
		t.Fatal("Reconcile without a reason accepted")
	}
	if _, err := Reconcile("h-dead", map[string]float64{"zzz": 1}, "owner crashed"); err == nil {
		t.Fatal("Reconcile with a pool outside the hold accepted")
	}
	got, err := Reconcile("h-dead", map[string]float64{"a": 1}, "owner crashed; provider dashboard shows 1")
	if err != nil {
		t.Fatalf("Reconcile of a dead owner's hold: %v", err)
	}
	if got.State != "reconciled" || got.Note != "owner crashed; provider dashboard shows 1" || got.SettledAt == nil || got.Used["a"] != 1 || got.Used["b"] != 3 {
		t.Fatalf("reconciled hold %+v", got)
	}
	rows := map[string]ledger.Row{}
	for _, r := range ledgerRows(t) {
		rows[r.Pool] = r
	}
	if a := rows["a"]; len(rows) != 2 || a.Amount == nil || *a.Amount != 1 || a.Component != "worker" || a.ChargeID != "h-dead:a" || !strings.Contains(a.Notes, "owner crashed") {
		t.Fatalf("row for the pool the user gave: %+v", rows)
	}
	if b := rows["b"]; b.Amount == nil || *b.Amount != 3 || b.Component != "probe" || b.ChargeID != "h-dead:b" || !b.Approx || b.Notes != "charged at reserved bound: usage unknown" {
		t.Fatalf("row for the pool charged at its bound: %+v", rows)
	}
	if r := remaining(t, marker, p, ""); r["a"] != 19 || r["b"] != 17 {
		t.Fatalf("remaining after reconcile %v, want a=19 b=17", r)
	}
	if _, err := Reconcile("h-dead", nil, "again"); err == nil {
		t.Fatal("a reconciled hold was reconciled again")
	}
	if out, _ := Outstanding(); len(out) != 0 {
		t.Fatalf("outstanding %+v", out)
	}
}

func TestReconcileReusedPid(t *testing.T) {
	reserveEnv(t)
	st, err := run.ProcStart(os.Getpid())
	if err != nil || st == 0 {
		t.Skipf("no process start time on this platform: %d %v", st, err)
	}
	// Same pid, different start time: the pid now belongs to another process, so the owner is gone.
	inject(t, Hold{ID: "h-reused", Run: testRun, Items: []Item{fund("p", 2)}, State: "reserved", CreatedAt: time.Now().UTC(), OwnerPid: os.Getpid(), OwnerStart: st + 1})
	if got, err := Reconcile("h-reused", nil, "pid reused"); err != nil || got.State != "reconciled" {
		t.Fatalf("reused pid: %+v %v", got, err)
	}
	if rows := ledgerRows(t); len(rows) != 1 || *rows[0].Amount != 2 || rows[0].ChargeID != "h-reused:p" {
		t.Fatalf("unknown usage must be charged at the bound: %+v", rows)
	}
	// A pool the user reports as unused is released without a charge.
	inject(t, Hold{ID: "h-zero", Run: testRun, Items: []Item{fund("p", 5)}, State: "reserved", CreatedAt: time.Now().UTC(), OwnerPid: os.Getpid(), OwnerStart: st + 1})
	if got, err := Reconcile("h-zero", map[string]float64{"p": 0}, "nothing ran"); err != nil || got.Used["p"] != 0 || got.State != "reconciled" {
		t.Fatalf("zero usage: %+v %v", got, err)
	}
	if rows := ledgerRows(t); len(rows) != 1 {
		t.Fatalf("a zero usage appended a row: %+v", rows)
	}
}

func TestScopesTaskAndRun(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 5, 8)
	mustReserve(t, marker, p, request("t1", fund("p", 5)))
	_, err := Reserve(marker, p, request("t1", fund("p", 1)))
	if ei := wantInsufficient(t, err, "p", "task"); ei.Need != 1 || ei.Remaining != 0 {
		t.Fatalf("task scope: %+v", ei)
	}
	mustReserve(t, marker, p, request("t2", fund("p", 3))) // fits the task and exactly fills the run
	_, err = Reserve(marker, p, request("t3", fund("p", 1)))
	if ei := wantInsufficient(t, err, "p", "run"); ei.Remaining != 0 {
		t.Fatalf("run scope: %+v", ei)
	}
	// A hold without a task counts against the run only: it uses none of any task's cap.
	q := capProfile("q", 2, 10)
	mustReserve(t, marker, q, request("", fund("q", 5)))
	mustReserve(t, marker, q, request("t1", fund("q", 2)))
	mustReserve(t, marker, q, request("t2", fund("q", 2)))
	_, err = Reserve(marker, q, request("t3", fund("q", 2))) // t3 has task room, the run has 1 left
	if ei := wantInsufficient(t, err, "q", "run"); ei.Remaining != 1 {
		t.Fatalf("run scope: %+v", ei)
	}
}

func TestRaisesAreHonoured(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 5, 8)
	mustReserve(t, marker, p, request("t1", fund("p", 5)))
	if _, err := Reserve(marker, p, request("t1", fund("p", 1))); err == nil {
		t.Fatal("over the task cap")
	}
	if err := Raise(marker, "p", 4, "user raised"); err != nil { // task cap 9, run cap 12
		t.Fatal(err)
	}
	if err := Raise(marker, "no-cap-pool", 100, "user raised"); err != nil { // a pool without a cap stays uncapped
		t.Fatal(err)
	}
	mustReserve(t, marker, p, request("t1", fund("p", 4)))
	if r := remaining(t, marker, p, "t1"); r["p"] != 0 {
		t.Fatalf("task remaining %v, want 0", r["p"])
	}
	if r := remaining(t, marker, p, ""); r["p"] != 3 {
		t.Fatalf("run remaining %v, want 3", r["p"])
	}
	if _, err := Reserve(marker, p, request("t1", fund("p", 1))); err == nil {
		t.Fatal("over the raised task cap")
	}
}

func TestUncappedPoolFits(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("capped", 0, 10)
	h := mustReserve(t, marker, p, request("t1", fund("free", 1e12), fund("capped", 10)))
	if len(h.Items) != 2 || len(allHolds(t)) != 1 {
		t.Fatalf("hold %+v", h)
	}
	r := remaining(t, marker, p, "t1")
	if _, ok := r["free"]; ok || r["capped"] != 0 {
		t.Fatalf("remaining %v: pools without a cap must be absent", r)
	}
	if _, err := Reserve(marker, p, request("t1", fund("capped", 1))); err == nil {
		t.Fatal("capped pool not enforced")
	}
}

func TestNoMarkerNoBudget(t *testing.T) {
	reserveEnv(t)
	for name, p := range map[string]*contract.Profile{
		"nil profile": nil, "no budget": {}, "budget without pools": {Budget: &contract.Budget{MaxReviewRounds: 2}},
	} {
		h, err := Reserve("", p, ReserveRequest{Run: "any", Items: []Item{fund("whatever", 1e9)}})
		if err != nil || h.State != "reserved" {
			t.Errorf("%s: %+v %v", name, h, err)
		}
		if r, err := Remaining("", p, "t"); err != nil || len(r) != 0 {
			t.Errorf("%s: Remaining = %v, %v", name, r, err)
		}
	}
	if len(allHolds(t)) != 3 {
		t.Fatalf("holds were not recorded without a budget: %d", len(allHolds(t)))
	}
	if _, err := Remaining("", capProfile("p", 0, 1), ""); err == nil {
		t.Fatal("Remaining with pools and no marker accepted")
	}
}

func TestRemainingTighterOfTaskAndRun(t *testing.T) {
	marker := reserveEnv(t)
	p := &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{
		"both": {TaskCap: 5, RunCap: 12}, "taskonly": {TaskCap: 4}, "runonly": {RunCap: 9}, "none": {}}}}
	mustReserve(t, marker, p, request("t1", fund("both", 4), fund("runonly", 3)))
	mustReserve(t, marker, p, request("t2", fund("both", 5), fund("taskonly", 2)))
	for _, c := range []struct {
		task string
		want map[string]float64
	}{
		{"", map[string]float64{"both": 3, "runonly": 6}},                      // run scope; a task cap alone does not cap the run
		{"t1", map[string]float64{"both": 1, "taskonly": 4, "runonly": 6}},     // task 5-4 is tighter than run 12-9
		{"t2", map[string]float64{"both": 0, "taskonly": 2, "runonly": 6}},     // task 5-5
		{"t3", map[string]float64{"both": 3, "taskonly": 4, "runonly": 9 - 3}}, // run 12-9 is tighter than task 5
	} {
		got := remaining(t, marker, p, c.task)
		if len(got) != len(c.want) {
			t.Errorf("task %q: %v, want %v", c.task, got, c.want)
			continue
		}
		for pool, w := range c.want {
			if got[pool] != w {
				t.Errorf("task %q pool %s: %v, want %v", c.task, pool, got[pool], w)
			}
		}
	}
}

func TestCalibrationCaps(t *testing.T) {
	t.Run("missing caps", func(t *testing.T) {
		marker := reserveEnv(t)
		_, err := Reserve(marker, nil, request("c", calib("cal", "tokens", 1, 1)))
		var ei *ErrInsufficient
		if err == nil || errors.As(err, &ei) {
			t.Fatalf("calibration without CalibrationCaps: %v, want a plain error", err)
		}
		if len(allHolds(t)) != 0 {
			t.Fatal("hold recorded")
		}
	})
	reserveCal := func(t *testing.T, marker string, caps *CalibrationCaps, items ...Item) (*Hold, error) {
		r := request("c", items...)
		r.Calibration = caps
		return Reserve(marker, nil, r)
	}
	t.Run("calls", func(t *testing.T) {
		marker := reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 3, PoolCaps: map[string]float64{"cal": 100}}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 1, 2)); err != nil {
			t.Fatal(err)
		}
		_, err := reserveCal(t, marker, caps, calib("cal", "tokens", 1, 2))
		if ei := wantInsufficient(t, err, "calls", "calibration"); ei.Need != 2 || ei.Remaining != 1 {
			t.Fatalf("%+v", ei)
		}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 1, 1)); err != nil {
			t.Fatal(err)
		}
		_, err = reserveCal(t, marker, caps, calib("cal", "tokens", 0, 1))
		wantInsufficient(t, err, "calls", "calibration")
		// Calls of non-calibration items are not calibration calls.
		if _, err := Reserve(marker, nil, request("c", Item{Pool: "cal", Unit: "tokens", Amount: 1, Calls: 50, Kind: "probe"})); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("settled calibration rows count once per charge", func(t *testing.T) {
		marker := reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 3, PoolCaps: map[string]float64{"cal": 10}}
		row := ledger.Row{Task: "c", Attempt: "a-c", Pool: "cal", Unit: "tokens", Component: "calibration"}
		for _, id := range []string{"c1", "c1", "c2"} { // c1 recorded twice
			row.ChargeID = id
			addCost(t, row, 2)
		}
		addCost(t, ledger.Row{Run: "other-run", Pool: "cal", Unit: "tokens", Component: "calibration", ChargeID: "x1"}, 99) // not this run
		addCost(t, ledger.Row{Pool: "cal", Unit: "tokens", Component: "worker", ChargeID: "w1"}, 99)                        // not calibration
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 5, 2)); err == nil {
			t.Fatal("2 calls used + 2 requested fit under 3")
		}
		_, err := reserveCal(t, marker, caps, calib("cal", "tokens", 7, 1))
		if ei := wantInsufficient(t, err, "cal", "calibration"); ei.Need != 7 || ei.Remaining != 6 { // 4 used of 10
			t.Fatalf("%+v", ei)
		}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 6, 1)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("pool caps", func(t *testing.T) {
		marker := reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 99, PoolCaps: map[string]float64{"cal": 10}}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 6, 1)); err != nil {
			t.Fatal(err)
		}
		_, err := reserveCal(t, marker, caps, calib("cal", "tokens", 5, 1))
		if ei := wantInsufficient(t, err, "cal", "calibration"); ei.Need != 5 || ei.Remaining != 4 {
			t.Fatalf("%+v", ei)
		}
		_, err = reserveCal(t, marker, caps, calib("other", "tokens", 0, 0)) // not in PoolCaps: not authorized at all
		wantInsufficient(t, err, "other", "calibration")
		if len(allHolds(t)) != 1 {
			t.Fatal("a refused request recorded a hold")
		}
	})
	t.Run("cash", func(t *testing.T) {
		marker := reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 99, PoolCaps: map[string]float64{"cash": 5, "cal": 100}, MaxCashUSD: 1}
		if _, err := reserveCal(t, marker, caps, calib("cash", "usd", 0.5, 1)); err != nil {
			t.Fatal(err)
		}
		_, err := reserveCal(t, marker, caps, calib("cash", "usd", 0.75, 1))
		if ei := wantInsufficient(t, err, "cash_usd", "calibration"); ei.Need != 0.75 || ei.Remaining != 0.5 {
			t.Fatalf("%+v", ei)
		}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 90, 1)); err != nil { // other units are not cash
			t.Fatal(err)
		}
		if _, err := reserveCal(t, marker, caps, calib("cash", "usd", 0.5, 1)); err != nil { // exactly fills the cash cap
			t.Fatal(err)
		}
		// Settled cash counts as well: a ledger row in usd uses the cash cap up.
		addCost(t, ledger.Row{Task: "c", Pool: "cash", Unit: "usd", Component: "calibration", ChargeID: "u1"}, 0.01)
		if _, err := reserveCal(t, marker, caps, calib("cash", "usd", 0, 0)); err == nil {
			t.Fatal("cash already over the cap accepted")
		}
		noCash := &CalibrationCaps{MaxCalls: 9, PoolCaps: map[string]float64{"cash": 5}} // MaxCashUSD 0 = no cash
		_, err = reserveCal(t, reserveEnv(t), noCash, calib("cash", "usd", 0.01, 1))
		wantInsufficient(t, err, "cash_usd", "calibration")
	})
	t.Run("unknown usage keeps counting its reserved calls", func(t *testing.T) {
		marker := reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 4, PoolCaps: map[string]float64{"cal": 100}}
		h, err := reserveCal(t, marker, caps, calib("cal", "tokens", 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Settle(h.ID, nil); err != nil { // usage unknown: one row at the bound stands for the 3 calls reserved
			t.Fatal(err)
		}
		rows := ledgerRows(t)
		if len(rows) != 1 || rows[0].Component != "calibration" {
			t.Fatalf("rows %+v", rows)
		}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 1, 2)); err == nil {
			t.Fatal("3 reserved calls released as 1")
		}
		if _, err := reserveCal(t, marker, caps, calib("cal", "tokens", 1, 1)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("without a marker only this run's rows count", func(t *testing.T) {
		reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 1, PoolCaps: map[string]float64{"cal": 100}}
		addCost(t, ledger.Row{Pool: "cal", Unit: "tokens", Component: "calibration", ChargeID: "c1"}, 1)
		if _, err := reserveCal(t, "", caps, calib("cal", "tokens", 1, 1)); err == nil {
			t.Fatal("a settled calibration call was ignored")
		}
		r := request("c", calib("cal", "tokens", 1, 1))
		r.Run, r.Calibration = "fresh-run", caps
		if _, err := Reserve("", nil, r); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the profile caps apply to calibration items too", func(t *testing.T) {
		marker := reserveEnv(t)
		caps := &CalibrationCaps{MaxCalls: 9, PoolCaps: map[string]float64{"cal": 100}}
		r := request("", calib("cal", "tokens", 6, 1))
		r.Calibration = caps
		_, err := Reserve(marker, capProfile("cal", 0, 5), r)
		wantInsufficient(t, err, "cal", "run")
		if _, err := Reserve(marker, capProfile("cal", 0, 6), r); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCorruptStoreFailsClosed(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	path := filepath.Join(holdsDir(), "holds.json")
	if err := os.MkdirAll(holdsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"not json":      "{not json",
		"empty file":    "",
		"null":          "null",
		"array":         "[]",
		"null hold":     `{"h-1": null}`,
		"id mismatch":   `{"h-2": {"id":"h-1","state":"reserved"}}`,
		"unknown state": `{"h-1": {"id":"h-1","state":"lost"}}`,
		"negative item": `{"h-1": {"id":"h-1","state":"reserved","items":[{"pool":"p","unit":"tokens","amount":-5,"kind":"funding"}]}}`,
		"trailing junk": `{"h-1": {"id":"h-1","state":"reserved"}} x`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Reserve(marker, p, request("t1", fund("p", 1))); err == nil {
			t.Errorf("%s: Reserve treated a corrupt store as usable", name)
		}
		if _, err := Reserve("", nil, request("t1", fund("p", 1))); err == nil {
			t.Errorf("%s: Reserve without a budget treated a corrupt store as usable", name)
		}
		if _, err := Outstanding(); err == nil {
			t.Errorf("%s: Outstanding", name)
		}
		if _, err := HoldsFor("a"); err == nil {
			t.Errorf("%s: HoldsFor", name)
		}
		if _, err := Remaining(marker, p, ""); err == nil {
			t.Errorf("%s: Remaining", name)
		}
		if _, err := Settle("h-1", nil); err == nil {
			t.Errorf("%s: Settle", name)
		}
		if _, err := Reconcile("h-1", nil, "x"); err == nil {
			t.Errorf("%s: Reconcile", name)
		}
		if b, _ := os.ReadFile(path); string(b) != content {
			t.Errorf("%s: the corrupt store was rewritten: %q", name, b)
		}
	}
	// A directory where the file should be is unreadable, not empty.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Outstanding(); err == nil {
		t.Error("unreadable store treated as empty")
	}
}

func TestTallySpendCostRows(t *testing.T) {
	amt := func(v float64) *float64 { return &v }
	n := 7
	rows := []ledger.Row{
		{Kind: "cost", Run: "r", Task: "t", Attempt: "a1", Pool: "p", Amount: amt(5), ChargeID: "c1"},
		{Kind: "cost", Run: "r", Task: "t", Attempt: "a1", Pool: "p", Amount: amt(5), ChargeID: "c1"}, // the same charge recorded twice
		{Kind: "cost", Run: "r", Task: "t", Attempt: "a2", Pool: "p", Amount: amt(3), ChargeID: "c1"}, // another attempt: another charge
		{Kind: "cost", Run: "r", Task: "t", Pool: "p", Amount: amt(2)},                                // no ChargeID: cannot be matched
		{Kind: "cost", Run: "r", Task: "t", Pool: "p", Amount: amt(2)},
		{Kind: "cost", Run: "r", Task: "t2", Attempt: "a1", Pool: "q", Amount: amt(4), ChargeID: "c9"}, // another task
		{Kind: "cost", Run: "other", Pool: "p", Amount: amt(100)},                                      // another run
		{Kind: "cost", Run: "r", Task: "t", Amount: amt(50)},                                           // no pool
		{Kind: "cost", Run: "r", Task: "t", Pool: "p"},                                                 // no amount
		{Kind: "cost", Run: "r", Task: "t", Pool: "p", Amount: amt(-9), ChargeID: "neg"},               // a negative amount never lowers spend
		{Kind: "task", Run: "r", Task: "t", Pool: "p", Amount: amt(60)},                                // not a cost row
		{Run: "r", Task: "t", Worker: &ledger.AgentModel{Agent: "claude"}, WorkerTokens: &n},           // legacy rows count as before
	}
	task := tallySpend(rows, "r", "t")
	if task["p"] != 12 || task["claude_tokens"] != 7 || len(task) != 2 {
		t.Fatalf("task spend %v, want p=12 claude_tokens=7", task)
	}
	all := tallySpend(rows, "r", "")
	if all["p"] != 12 || all["q"] != 4 || all["claude_tokens"] != 7 || len(all) != 3 {
		t.Fatalf("run spend %v, want p=12 q=4 claude_tokens=7", all)
	}
}

// Append stamps rows in local time and the marker holds UTC; comparing those strings (what LoadStrict(since) does) drops the rows
// of a run's first hours west of UTC, which would undercount spend at the gate.
func TestRowsOfAFreshRunCountWestOfUTC(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("p", 0, 10)
	local := time.Local
	time.Local = time.FixedZone("west", -8*3600)
	t.Cleanup(func() { time.Local = local })
	m, err := run.Load(marker)
	if err != nil {
		t.Fatal(err)
	}
	m.StartedAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := writeTestMarker(marker, m); err != nil {
		t.Fatal(err)
	}
	addCost(t, ledger.Row{Task: "t1", Pool: "p", Unit: "tokens", Component: "worker", ChargeID: "c1"}, 4)
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339) // a row from before the run started is not this run's spend
	b, _ := os.ReadFile(ledger.Path())
	if err := os.WriteFile(ledger.Path(), append(b, []byte(`{"kind":"cost","run":"`+testRun+`","pool":"p","unit":"tokens","amount":3,"charge_id":"old","recorded_at":"`+old+"\"}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := remaining(t, marker, p, ""); r["p"] != 6 {
		t.Fatalf("remaining %v, want 6: the row recorded after the start must count and the older one must not", r["p"])
	}
}

// A worker's plan sets review capacity aside (review_reserve). The review that follows reserves under its own attempt: it takes
// the worker's reserve over in the SAME transaction, so the capacity is not counted twice, and a review that does not fit
// leaves the worker's reserve in place.
func TestReserveHandoverOfReviewReserve(t *testing.T) {
	marker := reserveEnv(t)
	p := capProfile("claude", 0, 10)
	worker := request("t1", Item{Pool: "codex", Unit: "tokens", Amount: 1, Kind: "probe"}, fund("codex", 5), Item{Pool: "claude", Unit: "tokens", Amount: 8, Kind: "review_reserve"})
	worker.Attempt = "worker-attempt"
	wh := mustReserve(t, marker, p, worker)
	if r := remaining(t, marker, p, ""); r["claude"] != 2 {
		t.Fatalf("remaining after the worker's reserve = %v, want claude 2", r)
	}

	review := request("t1", Item{Pool: "claude", Unit: "tokens", Amount: 1, Kind: "probe"}, Item{Pool: "claude", Unit: "tokens", Amount: 4, Kind: "review_funding"})
	review.Attempt = "review-attempt"
	if _, err := Reserve(marker, p, review); err == nil {
		t.Fatal("a review that does not fit beside the worker's reserve was accepted without a handover")
	}
	review.Handover = "other-attempt" // an attempt that holds no reserve frees nothing
	if _, err := Reserve(marker, p, review); err == nil {
		t.Fatal("handover from an unrelated attempt freed capacity")
	}
	huge := review
	huge.Items = []Item{{Pool: "claude", Unit: "tokens", Amount: 11, Kind: "review_funding"}}
	huge.Handover = "worker-attempt"
	wantInsufficient(t, func() error { _, err := Reserve(marker, p, huge); return err }(), "claude", "task")
	if hs := allHolds(t); len(hs[wh.ID].Items) != 3 {
		t.Fatalf("a failed reservation dropped the worker's reserve: %+v", hs[wh.ID].Items)
	}

	review.Handover = "worker-attempt"
	rh := mustReserve(t, marker, p, review)
	hs := allHolds(t)
	var kinds []string
	for _, it := range hs[wh.ID].Items {
		kinds = append(kinds, it.Kind)
	}
	if strings.Join(kinds, ",") != "probe,funding" {
		t.Fatalf("worker hold after the handover holds %v, want its probe and funding only", kinds)
	}
	if hs[rh.ID].State != "reserved" || len(hs[rh.ID].Items) != 2 {
		t.Fatalf("review hold: %+v", hs[rh.ID])
	}
	if r := remaining(t, marker, p, ""); r["claude"] != 5 {
		t.Fatalf("remaining after the handover = %v, want claude 5 (10 - review 1 + 4)", r)
	}

	// another task's reserve is never taken over
	other := request("t2", Item{Pool: "claude", Unit: "tokens", Amount: 1, Kind: "review_reserve"})
	other.Attempt = "worker-attempt"
	mustReserve(t, marker, p, other)
	rev2 := request("t1", Item{Pool: "claude", Unit: "tokens", Amount: 1, Kind: "probe"})
	rev2.Attempt, rev2.Handover = "review-2", "worker-attempt"
	mustReserve(t, marker, p, rev2)
	for _, h := range allHolds(t) {
		if h.Task == "t2" && (len(h.Items) != 1 || h.Items[0].Kind != "review_reserve") {
			t.Fatalf("a handover reached another task: %+v", h)
		}
	}
}
