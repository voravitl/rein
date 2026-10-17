package hunter

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRegistryConcurrentUpdates(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _, err := Report(testFinding("t", "file", i+1)); errs <- err }(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	all, err := List()
	if err != nil || len(all) != n {
		t.Fatalf("list: %d, %v", len(all), err)
	}
	ids := map[string]bool{}
	for _, f := range all {
		if ids[f.ID] {
			t.Fatalf("duplicate ID %s", f.ID)
		}
		ids[f.ID] = true
	}
	id := all[0].ID
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- RecordAttempt(id, ReproAttempt{Command: fmt.Sprintf("test %d", i), Cwd: "/tmp", Outcome: "not-reproduced"})
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	f, err := Get(id)
	if err != nil || len(f.Attempts) != n {
		t.Fatalf("lost attempts: %d, %v", len(f.Attempts), err)
	}
}

func TestCorruptRegistryRejectsMutation(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id, _, err := Report(testFinding("t", "file", 1))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(registryPath(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("broken\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(registryPath())
	if _, _, err := Report(testFinding("t", "file", 2)); err == nil {
		t.Fatal("report accepted corrupt registry")
	}
	if err := RecordAttempt(id, ReproAttempt{Command: "test", Cwd: "/tmp", Outcome: "not-reproduced"}); err == nil {
		t.Fatal("attempt accepted corrupt registry")
	}
	if err := Move(id, Blocked, ""); err == nil {
		t.Fatal("move accepted corrupt registry")
	}
	if err := VerifyFix(id, "fixed", "fixed", "proof"); err == nil {
		t.Fatal("verification accepted corrupt registry")
	}
	after, _ := os.ReadFile(registryPath())
	if string(before) != string(after) {
		t.Fatal("corrupt registry overwritten")
	}
}

func TestCorruptRegistryIdentityAndIncompleteTail(t *testing.T) {
	for _, contents := range []string{"{}\n", "{}"} {
		t.Run(contents, func(t *testing.T) {
			t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
			os.MkdirAll(filepath.Dir(registryPath()), 0700)
			os.WriteFile(registryPath(), []byte(contents), 0600)
			if _, _, err := Report(testFinding("t", "file", 1)); err == nil {
				t.Fatal("corrupt journal accepted")
			}
			got, _ := os.ReadFile(registryPath())
			if string(got) != contents {
				t.Fatal("corrupt journal changed")
			}
		})
	}
}

func TestReportRejectsForgedRuntimeStrengthAndFingerprint(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	f := testFinding("t", "file", 1)
	f.Strength = ReproducedMulti
	if _, _, err := Report(f); err == nil {
		t.Fatal("runtime strength self-certified")
	}
	f.Strength = StaticSupported
	f.Fingerprint = "forged"
	if _, _, err := Report(f); err == nil {
		t.Fatal("fingerprint self-certified")
	}
}
