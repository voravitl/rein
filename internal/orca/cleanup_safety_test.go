package orca

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupReleaseMustConfirmClosure(t *testing.T) {
	for _, state := range []string{"retained", "release_pending", "", "release_unknown"} {
		t.Run(state, func(t *testing.T) {
			safetyMock(t, func(k string) ([]byte, error) {
				if strings.HasPrefix(k, "orca orchestration worker-release") {
					return []byte(`{"result":{"state":"` + state + `"}}`), nil
				}
				t.Fatalf("mutation after unconfirmed release: %s", k)
				return nil, nil
			})
			res, err := Close(context.Background(), CloseOptions{WorktreePath: filepath.Join(controllerRepo(t, false), "gone"), DispatchID: "d", TerminalHandle: "worker"})
			if err == nil || res.ReleaseState != state {
				t.Fatalf("unconfirmed release accepted: %+v %v", res, err)
			}
		})
	}
}

func TestCleanupDirectCoordinatorRefused(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	safetyMock(t, nil)
	if _, err := Close(context.Background(), CloseOptions{WorktreePath: "/nonexistent/worker", TerminalHandle: "coord"}); err == nil {
		t.Fatal("coordinator close accepted")
	}
}

func TestCleanupTitleDoesNotProveIdle(t *testing.T) {
	safetyMock(t, func(k string) ([]byte, error) {
		if k == "orca terminal list --json" {
			return []byte(`{"result":{"terminals":[{"handle":"active","title":"~/shell","worktreePath":"/work/task"}]}}`), nil
		}
		t.Fatalf("closed terminal based only on title: %s", k)
		return nil, nil
	})
	res, err := Tidy(context.Background(), TidyOptions{WorkspacesDir: "/work", Apply: true})
	if err != nil || len(res.Closed) != 0 {
		t.Fatalf("unsafe tidy: %+v %v", res, err)
	}
}

func TestCleanupRemovalWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wt := t.TempDir()
	safetyMock(t, func(k string) ([]byte, error) {
		switch {
		case strings.HasPrefix(k, "git status"):
			return nil, nil
		case strings.HasPrefix(k, "git branch"):
			return []byte("worker"), nil
		case strings.HasPrefix(k, "git for-each-ref"):
			return []byte("refs/remotes/origin/main"), nil
		case strings.HasPrefix(k, "orca terminal list"):
			return []byte(`{"result":{"terminals":[],"truncated":false,"totalCount":0,"hostScope":{"hostIds":["local"],"omittedHostIds":[]}}}`), nil
		case strings.HasPrefix(k, "orca worktree rm"):
			cancel()
			return nil, nil
		}
		t.Fatalf("unexpected command: %s", k)
		return nil, nil
	})
	start := time.Now()
	res, err := Close(ctx, CloseOptions{WorktreePath: wt})
	if !errors.Is(err, context.Canceled) || res.WorktreeGone || time.Since(start) > time.Second {
		t.Fatalf("removal cancellation ignored: %+v %v", res, err)
	}
}

func TestCleanupAlreadyReleasedDoesNotForceClose(t *testing.T) {
	wt := filepath.Join(controllerRepo(t, false), "gone")
	safetyMock(t, func(k string) ([]byte, error) {
		switch {
		case strings.HasPrefix(k, "orca orchestration worker-release"):
			return []byte(`{"result":{"state":"already_released"}}`), nil
		case strings.HasPrefix(k, "orca terminal list"):
			return []byte(`{"result":{"terminals":[],"truncated":false,"totalCount":0,"hostScope":{"hostIds":["local"],"omittedHostIds":[]}}}`), nil
		case strings.HasPrefix(k, "orca worktree rm"):
			return nil, nil
		}
		t.Fatalf("force-close after release: %s", k)
		return nil, nil
	})
	res, err := Close(context.Background(), CloseOptions{WorktreePath: wt, DispatchID: "d", TerminalHandle: "worker"})
	if err != nil || !res.WorktreeGone || !res.TerminalClosed {
		t.Fatalf("released cleanup failed: %+v %v", res, err)
	}
}

