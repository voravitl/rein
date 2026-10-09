package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fake harness CLI is this test binary re-executed (like internal/routing's TestProbeProcess). REIN_FAKE_SCENARIO picks the
// behaviour; the adapter's own arguments follow "--".

const secret = "sk-test-SECRET-123"

var codexModels = []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}

const kiroJSON = `{"models":[{"model_name":"auto","description":"x","model_id":"auto","context_window_tokens":1000000,"rate_multiplier":1.0,"rate_unit":"Credit"},
{"model_name":"claude-sonnet-5.5","description":"x","model_id":"claude-sonnet-5.5","context_window_tokens":1000000,"rate_multiplier":1.3,"rate_unit":"Credit"},
{"model_name":"qwen3-coder-next","description":"x","model_id":"qwen3-coder-next","context_window_tokens":256000,"rate_multiplier":0.05,"rate_unit":"Credit"}],"default_model":"auto"}`

func TestDiscoverHelper(t *testing.T) {
	sc := os.Getenv("REIN_FAKE_SCENARIO")
	if sc == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if m := os.Getenv("REIN_FAKE_MARKER"); m != "" { // proves the CLI was started at all
		_ = os.WriteFile(m, []byte("started"), 0o600)
	}
	if pf := os.Getenv("REIN_FAKE_PIDFILE"); pf != "" { // leader pid, then a grandchild in the same process group
		gc := exec.Command("sleep", "300")
		_ = gc.Start()
		if f, err := os.OpenFile(pf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), gc.Process.Pid)
			f.Close()
		}
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv("REIN_FAKE_VERSION"))
		os.Exit(0)
	}
	switch {
	case len(args) == 1 && args[0] == "app-server":
		fakeCodex(sc)
	case sc == "exit3":
		os.Exit(3)
	case sc == "hang":
		time.Sleep(time.Minute)
	case len(args) >= 1 && args[0] == "chat":
		fmt.Fprintln(os.Stderr, "noise", secret)
		out := map[string]string{"ok": kiroJSON, "badschema": `{"models":[{"model_id":"x"}]}`, "notjson": "hello", "emptymodels": `{"models":[]}`,
			"badunit": `{"models":[{"model_id":"x","context_window_tokens":1,"rate_multiplier":1,"rate_unit":"Token"}]}`,
			"badid":   `{"models":[{"model_id":"x y; rm -rf /","context_window_tokens":1,"rate_multiplier":1,"rate_unit":"Credit"}]}`}[sc]
		fmt.Print(out)
	case len(args) >= 1 && args[0] == "models" && os.Getenv("REIN_FAKE_HARNESS") == "antigravity":
		fmt.Fprintln(os.Stderr, "Fetching available models...", secret)
		switch sc {
		case "ok":
			fmt.Print("gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n\ngpt-oss-120b-medium\tGPT-OSS 120B (Medium)\nclaude-opus-5-5-low\tOpus\n")
		case "notab":
			fmt.Print("gemini-3.8-flash-high Gemini\n")
		}
	case len(args) >= 1 && args[0] == "models":
		n := bumpCounter()
		switch sc {
		case "cold": // first call for a location returns nothing, then the list
			if n > 1 {
				fmt.Print("a/one\na/two\n")
			}
		case "moves": // the list changes between calls 1 and 2, then holds
			fmt.Print("a/one\n")
			if n > 1 {
				fmt.Print("a/two\n")
			}
		case "flap": // never the same twice
			for i := 0; i < n; i++ {
				fmt.Printf("a/m%d\n", i)
			}
		case "ok":
			fmt.Print("ollama-cloud/deepseek-v4-flash:0731\n\nopenrouter/google/gemini-3.5-flash\nopencode-go/glm-5.3-flash\n")
		case "nopath":
			fmt.Print("justaname\n")
		}
	}
	os.Exit(0)
}

