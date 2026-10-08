// Package ledger records how each worker/reviewer model did (quality and cost) and reports per task type.
// Format: one JSON object per line, compatible with the Python prototype's model-ledger.jsonl.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var Types = []string{"backend", "frontend", "fullstack", "docs", "mechanical", "review"}
var Agents = []string{"claude", "codex", "antigravity", "opencode", "opencode2", "kiro"}

type AgentModel struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
}

type Row struct {
	Kind           string      `json:"kind"`
	Task           string      `json:"task,omitempty"`
	Type           string      `json:"type,omitempty"`
	Worker         *AgentModel `json:"worker,omitempty"`
	Reviewer       *AgentModel `json:"reviewer,omitempty"`
	ReviewRounds   *int        `json:"review_rounds,omitempty"`
	Approved       bool        `json:"approved,omitempty"`
	FirstGatePass  *bool       `json:"first_gate_pass,omitempty"`
	Blockers       *int        `json:"blockers,omitempty"`
	Highs          *int        `json:"highs,omitempty"`
	FalseClaims    *int        `json:"false_claims,omitempty"`
	Drift          *int        `json:"drift,omitempty"`
	GuardDenials   *int        `json:"guard_denials,omitempty"`
	Fallback       string      `json:"fallback,omitempty"`
	Minutes        *float64    `json:"minutes,omitempty"`
	WorkerTokens   *int        `json:"worker_tokens,omitempty"`
	ReviewerTokens *int        `json:"reviewer_tokens,omitempty"`
	CostUSD        *float64    `json:"cost_usd,omitempty"`
	Credits        *float64    `json:"credits,omitempty"`
	Risk           []string    `json:"risk,omitempty"`
	Run            string      `json:"run,omitempty"`
	Issue          *int        `json:"issue,omitempty"`
	MR             *int        `json:"mr,omitempty"`
	Source         string      `json:"source,omitempty"`
	Approx         bool        `json:"approx,omitempty"`
	Notes          string      `json:"notes,omitempty"`
	// advisor calls
	Role       string `json:"role,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	Tokens     *int   `json:"tokens,omitempty"`
	OK         *bool  `json:"ok,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
}

func Path() string {
	if p := os.Getenv("PIPELINE_LEDGER"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "omc", "worktree-pipeline", "model-ledger.jsonl")
}

func PricesPath() string {
	if p := os.Getenv("PIPELINE_PRICES"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(Path()), "prices.json")
}

func ParseAgentModel(v string) (*AgentModel, error) {
	a, m, ok := strings.Cut(v, ":")
	if !ok || m == "" {
		return nil, errors.New("use <agent>:<model>, e.g. codex:gpt-6.1-sol")
	}
	if !contains(Agents, a) {
		return nil, fmt.Errorf("agent must be one of %v", Agents)
	}
	if strings.Contains(m, "fable") {
		return nil, errors.New("fable is never used (user rule)")
	}
	return &AgentModel{a, m}, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func Append(r Row) error {
	r.RecordedAt = time.Now().Format(time.RFC3339)
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("not recorded: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func Load(since string) ([]Row, error) {
	f, err := os.Open(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []Row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			fmt.Fprintf(os.Stderr, "[ledger] skipping bad line %d\n", n)
			continue
		}
		if since != "" && r.RecordedAt < since {
			continue
		}
		rows = append(rows, r)
	}
	return rows, sc.Err()
}

type price struct {
	USDPerMTok *float64 `json:"usd_per_mtok"`
}

func loadPrices() map[string]price {
	b, err := os.ReadFile(PricesPath())
	if err != nil {
		return nil
	}
	raw := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &raw)
	out := map[string]price{}
	for k, v := range raw {
		var p price
		if json.Unmarshal(v, &p) == nil {
			out[k] = p
		}
	}
	return out
}

func costOf(prices map[string]price, model string, tokens *int) *float64 {
	p, ok := prices[model]
	if !ok || p.USDPerMTok == nil || tokens == nil {
		return nil
	}
	v := float64(*tokens) / 1e6 * *p.USDPerMTok
	return &v
}

type Summary struct {
	Approved                                    int      // tasks that reached APPROVE
	AvgRoundsApproved                           *float64 // rounds-to-approve over approved tasks only
	Type, Worker                                string
	N                                           int
	ApproveRate                                 float64
	AvgRounds, FirstGate, BlkHigh, DriftPerTask *float64
	FalseClaimTasks, Fallbacks, ApproxRows      int
	AvgMinutes                                  *float64
	WorkerTokens, ReviewerTokens                int
	Credits                                     float64
	CostUSD                                     *float64
}

