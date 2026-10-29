package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/voravitl/rein/internal/run"
)

func TestRunSupervisedOptInAndLegacyCheck(t *testing.T) {
	fixtureOwnerTerminal(t, true)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_PID", "")
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")
	plan := filepath.Join(t.TempDir(), "plan.json")
	b := []byte(fmt.Sprintf(`{"schema":1,"run":"r","repo":%q,"terminal":"coord","source":"orca","version":"1","max_checks":1,"timeout_ms":1000,"poll_interval_ms":1,"tasks":[{"task":"t","attempt":"a","current_attempt":"a","orca_task":"ot","dispatch":"d"}]}`+"\n", root))
	if err := os.WriteFile(plan, b, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"start", root, "--run", "r", "--session", "s", "--pid", strconv.Itoa(os.Getpid()), "--supervisor-plan", plan}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "coordinator")
	if code := cmdRun(args); code != 2 {
		t.Fatalf("coordinator self-authorized: %d", code)
	}
	loc, _ := run.Locate(root)
	if _, err := os.Stat(loc.Marker); !os.IsNotExist(err) {
		t.Fatalf("refusal wrote marker: %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	for _, bad := range []string{"null", "[]", "{}", "broken"} {
		if err := os.WriteFile(plan, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := cmdRun(args); code != 2 {
			t.Fatalf("bad plan %q: %d", bad, code)
		}
	}
	if err := os.WriteFile(plan, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRun(args); code != 0 {
		t.Fatalf("start: %d", code)
	}
	m, err := run.Load(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != run.SchemaSupervised || m.SupervisorPlanHash != fmt.Sprintf("%x", sha256.Sum256(b)) {
		t.Fatalf("marker: %+v", m)
	}
	if code := cmdRun([]string{"controller-check", root, "--run", "r"}); code != 2 {
		t.Fatalf("legacy check authorized supervised: %d", code)
	}
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	if code := cmdRun([]string{"controller-check", root, "--run", "r"}); code != 0 {
		t.Fatalf("no-marker legacy compatibility: %d", code)
	}
}
