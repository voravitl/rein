package budget

// Atomic reservations of worst-case spend (docs/ROUTING_SELECTION_DESIGN.md, "Decision and execution"). The capacity check
// and the hold are recorded under one cross-process lock, so concurrent decisions cannot each spend the same remaining
// quota. A hold stays until Settle or Reconcile closes it: a crash retains the reservation instead of releasing it, and
// unknown usage is charged at the reserved bound, never released as zero.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/run"
)

// Hold states.
const (
	stReserved   = "reserved"
	stSettled    = "settled"
	stReconciled = "reconciled"
)

// kindComponent maps an item kind to the Component of the ledger row that charges it.
var kindComponent = map[string]string{"probe": "probe", "funding": "worker", "review_funding": "review", "review_reserve": "review", "calibration": "calibration"}

// lockWait is how long a store operation waits for the holds lock (a variable so tests can shorten it).
var lockWait = 5 * time.Second

// Item is one worst-case claim on one pool.
type Item struct {
	Pool   string  `json:"pool"`            // provider/account/pool/window qualifier (the key used in profile budget.pools and in ledger cost rows)
	Unit   string  `json:"unit"`            // native unit of Pool; one pool is never requested in two units
	Amount float64 `json:"amount"`          // worst case in Unit, finite >= 0
	Calls  int     `json:"calls,omitempty"` // worst-case model calls
	// Kind: probe (the availability probe), funding (the attempt's own worker work), review_funding (a review that pays for itself),
	// review_reserve (capacity a worker's plan sets aside for reviews that will reserve and be charged under their own attempts),
	// calibration.
	Kind string `json:"kind"`
}

// Hold is one reservation of worst-case spend.
type Hold struct {
	ID         string             `json:"id"`
	Run        string             `json:"run"`
	Task       string             `json:"task,omitempty"`
	Attempt    string             `json:"attempt,omitempty"`
	Decision   string             `json:"decision,omitempty"`
	Items      []Item             `json:"items"`
	State      string             `json:"state"` // reserved | settled | reconciled
	CreatedAt  time.Time          `json:"created_at"`
	SettledAt  *time.Time         `json:"settled_at,omitempty"`
	Used       map[string]float64 `json:"used,omitempty"` // pool -> amount charged at settlement
	OwnerPid   int                `json:"owner_pid"`      // os.Getpid() of the reserving process
	OwnerStart int64              `json:"owner_start"`    // run.ProcStart of the owner; 0 if unknown
	Note       string             `json:"note,omitempty"`
}

// CalibrationCaps are the fixed caps of the calibration lane, counted per run.
type CalibrationCaps struct {
	MaxCalls   int
	PoolCaps   map[string]float64
	MaxCashUSD float64
}

// ReserveRequest asks for one hold covering every item, or none.
type ReserveRequest struct {
	Run, Task, Attempt, Decision string
	Items                        []Item
	Calibration                  *CalibrationCaps // required when any item has Kind "calibration"
	// Handover names an attempt of the same run and task whose outstanding review_reserve items this request takes over: they are
	// dropped in the same transaction that records the new hold, so the capacity a worker's plan set aside for reviews funds the
	// review instead of being counted twice. A request that does not fit leaves them in place.
	Handover string
}

// ErrInsufficient says which pool of which scope cannot cover a request.
type ErrInsufficient struct {
	Pool, Scope     string  // Scope: task | run | calibration
	Need, Remaining float64 // Need is the whole request for Pool; Remaining is negative when the scope is already over
}

func (e *ErrInsufficient) Error() string {
	return fmt.Sprintf("insufficient %s budget in pool %q: need %g, remaining %g", e.Scope, e.Pool, e.Need, e.Remaining)
}

// ---- store: <contract index>/holds/holds.json (object id -> Hold) guarded by holds.lock ----

func holdsDir() string { return filepath.Join(contract.IndexDir(), "holds") }

