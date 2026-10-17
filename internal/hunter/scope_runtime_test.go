package hunter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReproduceRejectsOutOfScopeWrites(t *testing.T) {
	for _, command := range []string{"printf changed > baseline.txt; exit 7", "printf new > application.go; exit 7", "chmod +x baseline.txt; exit 7", "printf changed > .git/config; exit 7", "mkdir -p hunter-tests; ln -s ../baseline.txt hunter-tests/alias; exit 7", `chmod u+w "$PIPELINE_CONTRACTS/$REIN_TASK.json"; printf tampered > "$PIPELINE_CONTRACTS/$REIN_TASK.json"; exit 7`, "mkdir -p hunter-tests/.git; printf changed > hunter-tests/.git/config; exit 7"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
			repo, sha := hunterRepo(t)
			f := testFinding("scope", "baseline.txt", 1)
			f.Baseline = sha
			id, _, err := Report(f)
			if err != nil {
				t.Fatal(err)
			}
			a, err := Reproduce(context.Background(), id, repo, "scope", 5*time.Second, 7, []string{"sh", "-c", command})
			if err != nil {
				t.Fatal(err)
			}
			if a.Outcome != "scope-violation" {
				t.Fatalf("out of scope reproduction accepted: %+v", a)
			}
			data, err := os.ReadFile(a.EvidencePath)
			if err != nil {
				t.Fatal(err)
			}
			var execution Execution
			if err := json.Unmarshal(data, &execution); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(execution.Error, "scope") {
				t.Fatalf("missing explanation: %+v", execution)
			}
			current, _ := os.ReadFile(filepath.Join(repo, "baseline.txt"))
			if string(current) != "committed baseline\n" {
				t.Fatal("source repository changed")
			}
		})
	}
}
