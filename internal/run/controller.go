package run

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var planHashRx = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Controller is an in-process capability backed by a lifetime exclusive lock.
// Its authority expires if any byte of the run marker changes.
type Controller struct {
	mu     sync.Mutex
	unlock func()
	loc    Loc
	marker Marker
	digest [32]byte
	pid    int
	start  int64
}

func controllerMarker(loc Loc) (*Marker, [32]byte, error) {
	b, err := os.ReadFile(loc.Marker)
	if err != nil {
		return nil, [32]byte{}, err
	}
	m, err := decodeMarker(loc.Marker, b)
	if err != nil {
		return nil, [32]byte{}, err
	}
	common, _, ok := repoAt(m.Root)
	if !ok || !filepath.IsAbs(m.Root) || real(common) != real(loc.Common) || !loc.Belongs(m) {
		return nil, [32]byte{}, errors.New("run marker belongs to another repository")
	}
	return m, sha256.Sum256(b), nil
}

// AcquireController authorizes one supervisor for the exact run and plan.
// Coordinator liveness is deliberately independent of supervisor authority.
func AcquireController(repo, planHash string) (*Controller, error) {
	if !planHashRx.MatchString(planHash) {
		return nil, errors.New("supervisor plan hash must be 64 hex characters")
	}
	loc, err := Locate(repo)
	if err != nil {
		return nil, err
	}
	unlock, err := LockFile(loc.Marker+".controller.lock", 0)
	if err != nil {
		return nil, err
	}
	m, digest, err := controllerMarker(loc)
	if err == nil && (m.Schema != SchemaSupervised || m.SupervisorPlanHash != planHash) {
		err = errors.New("run does not authorize this supervisor plan")
	}
	if err != nil {
		unlock()
		return nil, err
	}
	pid := os.Getpid()
	start, err := ProcStart(pid)
	if err != nil {
		unlock()
		return nil, err
	}
	return &Controller{unlock: unlock, loc: loc, marker: *m, digest: digest, pid: pid, start: start}, nil
}

// Check revalidates the capability immediately before each controller operation.
func (c *Controller) Check() error {
	if c == nil {
		return errors.New("controller capability is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unlock == nil || c.pid != os.Getpid() {
		return errors.New("controller capability is closed or belongs to another process")
	}
	start, err := ProcStart(c.pid)
	if err != nil || start != c.start {
		return errors.New("controller process identity changed")
	}
	_, digest, err := controllerMarker(c.loc)
	if err != nil {
		return err
	}
	if digest != c.digest {
		return errors.New("run marker changed; controller authority revoked")
	}
	return nil
}

// Marker returns a detached snapshot, never a writable reference to authority.
func (c *Controller) Marker() Marker {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.marker
	m.CoordinatorWritable = append([]string(nil), m.CoordinatorWritable...)
	m.CoordinatorTools = append([]string(nil), m.CoordinatorTools...)
	m.ReadonlyAgents = append([]string(nil), m.ReadonlyAgents...)
	m.Allowed = append([]Allowance(nil), m.Allowed...)
	return m
}

// Close releases authority; repeated calls are harmless.
func (c *Controller) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unlock != nil {
		c.unlock()
		c.unlock = nil
	}
}

// AcquireLegacyController shares legacy authority while excluding a supervisor.
// No marker preserves legacy operation; an existing marker must match runID.
func AcquireLegacyController(repo, runID string) (func(), error) {
	loc, err := Locate(repo)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(loc.Marker+".controller.lock", 0, true)
	if err != nil {
		return nil, err
	}
	m, _, err := controllerMarker(loc)
	if errors.Is(err, os.ErrNotExist) {
		return unlock, nil
	}
	if err == nil && (m.Schema != Schema || m.Run != runID) {
		err = fmt.Errorf("legacy controller cannot operate run %q (schema %d)", m.Run, m.Schema)
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