// bumpCounter counts invocations in REIN_FAKE_COUNTER (a file) and returns this invocation's number.
func bumpCounter() int {
	f := os.Getenv("REIN_FAKE_COUNTER")
	if f == "" {
		return 1
	}
	n := 0
	if b, err := os.ReadFile(f); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	_ = os.WriteFile(f, []byte(strconv.Itoa(n)), 0o600)
	return n
}

// fakeCodex speaks the app-server JSON-RPC subset over stdio.
func fakeCodex(sc string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	page := 3
	for in.Scan() {
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(in.Bytes(), &req)
		if mf := os.Getenv("REIN_FAKE_METHODS"); mf != "" && req.Method != "" { // what the client asked of the server, for the no-side-effects test
			if f, err := os.OpenFile(mf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				fmt.Fprintln(f, req.Method)
				f.Close()
			}
		}
		if req.ID == nil {
			continue // notifications such as "initialized"
		}
		reply := func(result any) { _ = out.Encode(map[string]any{"id": *req.ID, "result": result}) }
		switch req.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"method": "remoteControl/status/changed", "params": map[string]any{"status": "disabled"}})
			if sc == "chatter" {
				_ = out.Encode(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
			}
			ver := os.Getenv("REIN_FAKE_SERVER_VERSION")
			if sc == "badinit" {
				reply(map[string]any{"nope": true})
				continue
			}
			reply(map[string]any{"userAgent": "rein/" + ver + " (Fake OS)", "codexHome": "/home/x/.codex", "platformFamily": "unix"})
		case "model/list":
			switch sc {
			case "hang":
				time.Sleep(time.Minute)
			case "rpcerror":
				_ = out.Encode(map[string]any{"id": *req.ID, "error": map[string]any{"code": -1, "message": secret}})
				continue
			case "badschema":
				reply(map[string]any{"nodata": true})
				continue
			case "loop":
				reply(map[string]any{"data": []any{map[string]any{"id": "gpt-a", "model": "gpt-a"}}, "nextCursor": "same"})
				continue
			case "endless":
				reply(map[string]any{"data": []any{map[string]any{"id": fmt.Sprintf("gpt-%d", *req.ID), "model": fmt.Sprintf("gpt-%d", *req.ID)}}, "nextCursor": fmt.Sprintf("c%d", *req.ID)})
				continue
			case "badid":
				reply(map[string]any{"data": []any{map[string]any{"id": "bad id; $(x)", "model": "bad id; $(x)"}}})
				continue
			case "badsecond": // a good first page, then garbage: the models already read must not survive the failure
				var p struct {
					Cursor *string `json:"cursor"`
				}
				_ = json.Unmarshal(req.Params, &p)
				if p.Cursor == nil {
					reply(map[string]any{"data": []any{map[string]any{"id": "gpt-ok", "model": "gpt-ok"}}, "nextCursor": "c1"})
				} else {
					reply(map[string]any{"nodata": true})
				}
				continue
			case "empty":
				reply(map[string]any{"data": []any{}})
				continue
			}
			var p struct {
				Cursor *string `json:"cursor"`
			}
			_ = json.Unmarshal(req.Params, &p)
			start := 0
			if p.Cursor != nil {
				fmt.Sscanf(*p.Cursor, "c%d", &start)
			}
			end := min(start+page, len(codexModels))
			var data []any
			for _, m := range codexModels[start:end] {
				data = append(data, map[string]any{"id": m, "model": m, "hidden": false, "inputModalities": []string{"text", "image"},
					"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"}}})
			}
			res := map[string]any{"data": data, "nextCursor": nil}
			if end < len(codexModels) {
				res["nextCursor"] = fmt.Sprintf("c%d", end)
			}
			reply(res)
		case "account/rateLimits/read":
			if sc == "ratefail" {
				_ = out.Encode(map[string]any{"id": *req.ID, "error": map[string]any{"code": -1, "message": secret}})
				continue
			}
			reply(map[string]any{"accountId": "acct-" + secret, "ordinaryUsageAllowed": true,
				"rateLimits": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 12, "windowDurationMins": 300, "resetsAt": 1760000000},
					"secondary": map[string]any{"usedPercent": 40, "windowDurationMins": 10080, "resetsAt": nil}}})
		default:
			_ = out.Encode(map[string]any{"id": *req.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
	os.Exit(0) // stdin closed: a clean exit, as the real server does
}

// fake configures a fake CLI for one harness and returns the config.
func fake(t *testing.T, harness, scenario, version string, extra ...string) *Config {
	t.Helper()
	t.Setenv("REIN_FAKE_SCENARIO", scenario)
	t.Setenv("REIN_FAKE_VERSION", version)
	t.Setenv("REIN_FAKE_SERVER_VERSION", "0.162.0")
	t.Setenv("REIN_FAKE_HARNESS", harness)
	t.Setenv("REIN_FAKE_SECRET", secret)
	cmd := append([]string{os.Args[0], "-test.run=^TestDiscoverHelper$", "--"}, extra...)
	return &Config{Discovery: map[string]DiscoverySpec{harness: {Command: cmd}}}
}

func one(t *testing.T, cfg *Config, harness string) Inventory {
	t.Helper()
	invs := Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: 20 * time.Second, Harnesses: []string{harness}})
	if len(invs) != 1 || invs[0].Harness != harness {
		t.Fatalf("want one %s inventory: %+v", harness, invs)
	}
	return invs[0]
}