func mean(vals []float64) *float64 {
	if len(vals) == 0 {
		return nil
	}
	s := 0.0
	for _, v := range vals {
		s += v
	}
	m := s / float64(len(vals))
	return &m
}

func Summarize(rows []Row) []Summary {
	prices := loadPrices()
	groups := map[[2]string][]Row{}
	for _, r := range rows {
		if r.Kind != "task" || r.Worker == nil {
			continue
		}
		k := [2]string{r.Type, r.Worker.Agent + ":" + r.Worker.Model}
		groups[k] = append(groups[k], r)
	}
	var out []Summary
	for k, rs := range groups {
		s := Summary{Type: k[0], Worker: k[1], N: len(rs)}
		var rounds, roundsOK, gates, bh, drift, mins []float64
		approved := 0
		var cost float64
		haveCost := false
		for _, r := range rs {
			if r.Approved {
				approved++
			}
			if r.ReviewRounds != nil {
				rounds = append(rounds, float64(*r.ReviewRounds))
				if r.Approved {
					roundsOK = append(roundsOK, float64(*r.ReviewRounds))
				}
			}
			if r.FirstGatePass != nil {
				g := 0.0
				if *r.FirstGatePass {
					g = 1
				}
				gates = append(gates, g)
			}
			bh = append(bh, float64(deref(r.Blockers)+deref(r.Highs)))
			if r.Drift != nil {
				drift = append(drift, float64(*r.Drift))
			}
			if deref(r.FalseClaims) > 0 {
				s.FalseClaimTasks++
			}
			if r.Fallback != "" {
				s.Fallbacks++
			}
			if r.Approx || r.Source == "memory" {
				s.ApproxRows++
			}
			if r.Minutes != nil {
				mins = append(mins, *r.Minutes)
			}
			s.WorkerTokens += deref(r.WorkerTokens)
			s.ReviewerTokens += deref(r.ReviewerTokens)
			if r.Credits != nil {
				s.Credits += *r.Credits
			}
			c := r.CostUSD
			if c == nil {
				parts := []*float64{costOf(prices, r.Worker.Model, r.WorkerTokens)}
				if r.Reviewer != nil {
					parts = append(parts, costOf(prices, r.Reviewer.Model, r.ReviewerTokens))
				}
				for _, p := range parts {
					if p != nil {
						v := *p
						if c == nil {
							c = &v
						} else {
							*c += v
						}
					}
				}
			}
			if c != nil {
				cost += *c
				haveCost = true
			}
		}
		s.ApproveRate = float64(approved) / float64(len(rs))
		s.Approved, s.AvgRoundsApproved = approved, mean(roundsOK)
		s.AvgRounds, s.FirstGate, s.BlkHigh, s.DriftPerTask, s.AvgMinutes = mean(rounds), mean(gates), mean(bh), mean(drift), mean(mins)
		if haveCost {
			s.CostUSD = &cost
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Worker < out[j].Worker
	})
	return out
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func f1(p *float64, pct bool) string {
	if p == nil {
		return "-"
	}
	if pct {
		return fmt.Sprintf("%.0f%%", *p*100)
	}
	return fmt.Sprintf("%.1f", *p)
}

func Report(w io.Writer, rows []Row, typ string) {
	var tasks []Row
	calls := map[string]*struct {
		n, tokens, failed int
		credits, minutes  float64
		cost              *float64
	}{}
	prices := loadPrices()
	for _, r := range rows {
		switch r.Kind {
		case "task":
			if typ == "" || r.Type == typ {
				tasks = append(tasks, r)
			}
		case "call":
			prov := r.Provider
			if prov == "" {
				prov = "unknown"
			}
			k := r.Role + "@" + prov + ":" + r.Model
			v, ok := calls[k]
			if !ok {
				v = &struct {
					n, tokens, failed int
					credits, minutes  float64
					cost              *float64
				}{}
				calls[k] = v
			}
			v.n++
			v.tokens += deref(r.Tokens)
			if r.Credits != nil {
				v.credits += *r.Credits
			}
			if r.Minutes != nil {
				v.minutes += *r.Minutes
			}
			if r.OK != nil && !*r.OK {
				v.failed++
			}
			c := r.CostUSD
			if c == nil {
				c = costOf(prices, r.Model, r.Tokens)
			}
			if c != nil {
				if v.cost == nil {
					z := 0.0
					v.cost = &z
				}
				*v.cost += *c
			}
		}
	}
	fmt.Fprintf(w, "ledger: %s  (%d tasks, %d advisor calls)\n\n", Path(), len(tasks), len(rows)-len(tasks))
	fmt.Fprintln(w, "type | worker | n | approve | rounds | gate1 | blk+high | falseclaim | drift | fallback | min | tok(w/r) | credits | usd | approx")
	fmt.Fprintln(w, strings.Repeat("-", 120))
	for _, s := range Summarize(tasks) {
		fmt.Fprintf(w, "%s | %s | %d | %.0f%% | %s | %s | %s | %d | %s | %d | %s | %d/%d | %.2f | %s | %d\n",
			s.Type, s.Worker, s.N, s.ApproveRate*100, f1(s.AvgRounds, false), f1(s.FirstGate, true), f1(s.BlkHigh, false),
			s.FalseClaimTasks, f1(s.DriftPerTask, false), s.Fallbacks, f1(s.AvgMinutes, false), s.WorkerTokens, s.ReviewerTokens,
			s.Credits, money(s.CostUSD), s.ApproxRows)
	}
	if len(calls) > 0 {
		keys := make([]string, 0, len(calls))
		for k := range calls {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintln(w, "\nadvisor calls:")
		for _, k := range keys {
			v := calls[k]
			fmt.Fprintf(w, "  %s: %d calls, %d tokens, %.2f credits, %.0f min, usd %s, %d failed\n", k, v.n, v.tokens, v.credits, v.minutes, money(v.cost), v.failed)
		}
	}
	fmt.Fprintln(w, "\nrounds = review rounds until APPROVE (each extra round = one more strong-model review). approx = rows rebuilt from memory.")
}

func money(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *p)
}

// Suggest proposes routing changes only where every compared worker has at least minN tasks.
func Suggest(w io.Writer, rows []Row, minN int) {
	byType := map[string][]Summary{}
	for _, s := range Summarize(rows) {
		byType[s.Type] = append(byType[s.Type], s)
	}
	fmt.Fprintf(w, "Suggestions (evidence threshold: n >= %d per worker; the user approves any routing change)\n\n", minN)
	for _, t := range Types {
		var cands []Summary
		var thin []string
		for _, s := range byType[t] {
			// compare only observed completions: n approved tasks, rounds measured on them
			if s.Approved >= minN && s.AvgRoundsApproved != nil {
				cands = append(cands, s)
			} else {
				thin = append(thin, fmt.Sprintf("%s (approved %d of %d)", s.Worker, s.Approved, s.N))
			}
		}
		if len(cands) < 2 {
			if len(byType[t]) > 0 {
				msg := strings.Join(thin, ", ")
				if msg == "" {
					msg = "one worker only"
				}
				fmt.Fprintf(w, "- %s: not enough data to compare (%s)\n", t, msg)
			}
			continue
		}
		// approval rate first (a rejected task is the worst outcome), then rounds-to-approve, then blockers+highs
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].ApproveRate != cands[j].ApproveRate {
				return cands[i].ApproveRate > cands[j].ApproveRate
			}
			if *cands[i].AvgRoundsApproved != *cands[j].AvgRoundsApproved {
				return *cands[i].AvgRoundsApproved < *cands[j].AvgRoundsApproved
			}
			return f(cands[i].BlkHigh) < f(cands[j].BlkHigh)
		})
		b, z := cands[0], cands[len(cands)-1]
		fmt.Fprintf(w, "- %s: %s approves %.0f%% in %.1f rounds vs %s %.0f%% in %.1f (blk+high %s vs %s) -> consider routing %s to %s\n",
			t, b.Worker, b.ApproveRate*100, *b.AvgRoundsApproved, z.Worker, z.ApproveRate*100, *z.AvgRoundsApproved,
			f1(b.BlkHigh, false), f1(z.BlkHigh, false), t, b.Worker)
	}
	fmt.Fprintln(w, "\nCost is compared only when prices or cost_usd are recorded; fewer review rounds already means fewer strong-model reviews.")
}

func f(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
