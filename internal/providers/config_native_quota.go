package providers

import (
	"context"
	"errors"
	"math"
	"time"
)

const nativeCodexQuotaSource = "native Codex app-server/account/rateLimits/read"

// ReadNativeQuota reports local Codex metadata only. It does not select a model,
// authorize a worker, replace Orca quota gates, or prove the provider data's age.
// A nil config and one pinned harness prevent configured discovery argv or other
// harnesses from being launched through this display-only surface.
func ReadNativeQuota(ctx context.Context, dir string, timeout time.Duration) ([]QuotaSnapshot, error) {
	if timeout <= 0 {
		return nativeQuotaSnapshots(Inventory{}, time.Now().UTC()), errors.New("native quota timeout must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nativeQuotaSnapshots(Inventory{}, time.Now().UTC()), errors.New("native Codex quota read cancelled")
	}
	inventories := Discover(ctx, nil, DiscoverOptions{Dir: dir, Timeout: timeout, Harnesses: []string{"codex"}})
	if len(inventories) != 1 {
		return nativeQuotaSnapshots(Inventory{}, time.Now().UTC()), errors.New("native Codex inventory unavailable")
	}
	return nativeQuotaSnapshots(inventories[0], time.Now().UTC()), nil
}

// nativeQuotaSnapshots deliberately copies no inventory reason, scope, model,
// executable, credential/account identifier or raw response into display data.
func nativeQuotaSnapshots(inv Inventory, now time.Time) []QuotaSnapshot {
	out := make([]QuotaSnapshot, 0, 4)
	for _, provider := range []string{"claude", "codex", "antigravity", "opencode2"} {
		s := QuotaSnapshot{Provider: provider, Source: "native quota unsupported", Scope: "No native quota adapter queried; quota and account binding unknown", AccountEvidence: "unverified", Status: "unsupported", Windows: []QuotaWindow{}}
		if provider == "codex" {
			s.Source = nativeCodexQuotaSource
			s.Scope = "Local Codex CLI query time only; provider data age and launch account/model/pool binding unverified"
			s.Status = "unknown"
			if inv.Harness == "codex" {
				populateNativeCodexQuota(&s, inv, now)
			}
		}
		out = append(out, s)
	}
	return out
}

func populateNativeCodexQuota(s *QuotaSnapshot, inv Inventory, now time.Time) {
	if inv.Status != InvComplete {
		if inv.Status == InvFailed {
			s.Status = "unavailable"
		}
		return
	}
	if inv.QueriedAt.IsZero() {
		return
	}
	if inv.QueriedAt.After(now) || inv.QueriedAt.UnixMilli() <= 0 {
		s.Status = "invalid"
		return
	}
	stamp := inv.QueriedAt.UnixMilli()
	s.ObservedAt = &stamp
	if len(inv.Limits) == 0 {
		return
	}
	s.Status = "ok"
	seen := map[string]bool{}
	for _, limit := range inv.Limits {
		w := QuotaWindow{Name: limit.ID}
		if !validID(limit.ID) || seen[limit.ID] || limit.Unit != "percent" {
			s.Status = "invalid"
		}
		seen[limit.ID] = true
		if limit.UsedPercent == nil {
			if s.Status == "ok" {
				s.Status = "unknown"
			}
		} else if math.IsNaN(*limit.UsedPercent) || math.IsInf(*limit.UsedPercent, 0) || *limit.UsedPercent < 0 || *limit.UsedPercent > 100 {
			s.Status = "invalid"
		} else {
			remaining := 100 - *limit.UsedPercent
			w.Remaining = &remaining
		}
		if limit.WindowMinutes == nil || limit.ResetsAt == nil {
			if s.Status == "ok" {
				s.Status = "unknown"
			}
		}
		if limit.WindowMinutes != nil {
			if *limit.WindowMinutes <= 0 || uint64(*limit.WindowMinutes) > uint64(^uint(0)>>1) {
				s.Status = "invalid"
			} else {
				w.WindowMinutes = int(*limit.WindowMinutes)
			}
		}
		if limit.ResetsAt != nil {
			if limit.ResetsAt.Year() < 1970 || limit.ResetsAt.Year() > 9999 || !limit.ResetsAt.After(now) {
				s.Status = "invalid"
			} else {
				reset := limit.ResetsAt.UnixMilli()
				w.ResetsAt = &reset
			}
		}
		if limit.Allowed != nil && !*limit.Allowed && s.Status != "invalid" {
			s.Status = "unavailable"
		}
		s.Windows = append(s.Windows, w)
	}
	// A malformed, incomplete or disallowed snapshot never displays a usable
	// positive balance from another window in the same native response.
	if s.Status != "ok" {
		for i := range s.Windows {
			s.Windows[i].Remaining = nil
		}
	}
}
