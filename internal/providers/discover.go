package providers

// Metadata discovery (docs/ROUTING_SELECTION_DESIGN.md, "Refresh before every selection"). Each adapter asks one harness
// CLI which models it offers WITHOUT inference and returns an evidence-bearing snapshot. Nothing here selects a model, and
// nothing is cached: a failed or unproven refresh excludes the harness, it never falls back to an earlier answer.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Freshness says what a catalog query proves about the upstream catalog.
type Freshness string

const (
	RemoteVerified   Freshness = "remote_verified" // a remote refresh or provider cache validator proved the catalog current
	ClientCatalog    Freshness = "client_catalog"  // the local client answered; says nothing about upstream freshness
	FreshnessUnknown Freshness = "unknown"
)

// InvStatus is the outcome of one harness refresh.
type InvStatus string

const (
	InvComplete    InvStatus = "complete"    // every page and row parsed under the strict schema
	InvPartial     InvStatus = "partial"     // pagination did not finish
	InvFailed      InvStatus = "failed"      // exec error, timeout, bad exit, invalid schema, version mismatch, cancelled
	InvUnsupported InvStatus = "unsupported" // no unattended adapter exists for this harness
)

// ModelEntry is one model a harness offers.
type ModelEntry struct {
	ID            string   `json:"id"`
	Variants      []string `json:"variants,omitempty"`     // reasoning-effort variants
	Capabilities  []string `json:"capabilities,omitempty"` // e.g. "input:text", "input:image"
	Billing       string   `json:"billing,omitempty"`      // "credits" when the catalog says so, else unknown
	Unit          string   `json:"unit,omitempty"`         // native unit, e.g. "kiro_credits"
	Multiplier    *float64 `json:"multiplier,omitempty"`   // credit multiplier
	ContextTokens int      `json:"context_tokens,omitempty"`
	Hidden        bool     `json:"hidden,omitempty"`
	Alias         bool     `json:"alias,omitempty"`   // a moving alias such as "auto"
	Enabled       *bool    `json:"enabled,omitempty"` // nil = unknown; no current adapter proves entitlement
}