func ids(i Inventory) []string {
	var out []string
	for _, m := range i.Models {
		out = append(out, m.ID)
	}
	return out
}

func TestCodexCompleteAcrossPagesWithLimits(t *testing.T) {
	inv := one(t, fake(t, "codex", "chatter", "codex-cli 0.162.0"), "codex")
	if inv.Status != InvComplete || inv.Freshness != ClientCatalog || inv.Adapter == "" || inv.Version != "0.162.0" || inv.EvidenceAt != nil {
		t.Fatalf("%+v", inv)
	}
	if got := ids(inv); strings.Join(got, ",") != strings.Join(codexModels, ",") {
		t.Fatalf("all 3 pages must be collected: %v", got)
	}
	m, ok := inv.Has("gpt-6-sol")
	if !ok || strings.Join(m.Variants, ",") != "low,high" || strings.Join(m.Capabilities, ",") != "input:text,input:image" {
		t.Errorf("%+v %v", m, ok)
	}
	if _, ok := inv.Has("gpt-6"); ok {
		t.Error("Has must be an exact match")
	}
	if len(inv.Limits) != 2 || inv.Limits[0].ID != "codex/primary" || *inv.Limits[0].UsedPercent != 12 || *inv.Limits[0].WindowMinutes != 300 ||
		inv.Limits[0].ResetsAt == nil || inv.Limits[0].ResetsAt.Unix() != 1760000000 || inv.Limits[0].Allowed == nil || !*inv.Limits[0].Allowed || inv.Limits[1].ResetsAt != nil {
		t.Errorf("limits: %+v", inv.Limits)
	}
	if len(inv.Scope) != 16 || inv.SourceHash == "" || inv.Executable == "" || inv.QueriedAt.IsZero() {
		t.Errorf("evidence fields: %+v", inv)
	}
}

func TestCodexFailuresExcludeTheHarness(t *testing.T) {
	for scenario, want := range map[string]struct {
		status InvStatus
		why    string
	}{
		"loop": {InvPartial, "cursor"}, "endless": {InvPartial, "page cap"}, "badschema": {InvFailed, "invalid schema"}, "badsecond": {InvFailed, "invalid schema"},
		"rpcerror": {InvFailed, "server returned an error"}, "badid": {InvFailed, "invalid schema"}, "empty": {InvFailed, "empty catalog"}, "badinit": {InvFailed, "invalid schema"},
	} {
		t.Run(scenario, func(t *testing.T) {
			inv := one(t, fake(t, "codex", scenario, "codex-cli 0.162.0"), "codex")
			if inv.Status != want.status || !strings.Contains(inv.Reason, want.why) {
				t.Fatalf("%+v", inv)
			}
			if inv.Status == InvFailed && (len(inv.Models) != 0 || inv.Freshness != FreshnessUnknown) {
				t.Errorf("a failed refresh must carry no models: %+v", inv)
			}
		})
	}
}