// withStore runs fn on the holds under the cross-process lock; when fn reports a change the holds are written back (temp
// file, fsync, rename) before the lock is released. A store that cannot be read or parsed is an error, never an empty store.
// fn must not call withStore (the lock is not re-entrant).
func withStore(fn func(holds map[string]*Hold) (changed bool, err error)) error {
	dir := holdsDir()
	unlock, err := run.LockFile(filepath.Join(dir, "holds.lock"), lockWait)
	if err != nil {
		return fmt.Errorf("holds store: %w", err)
	}
	defer unlock()
	path := filepath.Join(dir, "holds.json")
	holds, err := readHolds(path)
	if err != nil {
		return err
	}
	if changed, err := fn(holds); err != nil || !changed {
		return err
	}
	return writeHolds(dir, path, holds)
}

func readHolds(path string) (map[string]*Hold, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Hold{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("holds store: %w", err)
	}
	var holds map[string]*Hold
	if err := json.Unmarshal(b, &holds); err != nil {
		return nil, fmt.Errorf("holds store %s is corrupt: %w", path, err)
	}
	if holds == nil {
		return nil, fmt.Errorf("holds store %s is corrupt: not an object", path)
	}
	for id, h := range holds {
		if h == nil || h.ID != id || (h.State != stReserved && h.State != stSettled && h.State != stReconciled) {
			return nil, fmt.Errorf("holds store %s is corrupt: bad hold %q", path, id)
		}
		for _, it := range h.Items {
			if err := checkItem(it); err != nil { // a negative amount would silently shrink what is held
				return nil, fmt.Errorf("holds store %s is corrupt: hold %q: %w", path, id, err)
			}
		}
	}
	return holds, nil
}

// ponytail: every mutation rewrites the whole file and settled holds are never pruned, so cost grows with the number of holds
// ever made (fine to a few thousand); archive closed holds when that matters. There is no directory fsync after the rename: a
// power loss may revert the last mutation, but the launched work dies with the machine. A crash between CreateTemp and Rename
// leaves a stray .holds-*.tmp that nothing sweeps.
func writeHolds(dir, path string, holds map[string]*Hold) error {
	b, err := json.MarshalIndent(holds, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".holds-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func newID(taken map[string]*Hold) (string, error) {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		if id := "h-" + hex.EncodeToString(b[:]); taken[id] == nil {
			return id, nil
		}
	}
}

// ---- validation and shared accounting ----

func okAmount(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 }

func checkItem(it Item) error {
	switch {
	case it.Pool == "" || it.Unit == "":
		return errors.New("item needs a pool and a unit")
	case kindComponent[it.Kind] == "":
		return fmt.Errorf("item kind %q: want probe, funding, review_funding, review_reserve or calibration", it.Kind)
	case !okAmount(it.Amount):
		return fmt.Errorf("item amount %v for pool %q: want finite >= 0", it.Amount, it.Pool)
	case it.Calls < 0:
		return fmt.Errorf("item calls %d for pool %q: want >= 0", it.Calls, it.Pool)
	}
	return nil
}

func checkCaps(c *CalibrationCaps) error {
	if c == nil {
		return errors.New("calibration items need CalibrationCaps")
	}
	if c.MaxCalls < 0 || !okAmount(c.MaxCashUSD) {
		return errors.New("calibration caps: want MaxCalls >= 0 and finite MaxCashUSD >= 0")
	}
	for pool, v := range c.PoolCaps {
		if !okAmount(v) {
			return fmt.Errorf("calibration cap for pool %q: want finite >= 0", pool)
		}
	}
	return nil
}

// newCharge reports whether r is the first record of its charge. A row repeating a seen (Attempt, ChargeID) records the same
// charge twice (a retried Settle, a replayed append) and counts once; rows without a ChargeID cannot be matched, so all count.
func newCharge(seen map[[2]string]bool, r ledger.Row) bool {
	if r.ChargeID == "" {
		return true
	}
	k := [2]string{r.Attempt, r.ChargeID}
	if seen[k] {
		return false
	}
	seen[k] = true
	return true
}

// runState is one run's ledger rows and holds, read under the store lock so they are consistent with each other.
type runState struct {
	run    string
	marker string // marker path; raises live beside it
	rows   []ledger.Row
	holds  map[string]*Hold
}

