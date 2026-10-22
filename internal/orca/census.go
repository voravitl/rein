// census.go — same-host, opt-in process-tree verifier for supervised Orca launches.
// Orca (<= 1.4.224) reports no durable verdict about how a worker process tree ended, so a verdict --sha
// approval could otherwise rest on the CLI's word alone. The census answers with OS ground truth instead:
// it pins every reachable process as (pid, start-marker, ppid) while the tree is up and later proves, on the
// same boot, that none of those pinned identities exists any more. Pid reuse cannot forge a "gone" row,
// because the start marker must match too. A setsid/daemonizing descendant that re-parents itself outside the
// sampled tree is NOT covered — by construction, not by defect; see the documented residual in
// docs/ARCHITECTURE_REIN_ORCA_OMC.th.md §2 and TestCensusResidualSetsidDescendantIsNotCovered.
package orca

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// censusRow is one OS process-table row pinned at sample time. State is the single first state letter
// ("Z" = zombie); Start is the platform start marker named by the snapshot's Marker field.
type censusRow struct {
	PID   int    `json:"pid"`
	PPID  int    `json:"ppid"`
	Start int64  `json:"start"`
	State string `json:"state"`
}

// CensusSnapshot is the saved census. Verifiers refuse it unless Hostname and Boot match the running host.
type CensusSnapshot struct {
	Hostname   string      `json:"hostname"`
	Boot       string      `json:"boot"`   // boot identity; empty where the platform offers none
	Marker     string      `json:"marker"` // unit of Start: "ticks_since_boot" (linux) or "usec_since_epoch" (darwin)
	RecordedAt time.Time   `json:"recorded_at"`
	Root       censusRow   `json:"root"`
	Nodes      []censusRow `json:"nodes"`
}

// CensusFinding reports what became of one pinned identity: Status "gone" (absent, or the slot was
// recycled to a different start marker) or "present" (alive, or the same identity still listed as a zombie).
type CensusFinding struct {
	Identity string `json:"identity"` // "root" or "pid <n>"
	Status   string `json:"status"`
	Detail   string `json:"detail"` // absent | pid_recycled | alive | zombie_pending_reap
}

// ErrCensusUnsupported means this platform cannot sample the process table here.
var ErrCensusUnsupported = errors.New("census unsupported on this platform")

// ErrCensusForeignSnapshot means the snapshot names a different host or boot than the running one.
var ErrCensusForeignSnapshot = errors.New("census snapshot is from another host or boot")

// censusScanTable lets tests freeze the process table; production resolves to the platform scan.
var censusScanTable = censusScan

// CensusRecord pins root's identity plus every descendant reachable through ppid, from one OS scan.
// A freshly exec'd root can be missed by the first pass under load, so the scan retries before failing —
// but every retried sample rebuilds the whole tree from one table, never stitching scans together.
func CensusRecord(root int) (*CensusSnapshot, error) {
	var last []censusRow
	for i := 0; ; i++ {
		rows, err := censusScanTable()
		if err != nil {
			return nil, err
		}
		snap, rootFound := assembleCensus(root, rows)
		if rootFound {
			return snap, nil
		}
		last = rows
		if i == 2 {
			head := []int{}
			for _, r := range last {
				head = append(head, r.PID)
				if len(head) == 14 {
					break
				}
			}
			return nil, fmt.Errorf("root pid %d not found in process table at record time (rows=%d head=%v)", root, len(last), head)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func assembleCensus(root int, rows []censusRow) (*CensusSnapshot, bool) {
	children := make(map[int][]censusRow, len(rows))
	var rootRow *censusRow
	for _, r := range rows {
		if r.PID == root {
			row := r
			rootRow = &row
		}
		children[r.PPID] = append(children[r.PPID], r)
	}
	if rootRow == nil {
		return nil, false
	}
	nodes := []censusRow{}
	seen := map[int]bool{root: true}
	queue := append([]censusRow{}, children[root]...)
	for len(queue) > 0 { // breadth-first within the one scan; a row becomes a node only once
		row := queue[0]
		queue = queue[1:]
		if seen[row.PID] {
			continue
		}
		seen[row.PID] = true
		nodes = append(nodes, row)
		queue = append(queue, children[row.PID]...)
	}
	return &CensusSnapshot{
		Hostname:   censusHostname(),
		Boot:       censusBoot(),
		Marker:     censusMarker(),
		RecordedAt: time.Now().UTC(),
		Root:       *rootRow,
		Nodes:      nodes,
	}, true
}

// CensusVerify re-reads the process table on this running host and judges every pinned identity.
// A zombie on the pinned identity still counts "present" — stricter than "gone"; the caller waits for reap.
func CensusVerify(snap *CensusSnapshot) (allGone bool, findings []CensusFinding, err error) {
	if snap == nil {
		return false, nil, errors.New("nil census snapshot")
	}
	if snap.Hostname != censusHostname() || snap.Boot != censusBoot() {
		return false, nil, ErrCensusForeignSnapshot
	}
	rows, err := censusScanTable()
	if err != nil {
		return false, nil, err
	}
	byPid := make(map[int]censusRow, len(rows))
	for _, r := range rows {
		byPid[r.PID] = r
	}
	judge := func(isRoot bool, row censusRow) CensusFinding {
		id := fmt.Sprintf("pid %d", row.PID)
		if isRoot {
			id = "root"
		}
		cur, present := byPid[row.PID]
		if !present {
			return CensusFinding{Identity: id, Status: "gone", Detail: "absent"}
		}
		if cur.Start != row.Start {
			return CensusFinding{Identity: id, Status: "gone", Detail: "pid_recycled"}
		}
		if cur.State == "Z" {
			return CensusFinding{Identity: id, Status: "present", Detail: "zombie_pending_reap"}
		}
		return CensusFinding{Identity: id, Status: "present", Detail: "alive"}
	}
	findings = append(findings, judge(true, snap.Root))
	for _, node := range snap.Nodes { // a ppid tree cannot make a node share the root pid
		findings = append(findings, judge(false, node))
	}
	for _, f := range findings {
		if f.Status != "gone" {
			return false, findings, nil
		}
	}
	return true, findings, nil
}

// censusHostname is the portable os.Hostname reading; snapshots with a different name refuse verification.
func censusHostname() string { h, _ := os.Hostname(); return h }

// SaveCensus writes the snapshot as JSON evidence, LoadCensus reads it back. No secrets inside, so 0644.
func SaveCensus(snap *CensusSnapshot, path string) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func LoadCensus(path string) (*CensusSnapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap CensusSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("parse census snapshot: %w", err)
	}
	return &snap, nil
}