// Limit is an observed rate or quota window. Unknown stays nil.
type Limit struct {
	ID            string     `json:"id"`
	Unit          string     `json:"unit"` // "percent"
	UsedPercent   *float64   `json:"used_percent,omitempty"`
	WindowMinutes *int64     `json:"window_minutes,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	Allowed       *bool      `json:"allowed,omitempty"`
}

// Inventory is one harness's refresh result. It holds no credential, environment value or raw output.
type Inventory struct {
	Harness    string       `json:"harness"`
	Adapter    string       `json:"adapter"`
	Executable string       `json:"executable,omitempty"`
	Version    string       `json:"version,omitempty"`
	Scope      string       `json:"scope"`
	QueriedAt  time.Time    `json:"queried_at"`
	EvidenceAt *time.Time   `json:"evidence_at,omitempty"` // upstream evidence time; nil = unknown (every current adapter)
	SourceHash string       `json:"source_hash,omitempty"`
	Status     InvStatus    `json:"status"`
	Freshness  Freshness    `json:"freshness"`
	Models     []ModelEntry `json:"models,omitempty"`
	Limits     []Limit      `json:"limits,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

// DiscoverOptions scopes one refresh round.
type DiscoverOptions struct {
	Dir       string        // project directory the catalog is scoped to ("" = current directory); the child's working directory
	Timeout   time.Duration // wall limit PER HARNESS, > 0
	Harnesses []string      // harnesses to refresh; empty = every distinct Agent in cfg.Providers
}

const (
	maxOutput    = 4 << 20 // per stream
	maxPages     = 100
	maxChatter   = 1000 // unsolicited protocol messages tolerated while waiting for one response
	graceful     = 2 * time.Second
	maxReasonLen = 200
)

// opencode catalog settling (see collectOpencode); variables so tests need not wait.
var (
	opencodeAttempts = 5
	opencodeSettle   = 300 * time.Millisecond
)

var (
	idRx      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+~-]*$`)
	versionRx = regexp.MustCompile(`\d+\.\d+(\.\d+)*`)
)

// adapters: harness -> how to query it. min is the oldest version the wire format was verified against (same major required).
var adapters = map[string]struct{ name, exe, min string }{
	"codex":       {"codex-app-server/model-list", "codex", "0.162.0"},
	"kiro":        {"kiro-cli/list-models-json", "kiro-cli", "2.21.0"},
	"antigravity": {"agy/models", "agy", "1.3.2"},
	"opencode":    {"opencode/models", "opencode", "2.0.0"},
	"opencode2":   {"opencode/models", "opencode2", "2.0.0"},
}

// Discover refreshes every requested harness concurrently and returns one Inventory per harness, sorted by harness name. It
// never panics and never caches. Every process it starts is gone before it returns.
func Discover(ctx context.Context, cfg *Config, o DiscoverOptions) []Inventory {
	if cfg == nil {
		cfg = &Config{}
	}
	names := slices.Clone(o.Harnesses)
	if len(names) == 0 {
		for _, p := range cfg.Providers {
			names = append(names, p.Agent)
		}
	}
	sort.Strings(names)
	names = slices.Compact(names)
	dir := o.Dir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	out := make([]Inventory, len(names))
	var wg sync.WaitGroup
	for i, h := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					out[i] = Inventory{Harness: h, Adapter: "internal", Scope: "-", QueriedAt: time.Now().UTC(), Status: InvFailed, Freshness: FreshnessUnknown, Reason: "internal error"}
				}
			}()
			out[i] = discoverOne(ctx, cfg, h, dir, o.Timeout)
		}()
	}
	wg.Wait()
	return out
}

// Has reports whether the inventory lists model exactly.
func (i Inventory) Has(model string) (ModelEntry, bool) {
	for _, m := range i.Models {
		if m.ID == model {
			return m, true
		}
	}
	return ModelEntry{}, false
}

// HashInventories is the sha256 of the canonical JSON of the inventories ordered by harness, binding a decision to the exact snapshot.
func HashInventories(invs []Inventory) string {
	s := slices.Clone(invs)
	sort.SliceStable(s, func(a, b int) bool { return s[a].Harness < s[b].Harness })
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (i *Inventory) fail(status InvStatus, reason string) {
	i.Status, i.Models, i.Limits = status, nil, nil
	i.note(reason)
}

// note stores a short single-line reason; it never carries raw command output.
func (i *Inventory) note(reason string) {
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen]
	}
	i.Reason = reason
}

func discoverOne(ctx context.Context, cfg *Config, harness, dir string, timeout time.Duration) Inventory {
	inv := Inventory{Harness: harness, Adapter: "none", Scope: "-", QueriedAt: time.Now().UTC(), Status: InvFailed, Freshness: FreshnessUnknown}
	if harness == "claude" {
		inv.fail(InvUnsupported, "no unattended complete model catalog adapter verified (Claude plan entitlement is not the Anthropic API catalog)")
		return inv
	}
	ad, ok := adapters[harness]
	if !ok {
		inv.fail(InvUnsupported, "no discovery adapter for harness "+harness)
		return inv
	}
	inv.Adapter = ad.name
	if timeout <= 0 {
		inv.fail(InvFailed, "discovery timeout must be > 0")
		return inv
	}
	argv := slices.Clone(cfg.Discovery[harness].Command)
	if len(argv) == 0 {
		argv = []string{ad.exe}
	}
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		inv.fail(InvFailed, "executable not found")
		return inv
	}
	argv[0], inv.Executable = exe, exe
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, why := runOnce(ctx, append(slices.Clone(argv), "--version"), dir, true)
	if why != "" {
		inv.fail(InvFailed, "version check: "+why)
		return inv
	}
	ver := versionRx.FindString(string(out))
	if ver == "" {
		inv.fail(InvFailed, "version unreadable")
		return inv
	}
	inv.Version = ver
	if why := versionOK(ver, ad.min); why != "" {
		inv.fail(InvFailed, why)
		return inv
	}
	inv.Freshness = ClientCatalog // every adapter: the local client answered; none proves the upstream catalog is current
	hint := ""
	switch harness {
	case "codex":
		hint = collectCodex(ctx, argv, dir, &inv)
	case "kiro":
		collectKiro(ctx, argv, dir, &inv)
	case "antigravity":
		collectAgy(ctx, argv, dir, &inv)
	default:
		collectOpencode(ctx, argv, dir, &inv)
	}
	if inv.Status != InvComplete && inv.Status != InvPartial {
		inv.Freshness = FreshnessUnknown
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{harness, exe, ver, dir, hint}, "|")))
	inv.Scope = hex.EncodeToString(sum[:])[:16]
	return inv
}

func versionOK(got, min string) string {
	g, m := versionParts(got), versionParts(min)
	if g[0] != m[0] || slices.Compare(g, m) < 0 {
		return fmt.Sprintf("unsupported version %s (adapter verified against %s)", got, min)
	}
	return ""
}

func versionParts(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	for len(out) < 3 {
		out = append(out, 0)
	}
	return out
}

// ---- process plumbing ----

// capBuf collects output up to maxOutput. Past the cap it keeps accepting (so the child never blocks on a full pipe) but drops data.
type capBuf struct {
	b    bytes.Buffer
	over bool
}

func (c *capBuf) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > maxOutput {
		c.over = true
		return len(p), nil
	}
	return c.b.Write(p)
}

// runOnce runs a fixed argv to completion and returns its stdout (stderr is discarded unless merged, for version banners).
// A non-empty second result says why it failed. The child's whole process group is killed before returning, on every path.
// stdout goes through a pipe on purpose: some CLIs print nothing when stdout is a regular file.
func runOnce(ctx context.Context, argv []string, dir string, merge bool) ([]byte, string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.WaitDelay = dir, graceful
	setGroup(cmd)
	buf := &capBuf{}
	cmd.Stdout = buf
	if merge {
		cmd.Stderr = buf
	}
	if err := cmd.Start(); err != nil {
		return nil, "cannot start executable"
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	killed := false
	select {
	case err = <-done:
	case <-ctx.Done():
		killed = true
		killGroup(cmd)
		<-done
	}
	killGroup(cmd) // descendants that outlived the leader
	switch {
	case killed:
		return nil, ctxWhy(ctx)
	case errors.Is(err, exec.ErrWaitDelay):
		return nil, "output pipe held open by a descendant"
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Sprintf("exit status %d", ee.ExitCode())
		}
		return nil, "execution error"
	case buf.over:
		return nil, "output too large"
	}
	return buf.b.Bytes(), ""
}

func ctxWhy(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return "canceled"
}

// rpc is a line-oriented JSON-RPC session with a child process. Its stdout writer never blocks, so a consumer that stopped
// reading cannot wedge the child or Wait.
type rpc struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	mu      sync.Mutex
	lines   [][]byte
	partial []byte
	total   int
	over    bool
	wake    chan struct{}
	exited  chan struct{}
}

func (r *rpc) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total += len(p); r.total > maxOutput {
		r.over = true
	} else {
		r.partial = append(r.partial, p...)
		for {
			i := bytes.IndexByte(r.partial, '\n')
			if i < 0 {
				break
			}
			if line := bytes.TrimSpace(r.partial[:i]); len(line) > 0 {
				r.lines = append(r.lines, slices.Clone(line))
			}
			r.partial = r.partial[i+1:]
		}
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return len(p), nil
}

func startRPC(argv []string, dir string) (*rpc, error) {
	r := &rpc{wake: make(chan struct{}, 1), exited: make(chan struct{})}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.WaitDelay, cmd.Stdout = dir, graceful, r // stderr is discarded
	setGroup(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.cmd, r.in = cmd, in
	go func() { _ = cmd.Wait(); close(r.exited) }()
	return r, nil
}

// close ends the session on every path: stdin first, a short wait for a clean exit (skipped once the context is already done:
// a timed-out or cancelled refresh has no time to be polite), then the whole process group.
func (r *rpc) close(ctx context.Context) {
	_ = r.in.Close()
	if ctx.Err() == nil {
		t := time.NewTimer(graceful)
		select {
		case <-r.exited:
		case <-t.C:
		}
		t.Stop()
	}
	killGroup(r.cmd)
	<-r.exited
}

var errSchema = errors.New("invalid schema")

func (r *rpc) next(ctx context.Context) ([]byte, error) {
	for {
		r.mu.Lock()
		if r.over {
			r.mu.Unlock()
			return nil, errors.New("output too large")
		}
		if len(r.lines) > 0 {
			l := r.lines[0]
			r.lines = r.lines[1:]
			r.mu.Unlock()
			return l, nil
		}
		r.mu.Unlock()
		select {
		case <-r.wake:
		case <-r.exited:
			r.mu.Lock()
			n := len(r.lines)
			r.mu.Unlock()
			if n == 0 {
				return nil, io.EOF
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (r *rpc) send(v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = r.in.Write(append(b, '\n'))
	return err
}

// call sends one request and returns its result. Notifications are ignored; a server-to-client request is refused (nothing is
// ever approved or executed on the server's behalf).
func (r *rpc) call(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := r.send(req); err != nil {
		return nil, err
	}
	for n := 0; n <= maxChatter; n++ {
		line, err := r.next(ctx)
		if err != nil {
			return nil, err
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &m) != nil {
			return nil, errSchema
		}
		switch {
		case len(m.ID) > 0 && m.Method != "":
			_ = r.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
		case len(m.ID) > 0 && string(m.ID) == strconv.Itoa(id):
			if len(m.Error) > 0 && string(m.Error) != "null" {
				return nil, errors.New("server returned an error")
			}
			if len(m.Result) == 0 {
				return nil, errSchema
			}
			return m.Result, nil
		}
	}
	return nil, errors.New("too many unsolicited messages")
}

func (i *Inventory) failErr(ctx context.Context, err error) {
	switch {
	case ctx.Err() != nil:
		i.fail(InvFailed, ctxWhy(ctx))
	case errors.Is(err, errSchema):
		i.fail(InvFailed, "invalid schema")
	case errors.Is(err, io.EOF):
		i.fail(InvFailed, "server closed the connection")
	default:
		i.fail(InvFailed, err.Error())
	}
}

func sha(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validID(id string) bool { return len(id) <= 200 && idRx.MatchString(id) }

// ---- codex ----

func collectCodex(ctx context.Context, argv []string, dir string, inv *Inventory) (hint string) {
	r, err := startRPC(append(slices.Clone(argv), "app-server"), dir)
	if err != nil {
		inv.fail(InvFailed, "cannot start app-server")
		return ""
	}
	defer r.close(ctx)

	res, err := r.call(ctx, 1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "rein", "version": "discover"}})
	if err != nil {
		inv.failErr(ctx, err)
		return ""
	}
	var init struct {
		UserAgent string `json:"userAgent"`
		CodexHome string `json:"codexHome"`
	}
	if json.Unmarshal(res, &init) != nil || !strings.HasPrefix(init.UserAgent, "rein/") {
		inv.fail(InvFailed, "invalid schema")
		return ""
	}
	if server, _, _ := strings.Cut(strings.TrimPrefix(init.UserAgent, "rein/"), " "); server != inv.Version {
		inv.fail(InvFailed, "server/cli version mismatch")
		return ""
	}
	if err := r.send(map[string]any{"method": "initialized"}); err != nil {
		inv.failErr(ctx, err)
		return ""
	}

	type effort struct {
		Effort string `json:"reasoningEffort"`
	}
	type model struct {
		ID         string   `json:"id"`
		Model      string   `json:"model"`
		Hidden     bool     `json:"hidden"`
		Modalities []string `json:"inputModalities"`
		Efforts    []effort `json:"supportedReasoningEfforts"`
	}
	var raw [][]byte
	var cursor *string
	seen := map[string]bool{}
	complete := false
	for page := 0; page < maxPages && !complete; page++ {
		res, err := r.call(ctx, 2+page, "model/list", map[string]any{"cursor": cursor, "includeHidden": false, "limit": 50})
		if err != nil {
			inv.failErr(ctx, err)
			return ""
		}
		var mr struct {
			Data       *[]model `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if json.Unmarshal(res, &mr) != nil || mr.Data == nil {
			inv.fail(InvFailed, "invalid schema")
			return ""
		}
		raw = append(raw, res)
		for _, m := range *mr.Data {
			id := m.Model
			if id == "" {
				id = m.ID
			}
			if !validID(id) {
				inv.fail(InvFailed, "invalid schema: model id")
				return ""
			}
			if _, dup := inv.Has(id); dup {
				continue
			}
			e := ModelEntry{ID: id, Hidden: m.Hidden}
			for _, mod := range m.Modalities {
				e.Capabilities = append(e.Capabilities, "input:"+mod)
			}
			for _, ef := range m.Efforts {
				if ef.Effort != "" {
					e.Variants = append(e.Variants, ef.Effort)
				}
			}
			inv.Models = append(inv.Models, e)
		}
		switch {
		case mr.NextCursor == nil || *mr.NextCursor == "":
			complete = true
		case seen[*mr.NextCursor]:
			inv.Status = InvPartial
			inv.note("pagination repeated a cursor")
			page = maxPages
		default:
			seen[*mr.NextCursor], cursor = true, mr.NextCursor
		}
	}
	if !complete && inv.Status != InvPartial {
		inv.Status = InvPartial
		inv.note("page cap reached")
	}
	if len(inv.Models) == 0 {
		inv.fail(InvFailed, "empty catalog")
		return ""
	}
	inv.SourceHash = sha(raw...)
	if !complete {
		return "" // partial: excluded by the caller, nothing more to read
	}
	inv.Status = InvComplete

	// observed limits: best effort, never changes the model evidence
	account := ""
	lim, err := r.call(ctx, 2+maxPages, "account/rateLimits/read", nil)
	if err == nil {
		account, err = codexLimits(lim, inv)
	}
	if err != nil && inv.Status == InvComplete {
		inv.Limits = nil
		inv.note("rate limits unavailable: " + shortWhy(ctx, err))
	}
	sum := sha256.Sum256([]byte(init.CodexHome + "|" + account))
	return hex.EncodeToString(sum[:])
}