// loadRun reads the marker and the ledger the way Check does (strictly: a corrupt ledger is an error, not a lower spend).
// Rows are selected by parsed RecordedAt, not by LoadStrict's string comparison: Append stamps local time and the marker
// holds UTC, so the string comparison drops rows of a run's first hours west of UTC. An unparsable time counts (over-counts).
func loadRun(markerPath string, holds map[string]*Hold) (runState, error) {
	m, err := run.Load(markerPath)
	if err != nil {
		return runState{}, fmt.Errorf("load marker: %w", err)
	}
	start, err := time.Parse(time.RFC3339, m.StartedAt)
	if err != nil {
		return runState{}, fmt.Errorf("parse started_at: %w", err)
	}
	all, err := ledger.LoadStrict("")
	if err != nil {
		return runState{}, fmt.Errorf("load ledger: %w", err)
	}
	rows := all[:0]
	for _, r := range all {
		if t, err := time.Parse(time.RFC3339, r.RecordedAt); err != nil || !t.Before(start) {
			rows = append(rows, r)
		}
	}
	return runState{run: m.Run, marker: markerPath, rows: rows, holds: holds}, nil
}

// room is, per capped pool, cap (+ raises) - ledger spend - outstanding holds: for one task when task != "", else for the
// whole run. Pools without a cap in that scope are absent.
func (s runState) room(b *contract.Budget, task string) map[string]float64 {
	caps := getEffectiveCaps(b, task != "")
	for _, r := range loadRaises(s.run, s.marker) {
		if c, ok := caps[r.Pool]; ok {
			caps[r.Pool] = c + r.Amount
		}
	}
	spend := tallySpend(s.rows, s.run, task)
	for _, h := range s.holds {
		if h.State == stReserved && h.Run == s.run && (task == "" || h.Task == task) {
			for _, it := range h.Items {
				spend[it.Pool] += it.Amount
			}
		}
	}
	out := make(map[string]float64, len(caps))
	for pool, c := range caps {
		out[pool] = c - spend[pool]
	}
	return out
}

// check fails with *ErrInsufficient for the first pool (by name) whose whole request exceeds the headroom of its task or run.
// A scope without a cap does not constrain.
func (s runState) check(b *contract.Budget, task string, need map[string]float64) error {
	rooms := map[string]map[string]float64{"run": s.room(b, "")}
	if task != "" {
		rooms["task"] = s.room(b, task)
	}
	for _, pool := range slices.Sorted(maps.Keys(need)) {
		for _, scope := range []string{"task", "run"} {
			if r, capped := rooms[scope][pool]; capped && need[pool] > r {
				return &ErrInsufficient{Pool: pool, Scope: scope, Need: need[pool], Remaining: r}
			}
		}
	}
	return nil
}

