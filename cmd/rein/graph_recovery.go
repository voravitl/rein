package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
)

type graphHandoffRef struct{ Path, Digest string }

func prepareGraphHandoff(control *run.Controller, p supervisor.Plan, t supervisor.Task, d supervisor.Dispatch, o supervisor.Outcome) (graphHandoffRef, error) {
	var ref graphHandoffRef
	c, err := contract.Load(t.Task)
	if err != nil {
		return ref, err
	}
	if err := graphLaunchAuthority(control, c, p.Run); err != nil {
		return ref, err
	}
	e, b, err := loadGraphExecution(p, t, d)
	if err != nil {
		return ref, err
	}
	if graphHash(b) != o.ExecutionDigest {
		return ref, errors.New("previous execution evidence changed")
	}
	if e.QuotaSignal == "" {
		return ref, errors.New("retry lacks recorded configured quota signal")
	}
	if err := routing.VerifyCheckpoint(t.Task, d.Attempt); err != nil {
		return ref, err
	}
	cp, err := routing.LoadCheckpoint(t.Task, d.Attempt)
	if err != nil {
		return ref, err
	}
	if cp.Digest != o.CheckpointDigest || cp.Run != p.Run || cp.Worktree != d.Worktree || cp.ContractHash != t.ContractHash {
		return ref, errors.New("retry checkpoint identity changed")
	}
	holds, err := budget.HoldsFor(d.Attempt)
	if err != nil {
		return ref, err
	}
	if len(holds) == 0 {
		return ref, errors.New("retry accounting evidence missing")
	}
	for _, h := range holds {
		if h.State == "reserved" || h.Run != p.Run || h.Task != t.Task {
			return ref, errors.New("retry accounting remains unsettled or mismatched")
		}
	}
	pools, err := routing.Status()
	if err != nil {
		return ref, err
	}
	cooldown, exists := pools[d.Pool]
	if !exists || cooldown.Reason == "" {
		return ref, errors.New("retry quota cooldown evidence missing")
	}
	// Keep the artifact bounded: the checkpoint holds file contents; this is a continuation receipt.
	artifact := struct {
		Run             string   `json:"run"`
		Task            string   `json:"task"`
		PreviousAttempt string   `json:"previous_attempt"`
		Checkpoint      string   `json:"checkpoint_digest"`
		Execution       string   `json:"execution_digest"`
		Progress        []string `json:"progress"`
		NextAction      string   `json:"next_action"`
	}{Run: p.Run, Task: t.Task, PreviousAttempt: d.Attempt, Checkpoint: cp.Digest, Execution: graphHash(b), NextAction: "Continue the approved task from the verified checkpoint in this worktree; preserve the captured partial work and run the approved verification commands."}
	for _, f := range cp.Files {
		if f.Status != "unmodified" {
			artifact.Progress = append(artifact.Progress, f.Path+": "+f.Status)
		}
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return ref, err
	}
	if len(raw) > 64<<10 {
		return ref, errors.New("continuation receipt exceeds 64 KiB")
	}
	ref.Path = graphEvidencePath(d.Attempt, "handoff")
	ref.Digest = graphHash(raw)
	existing, err := os.ReadFile(ref.Path)
	if err == nil {
		if !bytes.Equal(existing, raw) {
			return ref, errors.New("immutable continuation receipt changed")
		}
		return ref, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ref, err
	}
	_, err = writeGraphEvidence(ref.Path, artifact)
	return ref, err
}

// Configured provider quota signals retain the probe's case-insensitive literal semantics.
// A bounded suffix detects a signal split across writes without retaining unbounded output.
type graphQuotaWriter struct {
	mu      sync.Mutex
	capture *dispatchReceiptBuffer
	signals []string
	suffix  string
	matched chan string
	seen    bool
}

func (w *graphQuotaWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.capture.Write(p)
	if w.seen || w.capture.overflow {
		return n, err
	}
	text := w.suffix + strings.ToLower(string(p))
	keep := 0
	for _, signal := range w.signals {
		if len(signal) > keep {
			keep = len(signal)
		}
		if strings.Contains(text, signal) {
			w.seen = true
			w.matched <- signal
			break
		}
	}
	if keep > len(text) {
		keep = len(text)
	}
	if keep > 0 {
		w.suffix = text[len(text)-keep:]
	}
	return n, err
}
func graphHandoffEnv(env []string, e *graphExecution) ([]string, error) {
	out := make([]string, 0, len(env)+2)
	for _, v := range env {
		if !strings.HasPrefix(v, "REIN_HANDOFF_PATH=") && !strings.HasPrefix(v, "REIN_HANDOFF_SHA256=") {
			out = append(out, v)
		}
	}
	if e == nil || e.HandoffPath == "" {
		return out, nil
	}
	raw, err := os.ReadFile(e.HandoffPath)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 || graphHash(raw) != e.HandoffDigest {
		return nil, fmt.Errorf("continuation receipt changed before launch")
	}
	return append(out, "REIN_HANDOFF_PATH="+e.HandoffPath, "REIN_HANDOFF_SHA256="+e.HandoffDigest), nil
}