// Discovery asks a harness what it offers and nothing else: no login, no default-model change, no credit purchase or overage.
func TestCodexDiscoveryOnlyReadsAndNeverChangesAnything(t *testing.T) {
	methods := filepath.Join(t.TempDir(), "methods")
	t.Setenv("REIN_FAKE_METHODS", methods)
	if inv := one(t, fake(t, "codex", "chatter", "codex-cli 0.162.0"), "codex"); inv.Status != InvComplete {
		t.Fatalf("%+v", inv)
	}
	b, _ := os.ReadFile(methods)
	seen := map[string]bool{}
	for _, m := range strings.Fields(string(b)) {
		seen[m] = true
	}
	for m := range seen {
		if m != "initialize" && m != "initialized" && m != "model/list" && m != "account/rateLimits/read" {
			t.Errorf("discovery called %q, which is outside the read-only set", m)
		}
	}
	if !seen["initialize"] || !seen["model/list"] || !seen["account/rateLimits/read"] {
		t.Errorf("expected the three reads, saw %v", seen)
	}
	// a server that asks the client to approve something (the "chatter" scenario sends a command-approval request) is refused
	if strings.Contains(string(b), "approval") {
		t.Error("the client must answer server requests with an error, never approve")
	}
}

func TestCodexRateLimitFailureKeepsCompleteCatalog(t *testing.T) {
	inv := one(t, fake(t, "codex", "ratefail", "codex-cli 0.162.0"), "codex")
	if inv.Status != InvComplete || len(inv.Models) != len(codexModels) || len(inv.Limits) != 0 || !strings.Contains(inv.Reason, "rate limits unavailable") {
		t.Fatalf("%+v", inv)
	}
}

func TestVersionGate(t *testing.T) {
	for version, want := range map[string]string{
		"codex-cli 0.161.9": "unsupported version", "codex-cli 1.0.0": "unsupported version", "no version here": "version unreadable",
	} {
		inv := one(t, fake(t, "codex", "ok", version), "codex")
		if inv.Status != InvFailed || !strings.Contains(inv.Reason, want) || len(inv.Models) != 0 {
			t.Errorf("%q: %+v", version, inv)
		}
	}
	cfg := fake(t, "codex", "ok", "codex-cli 0.162.0")
	t.Setenv("REIN_FAKE_SERVER_VERSION", "0.163.0")
	if inv := one(t, cfg, "codex"); inv.Status != InvFailed || !strings.Contains(inv.Reason, "server/cli version mismatch") {
		t.Errorf("%+v", inv)
	}
	for _, c := range []struct{ harness, version string }{{"kiro", "kiro-cli 2.20.0"}, {"antigravity", "1.3.1"}, {"opencode", "opencode v1.9.0"}, {"opencode2", "opencode v3.0.0"}} {
		if inv := one(t, fake(t, c.harness, "ok", c.version), c.harness); inv.Status != InvFailed || !strings.Contains(inv.Reason, "unsupported version") {
			t.Errorf("%s %s: %+v", c.harness, c.version, inv)
		}
	}
}

func TestKiroAdapter(t *testing.T) {
	inv := one(t, fake(t, "kiro", "ok", "kiro-cli 2.21.0"), "kiro")
	if inv.Status != InvComplete || inv.Freshness != ClientCatalog || len(inv.Models) != 3 {
		t.Fatalf("%+v", inv)
	}
	m, _ := inv.Has("claude-sonnet-5.5")
	if m.Unit != "kiro_credits" || m.Billing != "credits" || *m.Multiplier != 1.3 || m.ContextTokens != 1000000 || m.Alias {
		t.Errorf("credit multiplier and context must be preserved: %+v", m)
	}
	if a, _ := inv.Has("auto"); !a.Alias {
		t.Error("auto is an alias")
	}
	for _, scenario := range []string{"badschema", "notjson", "emptymodels", "badunit", "badid", "exit3"} {
		if inv := one(t, fake(t, "kiro", scenario, "kiro-cli 2.21.0"), "kiro"); inv.Status != InvFailed || len(inv.Models) != 0 {
			t.Errorf("%s: %+v", scenario, inv)
		}
	}
}