// checkCalibration enforces the calibration lane's caps on the calibration items of a request. Used = this run's calibration
// cost rows (each distinct charge once) + this run's outstanding calibration holds.
// ponytail: caps are per run (rows since the run started); a lifetime cap needs an all-time tally.
func (s runState) checkCalibration(c *CalibrationCaps, items []Item) error {
	// A row that Settle/Reconcile wrote at the reserved bound stands for every call its hold reserved, not for one.
	reservedCalls := map[string]int{}
	for _, h := range s.holds {
		if h.State != stReserved {
			for _, it := range h.Items {
				if it.Kind == "calibration" {
					reservedCalls[h.ID+":"+it.Pool] += it.Calls
				}
			}
		}
	}
	var calls int
	var cash float64
	pools := map[string]float64{}
	use := func(pool, unit string, amount float64) {
		pools[pool] += amount
		if unit == "usd" {
			cash += amount
		}
	}
	seen := map[[2]string]bool{}
	for _, r := range s.rows {
		if r.Run != s.run || r.Kind != "cost" || r.Component != "calibration" || !newCharge(seen, r) {
			continue
		}
		calls += max(1, reservedCalls[r.ChargeID])
		if r.Pool != "" && r.Amount != nil {
			use(r.Pool, r.Unit, max(*r.Amount, 0))
		}
	}
	for _, h := range s.holds {
		if h.State == stReserved && h.Run == s.run {
			for _, it := range h.Items {
				if it.Kind == "calibration" {
					calls += it.Calls
					use(it.Pool, it.Unit, it.Amount)
				}
			}
		}
	}

	var wantCalls int
	var wantCash float64
	var wantUSD bool
	want := map[string]float64{}
	for _, it := range items {
		if it.Kind == "calibration" {
			wantCalls += it.Calls
			want[it.Pool] += it.Amount
			if it.Unit == "usd" {
				wantUSD, wantCash = true, wantCash+it.Amount
			}
		}
	}
	if calls+wantCalls > c.MaxCalls {
		return &ErrInsufficient{Pool: "calls", Scope: "calibration", Need: float64(wantCalls), Remaining: float64(c.MaxCalls - calls)}
	}
	for _, pool := range slices.Sorted(maps.Keys(want)) {
		limit, ok := c.PoolCaps[pool] // a pool without a calibration cap is not authorized at all
		if !ok || pools[pool]+want[pool] > limit {
			return &ErrInsufficient{Pool: pool, Scope: "calibration", Need: want[pool], Remaining: limit - pools[pool]}
		}
	}
	if wantUSD && cash+wantCash > c.MaxCashUSD {
		return &ErrInsufficient{Pool: "cash_usd", Scope: "calibration", Need: wantCash, Remaining: c.MaxCashUSD - cash}
	}
	return nil
}

// ---- operations ----