func shortWhy(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return ctxWhy(ctx)
	}
	if errors.Is(err, errSchema) {
		return "invalid schema"
	}
	return err.Error()
}

// codexLimits maps an account/rateLimits/read result to Limits and returns the account id (only ever hashed by the caller).
func codexLimits(res json.RawMessage, inv *Inventory) (string, error) {
	type window struct {
		Used    *float64 `json:"usedPercent"`
		Mins    *int64   `json:"windowDurationMins"`
		ResetAt *int64   `json:"resetsAt"`
	}
	type snap struct {
		ID        *string `json:"limitId"`
		Primary   *window `json:"primary"`
		Secondary *window `json:"secondary"`
	}
	var rl struct {
		AccountID  *string         `json:"accountId"`
		Allowed    *bool           `json:"ordinaryUsageAllowed"`
		RateLimits *snap           `json:"rateLimits"`
		ByID       map[string]snap `json:"rateLimitsByLimitId"`
	}
	if json.Unmarshal(res, &rl) != nil || (rl.RateLimits == nil && len(rl.ByID) == 0) {
		return "", errSchema
	}
	snaps := map[string]snap{}
	if len(rl.ByID) > 0 {
		snaps = rl.ByID
	} else {
		id := "default"
		if rl.RateLimits.ID != nil && *rl.RateLimits.ID != "" {
			id = *rl.RateLimits.ID
		}
		snaps[id] = *rl.RateLimits
	}
	ids := make([]string, 0, len(snaps))
	for id := range snaps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var limits []Limit
	for _, id := range ids {
		for _, w := range []struct {
			name string
			w    *window
		}{{"primary", snaps[id].Primary}, {"secondary", snaps[id].Secondary}} {
			if w.w == nil || w.w.Used == nil || math.IsNaN(*w.w.Used) {
				continue
			}
			l := Limit{ID: id + "/" + w.name, Unit: "percent", UsedPercent: w.w.Used, WindowMinutes: w.w.Mins, Allowed: rl.Allowed}
			if w.w.ResetAt != nil {
				t := time.Unix(*w.w.ResetAt, 0).UTC()
				l.ResetsAt = &t
			}
			limits = append(limits, l)
		}
	}
	inv.Limits = limits
	if rl.AccountID != nil {
		return *rl.AccountID, nil
	}
	return "", nil
}