func TestCleanupTidyGitFailure(t *testing.T) {
	safetyMock(t, func(k string) ([]byte, error) {
		if strings.HasPrefix(k, "orca terminal list") {
			return []byte(`{"result":{"terminals":[]}}`), nil
		}
		return nil, os.ErrPermission
	})
	if _, err := Tidy(context.Background(), TidyOptions{RepoDir: "/repo"}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("git inventory error ignored: %v", err)
	}
}

func TestCleanupLeftoverTerminalBlocksRemoval(t *testing.T) {
	for _, inventory := range []string{`{"result":{"terminals":[{"handle":"active"}]}}`, `{"result":{}}`, `{"result":{"terminals":[],"truncated":true}}`} {
		t.Run(inventory, func(t *testing.T) {
			safetyMock(t, func(k string) ([]byte, error) {
				if strings.HasPrefix(k, "orca terminal list") {
					return []byte(inventory), nil
				}
				t.Fatalf("unsafe removal: %s", k)
				return nil, nil
			})
			if _, err := Close(context.Background(), CloseOptions{WorktreePath: filepath.Join(controllerRepo(t, false), "gone")}); err == nil {
				t.Fatal("removed worktree without empty inventory")
			}
		})
	}
}

func TestCleanupRemovalTimeout(t *testing.T) {
	safetyMock(t, func(k string) ([]byte, error) {
		switch {
		case strings.HasPrefix(k, "git status"):
			return nil, nil
		case strings.HasPrefix(k, "git branch"):
			return []byte("worker"), nil
		case strings.HasPrefix(k, "git for-each-ref"):
			return []byte("refs/remotes/origin/main"), nil
		case strings.HasPrefix(k, "orca terminal list"):
			return []byte(`{"result":{"terminals":[],"truncated":false,"totalCount":0,"hostScope":{"hostIds":["local"],"omittedHostIds":[]}}}`), nil
		case strings.HasPrefix(k, "orca worktree rm"):
			return nil, nil
		}
		t.Fatalf("unexpected command: %s", k)
		return nil, nil
	})
	res, err := Close(context.Background(), CloseOptions{WorktreePath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "timed out") || res.WorktreeGone {
		t.Fatalf("false removal success: %+v %v", res, err)
	}
}

func TestCleanupReleaseErrorEnvelope(t *testing.T) {
	safetyMock(t, func(k string) ([]byte, error) {
		if strings.HasPrefix(k, "orca orchestration worker-release") {
			return []byte(`{"ok":false,"error":{"code":"failed"},"result":{"state":"released"}}`), nil
		}
		t.Fatalf("mutation after error envelope: %s", k)
		return nil, nil
	})
	if _, err := Close(context.Background(), CloseOptions{WorktreePath: filepath.Join(controllerRepo(t, false), "gone"), DispatchID: "d"}); err == nil {
		t.Fatal("error envelope accepted")
	}
}

func TestCleanupTidyCoordinatorHandle(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	safetyMock(t, func(k string) ([]byte, error) {
		return []byte(`{"result":{"terminals":[{"handle":"coord","title":"~/shell","worktreePath":"/work/task"}]}}`), nil
	})
	res, err := Tidy(context.Background(), TidyOptions{WorkspacesDir: "/work", Apply: true})
	if err != nil || res.Terminals[0].Category != "foreign" || len(res.Closed) != 0 {
		t.Fatalf("coordinator not protected: %+v %v", res, err)
	}
}

func TestCleanupTidyInvalidInventory(t *testing.T) {
	for _, raw := range []string{`{}`, `{"ok":false,"result":{"terminals":[]}}`, `{"error":{"code":"failed"},"result":{"terminals":[]}}`, `{"result":{"terminals":[],"truncated":true}}`} {
		t.Run(raw, func(t *testing.T) {
			safetyMock(t, func(k string) ([]byte, error) { return []byte(raw), nil })
			if _, err := Tidy(context.Background(), TidyOptions{}); err == nil {
				t.Fatal("invalid inventory reported as successful audit")
			}
		})
	}
}
