package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ReadOrcaQuota reads the IDE's snapshot without a model prompt. No raw account
// records or stderr (which may include private environment details) are returned.
func ReadOrcaQuota() ([]QuotaSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "orca", "account", "list", "--json")
	var out quotaOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, errors.New("Orca quota lookup failed; no model probe attempted")
	}
	snapshots, err := ParseOrcaQuota(out.b, time.Now(), 5*time.Minute)
	if err != nil {
		return nil, err
	}
	for i := range snapshots {
		if snapshots[i].Provider == "codex" {
			snapshots[i].AccountEvidence = codexAccountMetadataEvidence(out.b)
		}
	}
	return snapshots, nil
}

type quotaOutput struct{ b []byte }

func (w *quotaOutput) Write(p []byte) (int, error) {
	if len(w.b)+len(p) > 1<<20 {
		return 0, errors.New("Orca quota response exceeds limit")
	}
	w.b = append(w.b, p...)
	return len(p), nil
}

// CheckQuota is a conservative veto, not a spending or account authorization.
// The owner explicitly maps each model to exact windows; labels are never guessed.
// A positive reading cannot replace budget reservations or launch-account proof.
func (c *Config) CheckQuota(name string) error {
	if !c.RequireOrcaQuota {
		return nil
	}
	if os.Getenv("ORCA_ENVIRONMENT") != "" || os.Getenv("ORCA_PAIRING_CODE") != "" {
		return errors.New("quota precheck: remote Orca scope is not bound to the launch account")
	}
	p, ok := c.Providers[name]
	if !ok {
		return errors.New("quota precheck: unknown provider")
	}
	if err := validateQuotaMapping(p); err != nil {
		return err
	}
	snapshots, err := ReadOrcaQuota()
	if err != nil {
		return fmt.Errorf("quota precheck: %w", err)
	}
	return checkQuotaWindows(p, snapshots, time.Now())
}
func validateQuotaMapping(p Provider) error {
	if strings.TrimSpace(p.Model) == "" || len(p.QuotaWindows) == 0 {
		return errors.New("quota precheck: configure the model's exact quota_windows before calling")
	}
	if math.IsNaN(p.QuotaReservePercent) || math.IsInf(p.QuotaReservePercent, 0) || p.QuotaReservePercent < 0 || p.QuotaReservePercent >= 100 {
		return errors.New("quota precheck: invalid reserve percentage")
	}
	seen := map[string]bool{}
	for _, name := range p.QuotaWindows {
		if strings.TrimSpace(name) == "" || seen[name] {
			return errors.New("quota precheck: empty or duplicate window mapping")
		}
		seen[name] = true
	}
	return nil
}
func checkQuotaWindows(p Provider, snapshots []QuotaSnapshot, now time.Time) error {
	if err := validateQuotaMapping(p); err != nil {
		return err
	}
	provider := p.Agent
	if provider == "agy" {
		provider = "antigravity"
	}
	matches := []QuotaSnapshot{}
	for _, s := range snapshots {
		if s.Provider == provider {
			matches = append(matches, s)
		}
	}
	if len(matches) != 1 {
		return errors.New("quota precheck: provider quota unknown or ambiguous")
	}
	s := matches[0]
	if s.AccountEvidence == "credential_metadata_mismatch" {
		return errors.New("quota precheck: Orca and local Codex default credential metadata differ")
	}
	if s.Status != "ok" || s.ObservedAt == nil || *s.ObservedAt <= 0 || now.Sub(time.UnixMilli(*s.ObservedAt)) > 5*time.Minute || time.UnixMilli(*s.ObservedAt).After(now.Add(time.Minute)) {
		return errors.New("quota precheck: quota unavailable, invalid or stale")
	}
	windows := append([]string(nil), p.QuotaWindows...)
	// Claude/Codex's shared windows all constrain the same account. A full
	// session cannot hide an exhausted weekly limit by omitting it from mapping.
	if provider == "claude" || provider == "codex" {
		for _, w := range s.Windows {
			found := false
			for _, name := range windows {
				if name == w.Name {
					found = true
				}
			}
			if !found {
				windows = append(windows, w.Name)
			}
		}
	}
	for _, name := range windows {
		count := 0
		for _, w := range s.Windows {
			if w.Name != name {
				continue
			}
			count++
			if w.Remaining == nil || math.IsNaN(*w.Remaining) || math.IsInf(*w.Remaining, 0) || *w.Remaining <= p.QuotaReservePercent || *w.Remaining > 100 {
				return fmt.Errorf("quota precheck: window %q exhausted, unknown or below reserve", name)
			}
			if w.ResetsAt != nil && *w.ResetsAt <= now.UnixMilli() {
				return fmt.Errorf("quota precheck: window %q needs refresh after reset", name)
			}
		}
		if count != 1 {
			return fmt.Errorf("quota precheck: window %q missing or ambiguous", name)
		}
	}
	return nil
}
