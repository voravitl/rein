package main

// Strict, attributable evidence for automatic selection: charges in one native unit of one pool, and reviewer calibration on
// frozen fixtures. Both are append-only ledger rows; nothing here estimates or merges units.
//
//	rein ledger charge  --attempt A --pool P (--unit U --amount X | --price-key K --in N --out N ...) [--component worker] ...
//	rein ledger fixture --provider P --fixture ID --class defect|clean (--detected|--missed|--false-positive|--clean-ok) --adjudicated --type kind

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
)

func cmdLedgerCharge(args []string) int {
	fs := flag.NewFlagSet("ledger charge", flag.ContinueOnError)
	attempt := fs.String("attempt", "", "attempt ID (the root decision from `rein route auto`) this charge belongs to")
	component := fs.String("component", "worker", strings.Join(ledger.Components, "|"))
	pool := fs.String("pool", "", "provider/account/pool/window qualifier: the key used in budget.pools and billing.pool")
	unit := fs.String("unit", "", strings.Join(ledger.Units, "|"))
	id := fs.String("charge-id", "", "idempotency key: the same charge recorded twice counts once")
	provider := fs.String("provider", "", "provider (an entry of fallback-chain.json) that incurred it; a review charge must name the reviewer provider")
	model := fs.String("model", "", "model that incurred it (a review charge names the reviewer model)")
	cfgPath := fs.String("config", "", "fallback-chain.json (checks the pool's unit against the provider billing)")
	runID := fs.String("run", "", "run (default: the attempt's launch record)")
	var amount *float64
	fs.Var(optFloat{&amount}, "amount", "amount in --unit")
	priceKey := fs.String("price-key", "", "price an API call from prices.json instead of --amount (needs --in and --out)")
	in, out := fs.Int("in", 0, "input tokens"), fs.Int("out", 0, "output tokens")
	cacheRead, cacheWrite := fs.Int("cache-read", 0, "cache read tokens"), fs.Int("cache-write", 0, "cache write tokens")
	context := fs.Int("context", 0, "prompt size in tokens (selects the context tier)")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *attempt == "" || *pool == "" {
		fmt.Fprintln(os.Stderr, "[ledger] charge requires --attempt A --pool P and --unit/--amount or --price-key/--in/--out")
		return 2
	}
	note := ""
	if *priceKey != "" {
		if amount != nil || (*unit != "" && *unit != "usd") {
			fmt.Fprintln(os.Stderr, "[ledger] --price-key prices a call in usd: leave --amount out and --unit usd")
			return 2
		}
		rates, err := ledger.LoadRates(ledger.PricesPath())
		r, ok := rates[*priceKey]
		if err == nil && !ok {
			err = fmt.Errorf("no rates for %q in %s (legacy usd_per_mtok entries carry no provenance and are not used)", *priceKey, ledger.PricesPath())
		}
		if err == nil {
			err = r.Validate(time.Now()) // stale rates are refreshed from their source, never extended
		}
		var usd float64
		if err == nil {
			usd, err = r.PriceUSD(ledger.Usage{InputTokens: *in, OutputTokens: *out, CacheReadTokens: *cacheRead, CacheWriteTokens: *cacheWrite, ContextTokens: *context})
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "[ledger]", err)
			return 2
		}
		amount, *unit = &usd, "usd"
		note = fmt.Sprintf("priced from %s as of %s (rates %s)", r.Source, r.AsOf, r.Hash()[:12])
	}
	if amount == nil {
		fmt.Fprintln(os.Stderr, "[ledger] --amount (or --price-key) is required")
		return 2
	}
	if *component == "review" && (*provider == "" || *model == "") {
		fmt.Fprintln(os.Stderr, "[ledger] a review charge needs --provider (the reviewer's name in fallback-chain.json) and --model: without them it prices no reviewer")
		return 2
	}
	// ponytail: the pool's unit is checked against the provider billing only when that configuration loads; budget caps carry no unit
	if cfg, err := providers.Load(providers.ConfigPath(*cfgPath)); err == nil {
		for name, p := range cfg.Providers {
			if b := p.Billing; b != nil && b.Pool == *pool && b.Unit != *unit {
				fmt.Fprintf(os.Stderr, "[ledger] pool %q is billed in %s (provider %s), not %s: units are never merged\n", *pool, b.Unit, name, *unit)
				return 2
			}
		}
	}
	row, err := ledger.NewChargeRow(*attempt, *id, *component, *pool, *unit, *provider, *model, *amount)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[ledger]", err)
		return 2
	}
	row.Notes = note
	rows, err := ledger.LoadStrict("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "[ledger]", err)
		return 2
	}
	if launch, ok := ledger.LaunchRow(rows, *attempt); ok {
		if *runID != "" && *runID != launch.Run {
			fmt.Fprintln(os.Stderr, "[ledger] --run contradicts the attempt's launch run")
			return 2
		}
		row.Run, row.Task, row.Decision = launch.Run, launch.Task, launch.Decision
		if *component == "review" {
			if launch.Provider != *provider || launch.Worker == nil || launch.Worker.Model != *model || launch.Role != "review:auto" || launch.Config == "" {
				fmt.Fprintln(os.Stderr, "[ledger] review charge must match a known reviewer launch and its configuration")
				return 2
			}
			row.Effort, row.Config = launch.Effort, launch.Config
		}
	} else {
		if *component == "review" {
			if *amount <= 0 {
				fmt.Fprintln(os.Stderr, "[ledger] a calibration review charge must be measured and greater than zero")
				return 2
			}
			holds, err := budget.HoldsFor(*attempt)
			if err != nil {
				fmt.Fprintln(os.Stderr, "[ledger]", err)
				return 2
			}
			matched := false
			for _, h := range holds {
				if h.State != "reserved" || (*runID != "" && *runID != h.Run) {
					continue
				}
				for _, it := range h.Items {
					if it.Kind != "calibration" || it.Provider != *provider || it.Model != *model || it.Pool != *pool || it.Unit != *unit || *amount > it.Amount || it.Config == "" {
						continue
					}
					if matched {
						fmt.Fprintln(os.Stderr, "[ledger] calibration attempt has ambiguous reviewer reservations")
						return 2
					}
					matched = true
					row.Run, row.Task, row.Effort, row.Config = h.Run, h.Task, it.Effort, it.Config
				}
			}
			if !matched {
				fmt.Fprintln(os.Stderr, "[ledger] review charge needs a matching outstanding reviewer calibration hold")
				return 2
			}
			row.ChargeID = "calibration-review:" + *attempt
			row.Source = "measured"
			row.Notes = note
			return save(row)
		}
		row.Run = *runID
	}
	if row.Run == "" { // spend is counted per run: an unattributed charge would never reach a cap
		fmt.Fprintln(os.Stderr, "[ledger] --run is required when the attempt has no launch record")
		return 2
	}
	return save(row)
}

