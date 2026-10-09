// Package budget enforces cost and quality caps per task and per run (ADR 0002 B1).
// Check reads spend from the ledger starting at the run marker's started_at and returns exit codes 0 (ok), 1 (soft limit), 2 (hard cap).
// Raise persists user-authorized allowances. Cooldowns tracks provider health.
package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/run"
)

// ExitOK means spend is within soft limit.
const ExitOK = 0

// ExitSoft means spend >= soft_ratio * cap; no new tasks allowed.
const ExitSoft = 1

// ExitHard means spend >= hard cap OR review rounds >= max_review_rounds; user must raise.
const ExitHard = 2

// CheckResult holds the outcome of a budget check.
type CheckResult struct {
	Code            int                // ExitOK, ExitSoft, ExitHard
	Message         string             // human reason
	Spend           map[string]float64 // per pool
	Caps            map[string]float64 // effective caps (task or run)
	Approx          map[string]bool    // which pools are estimated
	ReviewRounds    int                // task review rounds so far
	MaxReviewRounds int                // ceiling
	TimeSinceStart  time.Duration      // run elapsed time
}

// BudgetRaise records a user-authorized increase.
type BudgetRaise struct {
	Pool   string  `json:"pool"`
	Amount float64 `json:"amount"`
	Reason string  `json:"reason"`
	At     string  `json:"at"` // RFC3339
}

// ProviderStatus is the health of a provider.
type ProviderStatus string

const (
	StatusOK        ProviderStatus = "ok"
	StatusQuota     ProviderStatus = "quota"
	StatusRateLimit ProviderStatus = "rate_limit"
	StatusHang      ProviderStatus = "hang"
	StatusEmpty     ProviderStatus = "empty"
	StatusUnknown   ProviderStatus = "unknown"
)

// Cooldown records provider health.
type Cooldown struct {
	Provider string         `json:"provider"`
	Status   ProviderStatus `json:"status"`
	LastSeen string         `json:"last_seen"` // RFC3339
	Message  string         `json:"message,omitempty"`
}

// Check reads spend from the ledger starting at the marker's started_at, tallies it against the budget, and returns ExitOK/Soft/Hard.
func Check(markerPath string, p *contract.Profile, task string) (CheckResult, error) {
	if p == nil {
		return CheckResult{}, errors.New("profile is nil")
	}

	// Load the run marker to get started_at
	m, err := run.Load(markerPath)
	if err != nil {
		return CheckResult{}, fmt.Errorf("load marker: %w", err)
	}

	// Parse started_at
	startTime, err := time.Parse(time.RFC3339, m.StartedAt)
	if err != nil {
		return CheckResult{}, fmt.Errorf("parse started_at: %w", err)
	}

	// Load ledger rows since started_at
	rows, err := ledger.LoadStrict(m.StartedAt)
	if err != nil {
		// Ledger unavailable is distinct from backend down
		return CheckResult{}, fmt.Errorf("load ledger: %w", err)
	}

	// Get budget (may be nil if not defined)
	budget := getBudget(p)
	if budget == nil {
		// No budget defined, always OK
		return CheckResult{Code: ExitOK, Message: "no budget defined"}, nil
	}

	// Tally spend per pool
	spend := tallySpend(rows, m.Run, task)

	// Count review rounds for this task if specified
	reviewRounds := countReviewRounds(rows, m.Run, task)

	// Get effective caps
	caps := getEffectiveCaps(budget, task != "")

	// Apply any raises
	raises := loadRaises(m.Run, markerPath)
	for _, r := range raises {
		if c, ok := caps[r.Pool]; ok {
			caps[r.Pool] = c + r.Amount
		}
	}

	// Determine exit code
	code := ExitOK
	message := "within soft limit"
	maxReviewRounds := budget.MaxReviewRounds
	if maxReviewRounds == 0 {
		maxReviewRounds = 2 // default
	}
	softRatio := budget.SoftRatio
	if softRatio == 0 {
		softRatio = 0.7 // default
	}

	// Check review rounds cap
	if task != "" && reviewRounds >= maxReviewRounds {
		code = ExitHard
		message = fmt.Sprintf("review rounds (%d) >= max (%d)", reviewRounds, maxReviewRounds)
	}

	// Check hard and soft caps
	for pool, spent := range spend {
		cap, ok := caps[pool]
		if !ok || cap == 0 {
			continue
		}

		if spent >= cap {
			code = ExitHard
			message = fmt.Sprintf("%s spend (%.0f) >= cap (%.0f)", pool, spent, cap)
			break
		} else if spent >= softRatio*cap && code < ExitSoft {
			code = ExitSoft
			message = fmt.Sprintf("%s spend (%.0f) >= soft limit (%.0f)", pool, spent, softRatio*cap)
		}
	}

	// Track which pools have approximated spend (rows with no exact token count)
	approx := make(map[string]bool)
	for _, row := range rows {
		if row.Run != m.Run || (task != "" && row.Task != task) {
			continue
		}
		if row.Worker != nil && row.WorkerTokens == nil && row.Tokens == nil {
			approx[tokenPool(row.Worker.Agent)] = true
		}
		if row.Reviewer != nil && row.ReviewerTokens == nil {
			approx[tokenPool(row.Reviewer.Agent)] = true
		}
		if row.Kind == "call" && row.Tokens == nil {
			approx[tokenPool(row.Provider)] = true
		}
	}

	return CheckResult{
		Code:            code,
		Message:         message,
		Spend:           spend,
		Caps:            caps,
		Approx:          approx,
		ReviewRounds:    reviewRounds,
		MaxReviewRounds: maxReviewRounds,
		TimeSinceStart:  time.Since(startTime),
	}, nil
}

