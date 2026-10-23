package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
)

func TestTickSamplesBoundWorkerAndRejectsForeignRepo(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	os.MkdirAll(common, 0700)
	t.Setenv("REIN_RUN_DIR", "")
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	c := &contract.Contract{Name: "worker", Worktree: root, HooksGeneration: "current", Allow: []string{"src/**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(root, "report")}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	start, err := ProcStart(os.Getpid())
	if err != nil || start == 0 {
		t.Skip("process start identity unavailable")
	}
	lease := workerLeaseObservation{Task: c.Name, Child: os.Getpid(), ChildStart: start}
	b, _ := json.Marshal(lease)
	path := filepath.Join(contract.IndexDir(), "routes", "leases", c.Name+".json")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, b, 0600)
	os.MkdirAll(filepath.Dir(contract.SeenPath(c.Name)), 0700)
	os.WriteFile(contract.SeenPath(c.Name), []byte(time.Now().Format(time.RFC3339)+" codex pretool Bash gen=current\n"), 0600)
	workers := sampleWorkers(common)
	if w, ok := workers[c.Name]; !ok || w.Observation != "alive" || w.PID != os.Getpid() || w.LastSeen == "" {
		t.Fatalf("bound process not sampled: %+v", workers)
	}
	if workers := sampleWorkers(filepath.Join(root, "foreign")); len(workers) != 0 {
		t.Fatalf("foreign contracts sampled: %+v", workers)
	}
	lease.Dispatched = true
	b, _ = json.Marshal(lease)
	os.WriteFile(path, b, 0600)
	if w := sampleWorkers(common)[c.Name]; w.Observation != "unknown" {
		t.Fatalf("remote process guessed: %+v", w)
	}
}

func TestSeenGenerationAndNormalizedEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen")
	os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)+" claude pretool Bash gen=current\n"), 0600)
	age, busy := checkSeenLogGeneration(path, "current")
	if age > time.Minute || !busy {
		t.Fatalf("normalized event ignored: %v %v", age, busy)
	}
	_, busy = checkSeenLogGeneration(path, "other")
	if busy {
		t.Fatal("previous generation accepted")
	}
}

func TestStaleTickNeverClaimsHealthyOrDead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", dir)
	for _, observation := range []string{"alive", "gone"} {
		b, _ := json.Marshal(TickState{Timestamp: time.Now().Add(-time.Hour).Format(time.RFC3339), Workers: map[string]Worker{"task": {Observation: observation, LastSeen: time.Now().Format(time.RFC3339), CPUPercent: 5}}})
		os.WriteFile(TickPath(dir), b, 0600)
		result, err := GetTaskStatus(dir, "task")
		if err != nil || result.Status != StatusUnknown {
			t.Fatalf("stale state: %+v %v", result, err)
		}
	}
}

func TestLiveWorkerWithoutActivityEvidenceIsUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", dir)
	t.Setenv("PIPELINE_CONTRACTS", dir)
	b, _ := json.Marshal(TickState{Timestamp: time.Now().Format(time.RFC3339), Workers: map[string]Worker{"task": {Observation: "alive", CPUPercent: 0}}})
	os.WriteFile(TickPath(dir), b, 0600)
	result, err := GetTaskStatus(dir, "task")
	if err != nil || result.Status != StatusUnknown {
		t.Fatalf("missing activity became stuck: %+v %v", result, err)
	}
}