func TestAgyAdapterIgnoresStderrNoise(t *testing.T) {
	inv := one(t, fake(t, "antigravity", "ok", "1.3.2"), "antigravity")
	if inv.Status != InvComplete || strings.Join(ids(inv), ",") != "gemini-3.8-flash-high,gpt-oss-120b-medium,claude-opus-5-5-low" {
		t.Fatalf("%+v", inv)
	}
	if m, _ := inv.Has("gemini-3.8-flash-high"); strings.Join(m.Variants, ",") != "high" {
		t.Errorf("%+v", m)
	}
	if inv = one(t, fake(t, "antigravity", "notab", "1.3.2"), "antigravity"); inv.Status != InvFailed {
		t.Errorf("a row without a tab is an invalid schema: %+v", inv)
	}
}

func TestOpencodeAdapterReadsThroughAPipe(t *testing.T) {
	for _, h := range []string{"opencode", "opencode2"} {
		inv := one(t, fake(t, h, "ok", "opencode v2.0.20"), h)
		if inv.Status != InvComplete || strings.Join(ids(inv), ",") != "ollama-cloud/deepseek-v4-flash:0731,opencode-go/glm-5.3-flash,openrouter/google/gemini-3.5-flash" {
			t.Fatalf("%s: %+v", h, inv)
		}
		if m, _ := inv.Has("opencode-go/glm-5.3-flash"); m.Enabled != nil {
			t.Error("a catalog is not proof the model is enabled for this account")
		}
	}
	if inv := one(t, fake(t, "opencode", "nopath", "opencode v2.0.20"), "opencode"); inv.Status != InvFailed {
		t.Errorf("%+v", inv)
	}
	// OpenRouter's moving aliases are real catalog IDs: openrouter/~anthropic/claude-opus-latest
	if !validID("openrouter/~anthropic/claude-opus-latest") || validID("~x") || validID("a b") || validID("a;b") || validID("$(x)/y") || validID(strings.Repeat("a", 201)) {
		t.Error("model ID validation")
	}
}

func TestOpencodeCatalogMustSettle(t *testing.T) {
	opencodeSettle = time.Millisecond
	t.Cleanup(func() { opencodeSettle = 300 * time.Millisecond })
	for scenario, want := range map[string]struct {
		status InvStatus
		models string
	}{
		"ok":    {InvComplete, "ollama-cloud/deepseek-v4-flash:0731,opencode-go/glm-5.3-flash,openrouter/google/gemini-3.5-flash"},
		"cold":  {InvComplete, "a/one,a/two"},             // the empty first answer of a new location is not a catalog
		"moves": {InvComplete, "a/one,a/two"},             // a changing list is accepted only after it agrees with itself
		"flap":  {InvPartial, "a/m0,a/m1,a/m2,a/m3,a/m4"}, // never settles: partial, so the harness is excluded
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("REIN_FAKE_COUNTER", filepath.Join(t.TempDir(), "n"))
			inv := one(t, fake(t, "opencode", scenario, "opencode v2.0.20"), "opencode")
			got := ids(inv)
			slices.Sort(got)
			if inv.Status != want.status || strings.Join(got, ",") != want.models {
				t.Fatalf("%+v", inv)
			}
		})
	}
	// a harness that only ever answers with nothing is not evidence
	t.Setenv("REIN_FAKE_COUNTER", filepath.Join(t.TempDir(), "n"))
	if inv := one(t, fake(t, "opencode", "nopath", "opencode v2.0.20"), "opencode"); inv.Status != InvFailed {
		t.Errorf("%+v", inv)
	}
	// OpenRouter's moving aliases are real catalog IDs: openrouter/~anthropic/claude-opus-latest
	if !validID("openrouter/~anthropic/claude-opus-latest") || validID("~x") || validID("a b") || validID("a;b") || validID("$(x)/y") || validID(strings.Repeat("a", 201)) {
		t.Error("model ID validation")
	}
}

