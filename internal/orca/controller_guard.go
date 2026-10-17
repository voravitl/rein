package orca

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/voravitl/rein/internal/run"
)

// legacyController fences all known repository scopes, including cwd, for the operation.
func legacyController(repo, runID string) (func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	envRepo := os.Getenv("ORCA_REPO_DIR")
	if envRepo != "" {
		if _, err := run.Locate(envRepo); err != nil {
			return nil, fmt.Errorf("invalid ORCA_REPO_DIR: %w", err)
		}
	}
	var releases []func()
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		})
	}
	seen := map[string]bool{}
	managed := false
	for _, candidate := range []string{repo, envRepo, cwd} {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			unlock()
			return nil, err
		}
		loc, found := run.Find(abs)
		managed = managed || found
		if !found {
			loc, err = run.Locate(abs)
			if err != nil {
				continue
			}
		}
		if seen[loc.Common] {
			continue
		}
		seen[loc.Common] = true
		identity := runID
		if identity == "" {
			identity = os.Getenv("ORCA_RUN")
		}
		if identity == "" && found {
			m, err := run.Load(loc.Marker)
			if err != nil {
				unlock()
				return nil, err
			}
			// APIs without a run argument may infer legacy identity only.
			if m.Schema == run.Schema {
				identity = m.Run
			}
		}
		release, err := run.AcquireLegacyController(loc.Top, identity)
		if err != nil {
			unlock()
			return nil, err
		}
		releases = append(releases, release)
	}
	if repo != "" {
		if _, err := os.Stat(repo); err != nil && !managed {
			unlock()
			return nil, fmt.Errorf("cannot establish repository scope for missing checkout: %w", err)
		}
	}
	// shortcut: existing non-Git checkout fixtures/standalone directories are allowed without a managed run; require a Git scope when they become managed.
	return unlock, nil
}