// Raise persists a user-authorized budget increase.
func Raise(markerPath string, pool string, amount float64, reason string) error {
	if reason == "" {
		return errors.New("reason is required")
	}

	m, err := run.Load(markerPath)
	if err != nil {
		return fmt.Errorf("load marker: %w", err)
	}

	r := BudgetRaise{
		Pool:   pool,
		Amount: amount,
		Reason: reason,
		At:     time.Now().UTC().Format(time.RFC3339),
	}

	// Append to raises log in the same directory as the marker
	raisesPath := filepath.Join(filepath.Dir(markerPath), fmt.Sprintf("raises-%s.jsonl", m.Run))
	f, err := os.OpenFile(raisesPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open raises log: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode raise: %w", err)
	}

	return nil
}

// SaveCooldown persists provider health.
func SaveCooldown(runName string, c Cooldown) error {
	dir := cooldownDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir cooldowns: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%s.json", runName))

	// Load existing cooldowns
	existing, err := LoadCooldowns(runName)
	if err != nil {
		return fmt.Errorf("load existing cooldowns: %w", err)
	}

	// Update or append the cooldown for this provider
	found := false
	for i, cd := range existing {
		if cd.Provider == c.Provider {
			existing[i] = c
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, c)
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cooldown: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write cooldown: %w", err)
	}

	return nil
}

// LoadCooldowns reads persisted provider health.
func LoadCooldowns(runName string) ([]Cooldown, error) {
	path := filepath.Join(cooldownDir(), fmt.Sprintf("%s.json", runName))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cooldowns: %w", err)
	}

	var cooldowns []Cooldown
	if err := json.Unmarshal(data, &cooldowns); err != nil {
		return nil, fmt.Errorf("unmarshal cooldowns: %w", err)
	}

	return cooldowns, nil
}

func cooldownDir() string {
	if d := os.Getenv("REIN_RUN_DIR"); d != "" {
		return filepath.Join(d, "cooldowns")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "worktree-pipeline", "cooldowns")
}

func getBudget(p *contract.Profile) *contract.Budget {
	return p.Budget
}

func tallySpend(rows []ledger.Row, runName, task string) map[string]float64 {
	spend := make(map[string]float64)

	for _, r := range rows {
		// Filter by run
		if r.Run != runName {
			continue
		}

		// Filter by task if specified
		if task != "" && r.Task != task {
			continue
		}

		// Component counts take precedence over a row's aggregate token count.
		if r.WorkerTokens != nil {
			agent := ""
			if r.Worker != nil {
				agent = r.Worker.Agent
			}
			spend[tokenPool(agent)] += float64(*r.WorkerTokens)
		}
		if r.ReviewerTokens != nil {
			agent := ""
			if r.Reviewer != nil {
				agent = r.Reviewer.Agent
			}
			spend[tokenPool(agent)] += float64(*r.ReviewerTokens)
		}
		if r.Tokens != nil && r.WorkerTokens == nil && r.ReviewerTokens == nil {
			agent := r.Provider
			if agent == "" && r.Worker != nil {
				agent = r.Worker.Agent
			}
			spend[tokenPool(agent)] += float64(*r.Tokens)
		}

		// Tally credits
		if r.Credits != nil {
			spend["credits"] += *r.Credits
		}

		// Tally USD
		if r.CostUSD != nil {
			spend["usd"] += *r.CostUSD
		}
	}

	return spend
}

// Token pools identify the harness; they do not infer subscription USD from tokens.
func tokenPool(agent string) string {
	for _, known := range ledger.Agents {
		if agent == known {
			return agent + "_tokens"
		}
	}
	return "unknown_tokens"
}

func countReviewRounds(rows []ledger.Row, runName, task string) int {
	if task == "" {
		return 0
	}

	maxRounds := 0
	for _, r := range rows {
		if r.Run == runName && r.Task == task && r.ReviewRounds != nil && *r.ReviewRounds > maxRounds {
			maxRounds = *r.ReviewRounds
		}
	}

	return maxRounds
}

func getEffectiveCaps(budget *contract.Budget, isTask bool) map[string]float64 {
	caps := make(map[string]float64)

	for pool, pc := range budget.Pools {
		if isTask {
			// For task checks, use TaskCap but also consider RunCap as a ceiling
			if pc.TaskCap > 0 {
				caps[pool] = pc.TaskCap
				// If RunCap exists and is lower, use it as the ceiling
				if pc.RunCap > 0 && pc.RunCap < pc.TaskCap {
					caps[pool] = pc.RunCap
				}
			} else if pc.RunCap > 0 {
				// No TaskCap, so RunCap is the limit
				caps[pool] = pc.RunCap
			}
		} else if pc.RunCap > 0 {
			caps[pool] = pc.RunCap
		}
	}

	return caps
}

func loadRaises(runName string, markerPath string) []BudgetRaise {
	// Read from the same directory as the marker (git common dir)
	raisesPath := filepath.Join(filepath.Dir(markerPath), fmt.Sprintf("raises-%s.jsonl", runName))
	f, err := os.Open(raisesPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var raises []BudgetRaise
	dec := json.NewDecoder(f)
	for {
		var r BudgetRaise
		if err := dec.Decode(&r); err != nil {
			break
		}
		raises = append(raises, r)
	}

	return raises
}
