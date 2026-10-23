package providers

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode"
)

func readHermesQuota(ctx context.Context, dir string, timeout time.Duration) QuotaSnapshot {
	s := QuotaSnapshot{Provider: "hermes", Source: "native Hermes usage --json", Scope: "Configured Hermes provider only; launch account/model/pool binding and provider data age unverified", AccountEvidence: "unverified", Status: "unsupported", Windows: []QuotaWindow{}}
	exe, err := exec.LookPath("hermes")
	if err != nil {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return collectHermesQuota(ctx, []string{exe}, dir, s)
}

func collectHermesQuota(ctx context.Context, argv []string, dir string, s QuotaSnapshot) QuotaSnapshot {
	s.Status = "unavailable"
	version, why := runOnce(ctx, append(slices.Clone(argv), "--version"), dir, true)
	if why != "" {
		return s
	}
	if versionOK(versionRx.FindString(string(version)), "0.21.5") != "" {
		s.Status = "unsupported"
		return s
	}
	raw, why := runOnce(ctx, append(slices.Clone(argv), "usage", "--json"), dir, false)
	if why != "" {
		return s
	}
	return parseHermesQuota(raw, s, time.Now().UTC())
}

func parseHermesQuota(raw []byte, s QuotaSnapshot, now time.Time) QuotaSnapshot {
	var doc struct {
		Provider    string  `json:"provider"`
		Source      string  `json:"source"`
		Fetched     string  `json:"fetched_at"`
		Unavailable *string `json:"unavailable_reason"`
		Windows     []struct {
			Label string   `json:"label"`
			Used  *float64 `json:"used_percent"`
			Reset *string  `json:"resets_at"`
		} `json:"windows"`
	}
	s.Status = "invalid"
	if json.Unmarshal(raw, &doc) != nil {
		return s
	}
	sources := map[string]string{"openai-codex": "usage_api", "anthropic": "oauth_usage_api", "openrouter": "credits_api"}
	if source, ok := sources[doc.Provider]; !ok || doc.Source != source {
		return s
	}
	stamp, err := time.Parse(time.RFC3339Nano, doc.Fetched)
	if err != nil || stamp.UnixMilli() <= 0 || stamp.After(now.Add(time.Minute)) {
		return s
	}
	if now.Sub(stamp) > 5*time.Minute {
		s.Status = "stale"
		return s
	}
	if doc.Unavailable != nil && strings.TrimSpace(*doc.Unavailable) != "" {
		s.Status = "unavailable"
		return s
	}
	s.Status = "unknown"
	if len(doc.Windows) == 0 || len(doc.Windows) > 64 {
		return s
	}
	seen := map[string]bool{}
	for _, w := range doc.Windows {
		if strings.TrimSpace(w.Label) == "" || len(w.Label) > 200 || strings.IndexFunc(w.Label, unicode.IsControl) >= 0 || seen[w.Label] || w.Used == nil || math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) || *w.Used < 0 || *w.Used > 100 {
			s.Status = "invalid"
			return s
		}
		seen[w.Label] = true
		if w.Reset == nil {
			return s
		}
		reset, err := time.Parse(time.RFC3339Nano, *w.Reset)
		if err != nil || !reset.After(now) || reset.Year() > 9999 {
			s.Status = "invalid"
			return s
		}
	}
	// Copy only measured fields after the entire response passes; account details never leave the parser.
	observed := stamp.UnixMilli()
	s.ObservedAt, s.Status = &observed, "ok"
	for _, w := range doc.Windows {
		reset, _ := time.Parse(time.RFC3339Nano, *w.Reset)
		resetMillis, remaining := reset.UnixMilli(), 100-*w.Used
		s.Windows = append(s.Windows, QuotaWindow{Name: w.Label, Remaining: &remaining, ResetsAt: &resetMillis})
	}
	return s
}
