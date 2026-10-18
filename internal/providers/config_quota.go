package providers

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

// QuotaWindow is a percentage of one provider window, never a currency amount.
// Remaining is nil when Orca supplies no trustworthy measurement.
type QuotaWindow struct {
	Name          string   `json:"name"`
	Remaining     *float64 `json:"remaining_percent"`
	WindowMinutes int      `json:"window_minutes,omitempty"`
	ResetsAt      *int64   `json:"resets_at_ms,omitempty"`
}
type QuotaSnapshot struct {
	AccountEvidence string        `json:"account_evidence"`
	Provider        string        `json:"provider"`
	Source          string        `json:"source"`
	Scope           string        `json:"scope"`
	Status          string        `json:"status"`
	ObservedAt      *int64        `json:"observed_at_ms"`
	Windows         []QuotaWindow `json:"windows"`
}
type orcaWindow struct {
	Name    string   `json:"name"`
	Used    *float64 `json:"usedPercent"`
	Minutes int      `json:"windowMinutes"`
	Reset   *int64   `json:"resetsAt"`
}
type orcaQuota struct {
	Status                   string  `json:"status"`
	Error                    *string `json:"error"`
	Updated                  *int64  `json:"updatedAt"`
	Session, Weekly, Monthly *orcaWindow
	Buckets                  []orcaWindow `json:"buckets"`
}

// ParseOrcaQuota consumes only account-list rateLimits; account identifiers and
// authentication material never enter the returned snapshot. Unknown free-model
// limits cannot be inferred from OpenCode Go's separate subscription limits.
func ParseOrcaQuota(raw []byte, now time.Time, maxAge time.Duration) ([]QuotaSnapshot, error) {
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			RateLimits map[string]json.RawMessage `json:"rateLimits"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errors.New("invalid Orca account-list JSON")
	}
	if !envelope.OK || envelope.Result.RateLimits == nil {
		return nil, errors.New("Orca account-list did not return rate limits")
	}
	out := []QuotaSnapshot{}
	for _, provider := range []string{"claude", "codex", "antigravity", "opencode2"} {
		s := QuotaSnapshot{Provider: provider, Source: "orca account list", Scope: "Orca runtime snapshot; launch account/model/pool binding unverified", AccountEvidence: "unverified", Status: "unknown", Windows: []QuotaWindow{}}
		rawQuota, ok := envelope.Result.RateLimits[provider]
		if !ok {
			out = append(out, s)
			continue
		}
		var q orcaQuota
		if err := json.Unmarshal(rawQuota, &q); err != nil {
			s.Status = "invalid"
			out = append(out, s)
			continue
		}
		s.ObservedAt = q.Updated
		switch {
		case q.Status != "ok" || q.Error != nil:
			s.Status = "unavailable"
		case q.Updated == nil || *q.Updated <= 0:
			s.Status = "unknown"
		case time.UnixMilli(*q.Updated).After(now.Add(time.Minute)):
			s.Status = "invalid"
		case maxAge <= 0 || now.Sub(time.UnixMilli(*q.Updated)) > maxAge:
			s.Status = "stale"
		default:
			s.Status = "ok"
		}
		add := func(name string, w *orcaWindow) {
			if w == nil {
				return
			}
			window := QuotaWindow{Name: name, WindowMinutes: w.Minutes, ResetsAt: w.Reset}
			if w.Used != nil && !math.IsNaN(*w.Used) && *w.Used >= 0 && *w.Used <= 100 && s.Status == "ok" {
				r := 100 - *w.Used
				window.Remaining = &r
			}
			s.Windows = append(s.Windows, window)
		}
		add("session", q.Session)
		add("weekly", q.Weekly)
		add("monthly", q.Monthly)
		for _, w := range q.Buckets {
			add(w.Name, &w)
		}
		if len(s.Windows) == 0 && s.Status == "ok" {
			s.Status = "unknown"
		}
		out = append(out, s)
	}
	return out, nil
}
