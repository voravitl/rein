package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// Lock actual task storage and known invocation scopes together: changing cwd
// must not let a legacy CLI mutate a supervised task in another repository.
func acquireRouteMutation(c *contract.Contract, runID string, extraDirs ...string) (func(), error) {
	if c != nil && c.Worktree == "" {
		return nil, fmt.Errorf("task has no repository ownership")
	}
	dirs := append([]string{os.Getenv("ORCA_REPO_DIR"), contract.IndexDir(), os.Getenv("REIN_RUN_DIR")}, extraDirs...)
	if c != nil {
		dirs = append(dirs, c.Worktree)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dirs = append(dirs, cwd)
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		loc, found := run.Find(dir)
		if !found {
			loc, err = run.Locate(dir)
			if err != nil {
				if c != nil && dir == c.Worktree {
					release()
					return nil, fmt.Errorf("cannot resolve task repository ownership: %w", err)
				}
				continue
			}
		}
		if seen[loc.Marker] {
			continue
		}
		seen[loc.Marker] = true
		expected := runID
		if expected == "" {
			if _, e := os.Lstat(loc.Marker); e == nil {
				m, e := run.Load(loc.Marker)
				if e != nil {
					release()
					return nil, e
				}
				expected = m.Run
			} else if !errors.Is(e, os.ErrNotExist) {
				release()
				return nil, e
			}
		}
		unlock, e := run.AcquireLegacyController(loc.Top, expected)
		if e != nil {
			release()
			return nil, e
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
}

func acquireRouteHoldMutation(holds []budget.Hold) (func(), error) {
	if len(holds) == 0 {
		return nil, fmt.Errorf("no recorded reservation scope")
	}
	first := holds[0]
	if first.Task == "" || first.Run == "" {
		return nil, fmt.Errorf("reservation lacks task/run ownership")
	}
	for _, h := range holds {
		if h.Task != first.Task || h.Run != first.Run {
			return nil, fmt.Errorf("reservation attempt has conflicting task/run ownership")
		}
	}
	c, err := contract.Load(first.Task)
	if err != nil {
		return nil, err
	}
	return acquireRouteMutation(c, first.Run)
}