// Reserve atomically (under the lock) checks EVERY item and records the whole hold, or records nothing and returns
// *ErrInsufficient. markerPath may be "" only when p==nil or p.Budget==nil or it has no pools (then every pool is uncapped but
// the hold is still recorded). Validation errors (no items, empty pool/unit/kind, NaN/Inf/negative amount, Calls<0, a missing
// CalibrationCaps) are returned before touching the store.
//
// Per pool, task scope (when req.Task != ""): cap + raises vs the task's ledger spend + outstanding holds of the same Run and
// Task; run scope: the run's spend + outstanding holds of the same Run. Both must fit; a scope without a cap does not
// constrain. Calibration items also need the CalibrationCaps (calls, per-pool, and cash for Unit "usd").
// ponytail: units are not cross-checked against the profile (a cap carries none); one request never mixes units in a pool.
func Reserve(markerPath string, p *contract.Profile, req ReserveRequest) (*Hold, error) {
	if len(req.Items) == 0 {
		return nil, errors.New("reserve: no items")
	}
	need, unit, cal := map[string]float64{}, map[string]string{}, false
	for _, it := range req.Items {
		if err := checkItem(it); err != nil {
			return nil, fmt.Errorf("reserve: %w", err)
		}
		if u, seen := unit[it.Pool]; seen && u != it.Unit {
			return nil, fmt.Errorf("reserve: pool %q is requested in both %q and %q; units are never merged", it.Pool, u, it.Unit)
		}
		unit[it.Pool] = it.Unit
		need[it.Pool] += it.Amount
		cal = cal || it.Kind == "calibration"
	}
	if cal {
		if err := checkCaps(req.Calibration); err != nil {
			return nil, fmt.Errorf("reserve: %w", err)
		}
	}
	var capped *contract.Budget
	if p != nil && p.Budget != nil && len(p.Budget.Pools) > 0 {
		capped = p.Budget
	}
	if markerPath == "" && capped != nil {
		return nil, errors.New("reserve: budget pools are checked against a run, so a run marker is required")
	}
	owner := os.Getpid()
	ownerStart, _ := run.ProcStart(owner) // 0 when unknown

	var hold *Hold
	err := withStore(func(holds map[string]*Hold) (bool, error) {
		s := runState{run: req.Run, holds: holds}
		var err error
		switch {
		case markerPath != "":
			if s, err = loadRun(markerPath, holds); err != nil {
				return false, err
			}
			if req.Run != s.run {
				return false, fmt.Errorf("reserve: request is for run %q but the marker is run %q", req.Run, s.run)
			}
		case cal:
			// No marker, so no run window: take every row of the run name. That can only over-count.
			if s.rows, err = ledger.LoadStrict(""); err != nil {
				return false, fmt.Errorf("load ledger: %w", err)
			}
		}
		if req.Handover != "" {
			for _, h := range holds {
				if h.State == stReserved && h.Run == req.Run && h.Task == req.Task && h.Attempt == req.Handover {
					h.Items = slices.DeleteFunc(h.Items, func(it Item) bool { return it.Kind == "review_reserve" })
				}
			}
		}
		if capped != nil {
			if err := s.check(capped, req.Task, need); err != nil {
				return false, err
			}
		}
		if cal {
			if err := s.checkCalibration(req.Calibration, req.Items); err != nil {
				return false, err
			}
		}
		id, err := newID(holds)
		if err != nil {
			return false, err
		}
		h := &Hold{ID: id, Run: req.Run, Task: req.Task, Attempt: req.Attempt, Decision: req.Decision, Items: slices.Clone(req.Items),
			State: stReserved, CreatedAt: time.Now().UTC(), OwnerPid: owner, OwnerStart: ownerStart}
		holds[id] = h
		c := *h
		hold = &c
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return hold, nil
}

// Settle closes a hold whose work ended. measured maps pool -> amount ALREADY RECORDED in the ledger as cost rows by someone
// else. Pools of the hold missing from measured (unknown usage) are charged at the reserved bound: Settle itself appends ONE
// ledger row per such pool (Kind "cost", ChargeID holdID+":"+pool, Approx, Component probe|worker|calibration after the item
// kind; a pool holding several kinds takes "calibration" if any item is one, else the first). Unknown usage is never released as
// zero. Idempotent: settling an already settled/reconciled hold returns it unchanged. A measured amount above the bound is
// accepted (it is real). A pool that is not part of the hold is an error. If appending fails midway the hold stays reserved;
// retrying re-appends the same ChargeIDs, which spend counts once.
func Settle(holdID string, measured map[string]float64) (*Hold, error) {
	return finish(holdID, measured, stSettled, "")
}

// Reconcile is the USER-ONLY release of a hold whose owner crashed: refused unless the owner is PROVEN gone (OwnerPid > 0 and
// run.ProcStart reports run.ErrNoProc, or a start time that differs from a known OwnerStart: the pid was reused); an owner that
// is alive, or whose liveness cannot be determined, keeps the hold. Unlike Settle's measured, used (pool -> actual amount) is
// not in the ledger yet: Reconcile appends a cost row for each pool given (Approx, Notes "reconciled by user: <reason>"; zero
// appends nothing) and charges the pools it lacks at the reserved bound, as Settle does. reason is required and stored in Note;
// the state becomes "reconciled". "User only" is enforced by the CLI (run.UnderClaude), as for budget raise; this trusts its caller.
func Reconcile(holdID string, used map[string]float64, reason string) (*Hold, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("reconcile: a reason is required")
	}
	return finish(holdID, used, stReconciled, reason)
}

// finish closes a reserved hold. usage holds the pools with known usage; the rest of the hold's pools are charged at the bound.
func finish(id string, usage map[string]float64, state, reason string) (*Hold, error) {
	user := state == stReconciled
	var out *Hold
	err := withStore(func(holds map[string]*Hold) (bool, error) {
		h := holds[id]
		if h == nil {
			return false, fmt.Errorf("hold %q not found", id)
		}
		if h.State != stReserved {
			if user {
				return false, fmt.Errorf("hold %s is already %s", id, h.State)
			}
			c := *h
			out = &c
			return false, nil
		}
		if user {
			if err := ownerGone(h); err != nil {
				return false, err
			}
		}
		bound, unit, component := map[string]float64{}, map[string]string{}, map[string]string{}
		for _, it := range h.Items {
			bound[it.Pool] += it.Amount
			unit[it.Pool] = it.Unit
			if c := kindComponent[it.Kind]; component[it.Pool] == "" || c == "calibration" {
				component[it.Pool] = c
			}
		}
		for pool, v := range usage {
			if _, ok := bound[pool]; !ok {
				return false, fmt.Errorf("pool %q is not part of hold %s", pool, id)
			}
			if !okAmount(v) {
				return false, fmt.Errorf("usage %v for pool %q: want finite >= 0", v, pool)
			}
		}
		used := map[string]float64{}
		for _, pool := range slices.Sorted(maps.Keys(bound)) {
			amount, known := usage[pool]
			if !known {
				amount = bound[pool]
			}
			used[pool] = amount
			if known && (!user || amount == 0) { // measured usage is already in the ledger; a zero needs no charge
				continue
			}
			note := "charged at reserved bound: usage unknown"
			if known {
				note = "reconciled by user: " + reason
			}
			if err := ledger.Append(ledger.Row{Kind: "cost", Run: h.Run, Task: h.Task, Attempt: h.Attempt, Decision: h.Decision,
				Pool: pool, Unit: unit[pool], Amount: &amount, Component: component[pool], ChargeID: id + ":" + pool,
				Approx: true, Notes: note}); err != nil {
				return false, fmt.Errorf("record charge for pool %q: %w", pool, err)
			}
		}
		now := time.Now().UTC()
		h.State, h.SettledAt, h.Used = state, &now, used
		if user {
			h.Note = reason
		}
		c := *h
		out = &c
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ownerGone returns nil only when the owner of h is proven gone; every other answer refuses.
// ponytail: identity is pid + start time, without the boot id a run marker also keeps; after a reboot a recycled pid with the
// same start tick would read as alive, which keeps the hold (fails closed) until the user investigates.
func ownerGone(h *Hold) error {
	if h.OwnerPid <= 0 {
		return fmt.Errorf("hold %s records no owner pid, so its owner cannot be proven gone", h.ID)
	}
	start, err := run.ProcStart(h.OwnerPid)
	switch {
	case errors.Is(err, run.ErrNoProc):
		return nil
	case err != nil:
		return fmt.Errorf("hold %s: liveness of owner pid %d cannot be determined: %w", h.ID, h.OwnerPid, err)
	case h.OwnerStart != 0 && start != 0 && start != h.OwnerStart:
		return nil // the pid now belongs to another process
	}
	return fmt.Errorf("hold %s: owner pid %d is still running", h.ID, h.OwnerPid)
}

func listHolds(keep func(*Hold) bool) ([]Hold, error) {
	var out []Hold
	err := withStore(func(holds map[string]*Hold) (bool, error) {
		for _, h := range holds {
			if keep(h) {
				out = append(out, *h)
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Hold) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// Outstanding lists the holds still reserved, oldest first.
func Outstanding() ([]Hold, error) {
	return listHolds(func(h *Hold) bool { return h.State == stReserved })
}

// HoldsFor lists every hold of an attempt, oldest first.
func HoldsFor(attempt string) ([]Hold, error) {
	return listHolds(func(h *Hold) bool { return h.Attempt == attempt })
}

// Remaining reports, for each configured pool: cap - ledger spend - outstanding holds (negative when already over). For task
// scope (task != "") it is the tighter of the task and run headroom. Pools without a cap are absent from the map.
func Remaining(markerPath string, p *contract.Profile, task string) (map[string]float64, error) {
	if p == nil || p.Budget == nil || len(p.Budget.Pools) == 0 {
		return map[string]float64{}, nil
	}
	if markerPath == "" {
		return nil, errors.New("remaining: budget pools are checked against a run, so a run marker is required")
	}
	var out map[string]float64
	err := withStore(func(holds map[string]*Hold) (bool, error) {
		s, err := loadRun(markerPath, holds)
		if err != nil {
			return false, err
		}
		out = s.room(p.Budget, "")
		if task != "" {
			for pool, r := range s.room(p.Budget, task) {
				if cur, ok := out[pool]; !ok || r < cur {
					out[pool] = r
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