// ---- kiro ----

func collectKiro(ctx context.Context, argv []string, dir string, inv *Inventory) {
	out, why := runOnce(ctx, append(slices.Clone(argv), "chat", "--list-models", "--format", "json"), dir, false)
	if why != "" {
		inv.fail(InvFailed, why)
		return
	}
	var doc struct {
		Models *[]struct {
			ID   string   `json:"model_id"`
			Ctx  *int     `json:"context_window_tokens"`
			Mult *float64 `json:"rate_multiplier"`
			Unit string   `json:"rate_unit"`
		} `json:"models"`
	}
	if json.Unmarshal(out, &doc) != nil || doc.Models == nil {
		inv.fail(InvFailed, "invalid schema")
		return
	}
	for _, m := range *doc.Models {
		if !validID(m.ID) || m.Unit != "Credit" || m.Mult == nil || math.IsNaN(*m.Mult) || math.IsInf(*m.Mult, 0) || *m.Mult < 0 || m.Ctx == nil || *m.Ctx < 0 {
			inv.fail(InvFailed, "invalid schema")
			return
		}
		mult := *m.Mult
		inv.Models = append(inv.Models, ModelEntry{ID: m.ID, Billing: "credits", Unit: "kiro_credits", Multiplier: &mult, ContextTokens: *m.Ctx, Alias: m.ID == "auto"})
	}
	finishLines(inv, out)
}