func cmdLedgerFixture(args []string) int {
	fs := flag.NewFlagSet("ledger fixture", flag.ContinueOnError)
	provider := fs.String("provider", "", "reviewer provider name in fallback-chain.json")
	cfgPath := fs.String("config", "", "fallback-chain.json")
	fixture := fs.String("fixture", "", "frozen fixture ID: judging the same fixture again replaces the earlier row")
	class := fs.String("class", "", "defect (a known realistic defect) | clean")
	detected := fs.Bool("detected", false, "defect fixture: the reviewer reported the known defect")
	missed := fs.Bool("missed", false, "defect fixture: it did not")
	falsePos := fs.Bool("false-positive", false, "clean fixture: a reported finding was adjudicated false")
	cleanOK := fs.Bool("clean-ok", false, "clean fixture: no false finding")
	adjudicated := fs.Bool("adjudicated", false, "a human or an objective check settled the outcome (unadjudicated rows count for nothing)")
	kind := fs.String("type", "", "task kind: "+strings.Join(ledger.Types, "|"))
	tier := fs.String("tier", "T1", "risk tier the fixture represents: T1|T2|T3")
	suite := fs.String("suite", "", "evaluation-suite version (default: the owner's selection.evaluation_suite)")
	resolved := fs.String("resolved-model", "", "exact model that answered, when it differs from the provider's")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *provider == "" || *fixture == "" || !oneOf(*kind, ledger.Types) || *kind == "review" || !oneOf(*tier, []string{"T1", "T2", "T3"}) {
		fmt.Fprintln(os.Stderr, "[ledger] fixture requires --provider P --fixture ID --class defect|clean --type kind and the outcome flag")
		return 2
	}
	r := ledger.Row{Kind: "fixture", Fixture: *fixture, FixtureClass: *class, Type: *kind, Tier: *tier, Suite: *suite, ResolvedModel: *resolved, Source: "measured"}
	switch *class {
	case "defect":
		if *detected == *missed {
			fmt.Fprintln(os.Stderr, "[ledger] a defect fixture needs exactly one of --detected, --missed")
			return 2
		}
		r.Detected = detected
	case "clean":
		if *falsePos == *cleanOK {
			fmt.Fprintln(os.Stderr, "[ledger] a clean fixture needs exactly one of --false-positive, --clean-ok")
			return 2
		}
		r.FalsePositive = falsePos
	default:
		fmt.Fprintln(os.Stderr, "[ledger] --class must be defect or clean")
		return 2
	}
	if *adjudicated {
		r.Adjudicated = adjudicated
	}
	cfg, err := providers.Load(providers.ConfigPath(*cfgPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "[ledger]", err)
		return 2
	}
	p, ok := cfg.Providers[*provider]
	if !ok {
		fmt.Fprintf(os.Stderr, "[ledger] unknown provider %q\n", *provider)
		return 2
	}
	r.Reviewer, r.Effort, r.Config = &ledger.AgentModel{Agent: p.Agent, Model: p.Model}, p.Effort, routing.ProviderFingerprint(p)
	if r.Suite == "" {
		if owner, err := contract.LoadProfile(""); err == nil && owner.EffectiveSelection() != nil {
			r.Suite = owner.EffectiveSelection().Suite
		}
	}
	if r.Suite == "" {
		fmt.Fprintln(os.Stderr, "[ledger] --suite is required (no selection.evaluation_suite in the owner's profile)")
		return 2
	}
	return save(r)
}
