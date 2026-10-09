package ledger

// Public per-token rates with provenance. The blended `usd_per_mtok` of the legacy prices.json (loadPrices) stays as it is
// for reports; it carries no source or lifetime and is never valid for scored selection.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"time"
)

// RateTier replaces base rates, field by field, once the prompt context exceeds ContextOver tokens.
type RateTier struct {
	ContextOver       int      `json:"context_over"`
	InputPerMTok      *float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok     *float64 `json:"output_per_mtok,omitempty"`
	CacheReadPerMTok  *float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok *float64 `json:"cache_write_per_mtok,omitempty"`
}

// Rates are USD per million tokens as published by Source on AsOf, believed current until ValidUntil.
type Rates struct {
	InputPerMTok      *float64   `json:"input_per_mtok,omitempty"`
	OutputPerMTok     *float64   `json:"output_per_mtok,omitempty"`
	CacheReadPerMTok  *float64   `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok *float64   `json:"cache_write_per_mtok,omitempty"`
	Tiers             []RateTier `json:"tiers,omitempty"`
	Source            string     `json:"source,omitempty"`
	AsOf              string     `json:"as_of,omitempty"`
	ValidUntil        string     `json:"valid_until,omitempty"`
}

// Usage is the token split of one priced call.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	ContextTokens    int `json:"context_tokens"` // prompt size, selects the context tier
}

// LoadRates reads the entries of prices.json that carry the new fields. Legacy entries (usd_per_mtok only) and `_doc`-style
// keys are skipped; a missing file is (nil, nil).
func LoadRates(path string) (map[string]Rates, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]Rates{}
	for model, msg := range raw {
		if len(model) > 0 && model[0] == '_' {
			continue
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(msg, &probe) != nil {
			continue // a non-object value such as a note string
		}
		isRates := false
		for _, f := range []string{"input_per_mtok", "output_per_mtok", "cache_read_per_mtok", "cache_write_per_mtok", "tiers", "source", "as_of", "valid_until"} {
			if _, ok := probe[f]; ok {
				isRates = true
			}
		}
		if !isRates {
			continue
		}
		var r Rates
		if err := json.Unmarshal(msg, &r); err != nil {
			return nil, fmt.Errorf("%s: model %q: %w", path, model, err)
		}
		out[model] = r
	}
	return out, nil
}

func okRate(p *float64) bool { return p == nil || (!math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0) }

// Validate checks provenance and lifetime: source, as_of and valid_until present, as_of <= now <= valid_until, finite
// non-negative rates, input and output present, tiers strictly increasing. Stale rates must be refreshed, not extended.
func (r Rates) Validate(now time.Time) error {
	if r.Source == "" {
		return errors.New("rates: source missing")
	}
	asOf, err := time.Parse(time.RFC3339, r.AsOf)
	if err != nil {
		return errors.New("rates: as_of must be RFC 3339")
	}
	until, err := time.Parse(time.RFC3339, r.ValidUntil)
	if err != nil {
		return errors.New("rates: valid_until must be RFC 3339")
	}
	if asOf.After(now) {
		return errors.New("rates: as_of is in the future")
	}
	if now.After(until) {
		return fmt.Errorf("rates: expired %s (source %s, as of %s); refresh them from the source", r.ValidUntil, r.Source, r.AsOf)
	}
	if r.InputPerMTok == nil || r.OutputPerMTok == nil {
		return errors.New("rates: input and output rates required")
	}
	for _, p := range []*float64{r.InputPerMTok, r.OutputPerMTok, r.CacheReadPerMTok, r.CacheWritePerMTok} {
		if !okRate(p) {
			return errors.New("rates: rates must be finite and >= 0")
		}
	}
	last := 0
	for _, t := range r.Tiers {
		if t.ContextOver <= last {
			return errors.New("rates: tiers must have strictly increasing context_over > 0")
		}
		last = t.ContextOver
		for _, p := range []*float64{t.InputPerMTok, t.OutputPerMTok, t.CacheReadPerMTok, t.CacheWritePerMTok} {
			if !okRate(p) {
				return errors.New("rates: tier rates must be finite and >= 0")
			}
		}
	}
	return nil
}

// PriceUSD prices a call with the tier whose ContextOver is the largest below u.ContextTokens. A category with tokens but
// no rate is an error: a missing rate never prices as zero.
func (r Rates) PriceUSD(u Usage) (float64, error) {
	in, out, cr, cw := r.InputPerMTok, r.OutputPerMTok, r.CacheReadPerMTok, r.CacheWritePerMTok
	for _, t := range r.Tiers {
		if t.ContextOver < u.ContextTokens {
			in, out, cr, cw = pick(t.InputPerMTok, r.InputPerMTok), pick(t.OutputPerMTok, r.OutputPerMTok),
				pick(t.CacheReadPerMTok, r.CacheReadPerMTok), pick(t.CacheWritePerMTok, r.CacheWritePerMTok)
		}
	}
	total := 0.0
	for _, c := range []struct {
		name   string
		tokens int
		rate   *float64
	}{{"input", u.InputTokens, in}, {"output", u.OutputTokens, out}, {"cache read", u.CacheReadTokens, cr}, {"cache write", u.CacheWriteTokens, cw}} {
		if c.tokens < 0 {
			return 0, fmt.Errorf("%s tokens %d: want >= 0", c.name, c.tokens)
		}
		if c.tokens == 0 {
			continue
		}
		if c.rate == nil {
			return 0, fmt.Errorf("%s tokens used but no %s rate: not priced as zero", c.name, c.name)
		}
		total += float64(c.tokens) / 1e6 * *c.rate
	}
	return total, nil
}

func pick(tier, base *float64) *float64 {
	if tier != nil {
		return tier
	}
	return base
}

// Hash binds a decision to exact rates.
func (r Rates) Hash() string { return hashJSON(r) }

// RatesHash hashes the named entries in sorted key order; a missing key hashes as absent, so adding or removing it changes the hash.
func RatesHash(m map[string]Rates, keys []string) string {
	type entry struct {
		Key   string `json:"key"`
		Rates *Rates `json:"rates"`
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	list := make([]entry, 0, len(sorted))
	for _, k := range slices.Compact(sorted) {
		e := entry{Key: k}
		if r, ok := m[k]; ok {
			e.Rates = &r
		}
		list = append(list, e)
	}
	return hashJSON(list)
}