// ---- agy ----

var agyEffort = regexp.MustCompile(`-(low|medium|high|xhigh|max)$`)

func collectAgy(ctx context.Context, argv []string, dir string, inv *Inventory) {
	out, why := runOnce(ctx, append(slices.Clone(argv), "models"), dir, false) // stderr says "Fetching available models..."
	if why != "" {
		inv.fail(InvFailed, why)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, _, ok := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if !ok || !validID(id) {
			inv.fail(InvFailed, "invalid schema")
			return
		}
		e := ModelEntry{ID: id}
		if m := agyEffort.FindStringSubmatch(id); m != nil {
			e.Variants = []string{m[1]}
		}
		inv.Models = append(inv.Models, e)
	}
	finishLines(inv, out)
}

// ---- opencode ----

func collectOpencode(ctx context.Context, argv []string, dir string, inv *Inventory) {
	// `opencode models` talks to a background service this adapter does not own, and the answer is scoped to the working
	// directory. Observed on opencode 2.0.20: the FIRST call for a location returns nothing while the service loads it, and the
	// list can still move between calls (543 entries, later 497). One answer is therefore not a catalog: accept it only once two
	// consecutive calls agree on a non-empty list, otherwise report a partial inventory.
	// ponytail: agreement proves the service settled, not that every listed model is enabled for this account (Enabled stays
	// unknown); the V2 /api/model route would add enabled flags and rates.
	var prev []string
	var raw []byte
	for attempt := 0; attempt < opencodeAttempts; attempt++ {
		out, why := runOnce(ctx, append(slices.Clone(argv), "models"), dir, false)
		if why != "" {
			inv.fail(InvFailed, why)
			return
		}
		var cur []string
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if !strings.Contains(line, "/") || !validID(line) {
				inv.fail(InvFailed, "invalid schema")
				return
			}
			cur = append(cur, line)
		}
		sort.Strings(cur)
		if len(cur) > 0 && slices.Equal(cur, prev) {
			for _, id := range slices.Compact(cur) {
				inv.Models = append(inv.Models, ModelEntry{ID: id})
			}
			finishLines(inv, raw)
			return
		}
		prev, raw = cur, out
		select {
		case <-ctx.Done():
			inv.fail(InvFailed, ctxWhy(ctx))
			return
		case <-time.After(opencodeSettle):
		}
	}
	if len(prev) == 0 {
		inv.fail(InvFailed, "empty catalog")
		return
	}
	inv.Models = nil
	for _, id := range slices.Compact(prev) {
		inv.Models = append(inv.Models, ModelEntry{ID: id})
	}
	inv.Status, inv.SourceHash = InvPartial, sha(raw)
	inv.note("catalog did not settle between calls")
}

// finishLines completes a one-shot adapter: an empty catalog is no evidence, otherwise the parsed bytes are hashed.
func finishLines(inv *Inventory, raw []byte) {
	if len(inv.Models) == 0 {
		inv.fail(InvFailed, "empty catalog")
		return
	}
	inv.Status, inv.SourceHash = InvComplete, sha(raw)
}