func TestOneShotFailures(t *testing.T) {
	if inv := one(t, fake(t, "kiro", "exit3", "kiro-cli 2.21.0"), "kiro"); !strings.Contains(inv.Reason, "exit status 3") {
		t.Errorf("%+v", inv)
	}
	start := time.Now()
	cfg := fake(t, "kiro", "hang", "kiro-cli 2.21.0")
	inv := Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: 500 * time.Millisecond, Harnesses: []string{"kiro"}})[0]
	if inv.Status != InvFailed || !strings.Contains(inv.Reason, "timeout") || time.Since(start) > 10*time.Second {
		t.Errorf("%+v after %v", inv, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	inv = Discover(ctx, cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: time.Minute, Harnesses: []string{"kiro"}})[0]
	if inv.Status != InvFailed || !strings.Contains(inv.Reason, "canceled") {
		t.Errorf("%+v", inv)
	}
	inv = Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Harnesses: []string{"kiro"}})[0]
	if inv.Status != InvFailed || !strings.Contains(inv.Reason, "timeout") {
		t.Errorf("a missing timeout must fail closed: %+v", inv)
	}
}

func TestUnsupportedAndMissingExecutablesSpawnNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	t.Setenv("REIN_FAKE_MARKER", marker)
	cfg := fake(t, "claude", "ok", "2.1.0")
	cfg.Discovery["mystery"] = cfg.Discovery["claude"]
	invs := Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: 5 * time.Second, Harnesses: []string{"claude", "mystery"}})
	for _, inv := range invs {
		if inv.Status != InvUnsupported || inv.Freshness != FreshnessUnknown || len(inv.Models) != 0 || inv.Reason == "" {
			t.Errorf("%+v", inv)
		}
	}
	if !strings.Contains(invs[0].Reason, "Claude plan entitlement") {
		t.Errorf("claude must say why: %+v", invs[0])
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an unsupported harness must not start any process")
	}
	missing := &Config{Discovery: map[string]DiscoverySpec{"codex": {Command: []string{"/definitely/not/here/codex"}}}}
	if inv := one(t, missing, "codex"); inv.Status != InvFailed || inv.Reason != "executable not found" {
		t.Errorf("%+v", inv)
	}
}

func TestDefaultsAndOrdering(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{"a": {Agent: "kiro"}, "b": {Agent: "claude"}, "c": {Agent: "kiro"}, "d": {Agent: "codex"}}}
	t.Setenv("PATH", t.TempDir()) // none of the CLIs can be found
	invs := Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: time.Second})
	var got []string
	for _, i := range invs {
		got = append(got, i.Harness+":"+string(i.Status))
	}
	if strings.Join(got, ",") != "claude:unsupported,codex:failed,kiro:failed" {
		t.Errorf("one inventory per distinct harness, sorted: %v", got)
	}
	if len(Discover(context.Background(), nil, DiscoverOptions{Timeout: time.Second})) != 0 {
		t.Error("nil config, nothing requested")
	}
}

