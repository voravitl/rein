package run

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestControllerExclusiveAuthority(t *testing.T) {
	root := newRepo(t)
	hash := strings.Repeat("a", 64)
	if _, err := StartSupervised(root, "r", nil, me(), hash); err != nil {
		t.Fatal(err)
	}
	c, err := AcquireController(root, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireController(root, hash); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second supervisor: %v", err)
	}
	if _, err := AcquireLegacyController(root, "r"); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("legacy overlap: %v", err)
	}
	if _, err := Start(root, "replacement", nil, me()); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("start overlap: %v", err)
	}
	m := c.Marker()
	m.CoordinatorTools[0] = "mutated"
	if c.Marker().CoordinatorTools[0] == "mutated" {
		t.Fatal("marker snapshot aliases authority")
	}
	c.Close()
	c.Close()
	if err := c.Check(); err == nil {
		t.Fatal("closed capability accepted")
	}
	if _, err := AcquireLegacyController(root, "r"); err == nil {
		t.Fatal("legacy accepted supervised marker")
	}
	if _, err := AcquireController(root, strings.Repeat("b", 64)); err == nil {
		t.Fatal("wrong plan accepted")
	}
	c, err = AcquireController(root, hash)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestControllerMarkerChangesRevokeAuthority(t *testing.T) {
	for _, change := range []string{"session", "plan", "schema", "root", "remove", "bytes"} {
		t.Run(change, func(t *testing.T) {
			root := newRepo(t)
			hash := strings.Repeat("1", 64)
			m, err := StartSupervised(root, "r", nil, me(), hash)
			if err != nil {
				t.Fatal(err)
			}
			loc, _ := Locate(root)
			c, err := AcquireController(root, hash)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			switch change {
			case "session":
				m.SessionID = "restarted-coordinator"
			case "plan":
				m.SupervisorPlanHash = strings.Repeat("2", 64)
			case "schema":
				m.Schema = 999
			case "root":
				m.Root = t.TempDir()
			case "remove":
				if err := os.Remove(loc.Marker); err != nil {
					t.Fatal(err)
				}
			case "bytes":
				b, err := os.ReadFile(loc.Marker)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(loc.Marker, append(b, ' '), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if change != "remove" && change != "bytes" {
				if err := writeMarker(loc.Marker, m, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Check(); err == nil {
				t.Fatal("changed marker accepted")
			}
		})
	}
}

func TestControllerSurvivesCoordinatorDeath(t *testing.T) {
	root := newRepo(t)
	hash := strings.Repeat("f", 64)
	m, err := StartSupervised(root, "r", nil, me(), hash)
	if err != nil {
		t.Fatal(err)
	}
	// A frozen marker with a gone coordinator is still authorized for supervision.
	m.PID, m.StartTime = 1<<30, 1
	loc, _ := Locate(root)
	if err := writeMarker(loc.Marker, m, false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(loc.Marker)
	c, err := AcquireController(root, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(loc.Marker)
	if string(before) != string(after) {
		t.Fatal("supervisor rebound coordinator marker")
	}
}

func TestControllerLegacyAndSchemaValidation(t *testing.T) {
	root := newRepo(t)
	u, err := AcquireLegacyController(root, "r")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := AcquireLegacyController(root, "r")
	if err != nil {
		t.Fatalf("second legacy holder refused: %v", err)
	}
	if _, err := AcquireController(root, strings.Repeat("a", 64)); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("supervisor overlapped legacy holders: %v", err)
	}
	u2()
	if _, err := StartSupervised(root, "r", nil, me(), strings.Repeat("a", 64)); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("start raced legacy: %v", err)
	}
	u()
	if _, err := Start(root, "r", nil, me()); err != nil {
		t.Fatal(err)
	}
	loc, _ := Locate(root)
	before, _ := os.ReadFile(loc.Marker)
	if _, err := StartSupervised(root, "new", nil, me(), strings.Repeat("a", 64)); err == nil {
		t.Fatal("adopted legacy marker")
	}
	after, _ := os.ReadFile(loc.Marker)
	if string(before) != string(after) {
		t.Fatal("existing marker overwritten")
	}
	if _, err := AcquireLegacyController(root, "wrong"); err == nil {
		t.Fatal("wrong run accepted")
	}
	u, err = AcquireLegacyController(root, "r")
	if err != nil {
		t.Fatal(err)
	}
	u()
	if _, err := AcquireController(root, strings.Repeat("a", 64)); err == nil {
		t.Fatal("supervisor accepted legacy marker")
	}
	for _, schema := range []int{SchemaSupervised, 99} {
		m, _ := Load(loc.Marker)
		m.Schema, m.SupervisorPlanHash = schema, "invalid"
		if err := writeMarker(loc.Marker, m, false); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(loc.Marker); err == nil {
			t.Fatal("invalid marker accepted")
		}
		if err := os.WriteFile(loc.Marker, before, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControllerBlocksMarkerMutation(t *testing.T) {
	root := newRepo(t)
	hash := strings.Repeat("a", 64)
	if _, err := StartSupervised(root, "r", nil, me(), hash); err != nil {
		t.Fatal(err)
	}
	loc, _ := Locate(root)
	before, _ := os.ReadFile(loc.Marker)
	c, err := AcquireController(root, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	checks := map[string]func() error{
		"resume":      func() error { _, err := Resume(root, "new-session", os.Getpid()); return err },
		"allow":       func() error { _, err := Allow(root, "task", "approved"); return err },
		"set-run-dir": func() error { return SetRunDir(loc.Marker, "/new-run-dir") },
		"end":         func() error { _, err := End(root, false, "", AuditOptions{}); return err },
		"abandon":     func() error { _, err := End(root, true, "approved", AuditOptions{}); return err },
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, ErrLockBusy) {
			t.Errorf("%s: %v", name, err)
		}
	}
	after, _ := os.ReadFile(loc.Marker)
	if string(before) != string(after) {
		t.Fatal("mutation changed active marker")
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := Resume(root, "new-session", os.Getpid()); err == nil {
		t.Fatal("supervised coordinator rebound after release")
	}
	if err := SetRunDir(loc.Marker, "/new-run-dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := Allow(root, "task", "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := End(root, true, "approved", AuditOptions{}); err != nil {
		t.Fatal(err)
	}
}