func TestNoCredentialsOrOutputLeakAndNoCache(t *testing.T) {
	inv := one(t, fake(t, "kiro", "ok", "kiro-cli 2.21.0"), "kiro")
	if inv.Status != InvComplete {
		t.Fatal(inv)
	}
	all := []Inventory{inv, one(t, fake(t, "codex", "ratefail", "codex-cli 0.162.0"), "codex"), one(t, fake(t, "codex", "rpcerror", "codex-cli 0.162.0"), "codex"),
		one(t, fake(t, "antigravity", "ok", "1.3.2"), "antigravity"), one(t, fake(t, "codex", "ok", "codex-cli 0.162.0"), "codex")}
	b, _ := json.Marshal(all)
	for _, leak := range []string{secret, "acct-", "/home/x/.codex", "Fetching available models"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("inventory leaks %q: %s", leak, b)
		}
	}
	// no cache: the same config answers differently the second time, and a failure carries nothing from the first success
	first := one(t, fake(t, "kiro", "ok", "kiro-cli 2.21.0"), "kiro")
	second := one(t, fake(t, "kiro", "notjson", "kiro-cli 2.21.0"), "kiro")
	if first.Status != InvComplete || second.Status != InvFailed || len(second.Models) != 0 || second.SourceHash != "" {
		t.Errorf("a failed refresh must not reuse the previous snapshot: %+v", second)
	}
}

func TestHashInventoriesIsStableAndOrderIndependent(t *testing.T) {
	a := Inventory{Harness: "a", Status: InvComplete, Models: []ModelEntry{{ID: "x"}}}
	b := Inventory{Harness: "b", Status: InvFailed, Reason: "r"}
	if HashInventories([]Inventory{a, b}) != HashInventories([]Inventory{b, a}) || HashInventories([]Inventory{a}) == HashInventories([]Inventory{a, b}) {
		t.Error("hash must ignore order and bind content")
	}
	a2 := a
	a2.Models = []ModelEntry{{ID: "y"}}
	if HashInventories([]Inventory{a}) == HashInventories([]Inventory{a2}) {
		t.Error("a catalog change must change the hash")
	}
}

func TestTwoHarnessesAtOnce(t *testing.T) {
	cfg := fake(t, "kiro", "ok", "kiro-cli 2.21.0")
	cfg.Discovery["codex"] = DiscoverySpec{Command: []string{os.Args[0], "-test.run=^TestDiscoverHelper$", "--"}}
	t.Setenv("REIN_FAKE_VERSION", "kiro-cli 2.21.0") // the helper answers both with the kiro banner; codex must fail on the mismatch of major/min
	invs := Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: 20 * time.Second, Harnesses: []string{"kiro", "codex"}})
	if len(invs) != 2 || invs[0].Harness != "codex" || invs[1].Harness != "kiro" || invs[1].Status != InvComplete || invs[0].Status != InvFailed {
		t.Errorf("each harness is judged on its own: %+v", invs)
	}
}

func TestLiveHarnessesOptIn(t *testing.T) {
	if os.Getenv("REIN_DISCOVER_LIVE") == "" {
		t.Skip("set REIN_DISCOVER_LIVE=1 to query the harness CLIs installed on this machine (no inference)")
	}
	cfg := &Config{Providers: map[string]Provider{}}
	for _, h := range []string{"codex", "kiro", "antigravity", "opencode", "claude"} {
		cfg.Providers[h] = Provider{Agent: h}
	}
	for _, inv := range Discover(context.Background(), cfg, DiscoverOptions{Timeout: 60 * time.Second}) {
		t.Logf("%-12s %-11s %-14s v=%-8s models=%-3d limits=%d scope=%s reason=%q", inv.Harness, inv.Status, inv.Freshness, inv.Version, len(inv.Models), len(inv.Limits), inv.Scope, inv.Reason)
		if len(inv.Limits) > 0 {
			b, _ := json.Marshal(inv.Limits)
			t.Logf("  limits: %s", b)
		}
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		one(t, fake(t, "codex", "ok", "codex-cli 0.162.0"), "codex")
		one(t, fake(t, "kiro", "ok", "kiro-cli 2.21.0"), "kiro")
		Discover(context.Background(), fake(t, "codex", "hang", "codex-cli 0.162.0"), DiscoverOptions{Dir: t.TempDir(), Timeout: 700 * time.Millisecond, Harnesses: []string{"codex"}})
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+1 {
		t.Fatalf("goroutines leaked: %d before, %d after", before, got)
	}
}
