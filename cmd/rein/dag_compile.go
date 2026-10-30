package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/dagspec"
	"github.com/voravitl/rein/internal/glob"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/supervisor"
)

func cmdDagCompile(args []string) int {
	fs := flag.NewFlagSet("dag compile", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	specPath := fs.String("spec", "", "rein_dag_spec/v1 JSON file")
	outDir := fs.String("out", "", "output directory for compiled plan, contracts, and worktrees")
	configPath := fs.String("config", "", "Crux providers configuration file")
	profilePath := fs.String("profile", "", "owner profile file (must declare sensitive_paths)")
	apply := fs.Bool("apply", false, "materialize worktrees, contracts, and install hooks, then validate in-process")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *specPath == "" || *outDir == "" {
		fmt.Fprintln(os.Stderr, dagUsage)
		return 2
	}

	cleanOut := filepath.Clean(*outDir)
	if !filepath.IsAbs(cleanOut) || cleanOut != *outDir {
		fmt.Fprintf(os.Stderr, "[dag compile] out directory %q must be an absolute, clean path\n", *outDir)
		return 2
	}

	specData, err := readDagSpec(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dag compile] cannot read spec: %v\n", err)
		return 2
	}

	spec, lintRes := dagspec.Lint(specData)
	if !lintRes.Valid {
		fmt.Fprintf(os.Stderr, "[dag compile] spec lint failed with %d error(s):\n", len(lintRes.Errors))
		for _, f := range lintRes.Errors {
			fmt.Fprintln(os.Stderr, formatDagFinding("error", f))
		}
		return 1
	}

	// Load and validate Crux providers configuration
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("CRUX_CONFIG")
	}
	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "[dag compile] missing required --config or CRUX_CONFIG")
		return 2
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dag compile] cannot read config: %v\n", err)
		return 2
	}
	cfg, err := providers.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dag compile] invalid providers config: %v\n", err)
		return 1
	}
	h := sha256.Sum256(cfgBytes)
	cfgDigest := hex.EncodeToString(h[:])

	// Load and validate owner profile
	profPath := *profilePath
	if profPath == "" {
		profPath = os.Getenv("REIN_PROFILE")
	}
	if profPath == "" {
		fmt.Fprintln(os.Stderr, "[dag compile] missing required --profile or REIN_PROFILE")
		return 2
	}
	ownerProf, err := contract.LoadProfile(profPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dag compile] invalid owner profile: %v\n", err)
		return 1
	}
	if len(ownerProf.SensitivePaths) == 0 {
		fmt.Fprintln(os.Stderr, "[dag compile] owner profile must declare sensitive_paths")
		return 1
	}

	compiled, err := compileDAG(spec, cfg, cfgPath, cfgDigest, ownerProf, cleanOut)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dag compile] compile error: %v\n", err)
		return 1
	}

	if *apply {
		if err := applyCompiledDAG(compiled); err != nil {
			fmt.Fprintf(os.Stderr, "[dag compile] apply failed: %v\n", err)
			return 1
		}
		fmt.Printf("compiled and applied: plan written to %s (validated in-process)\n", filepath.Join(cleanOut, "plan.json"))
	} else {
		if err := os.MkdirAll(cleanOut, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "[dag compile] cannot create out dir: %v\n", err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(cleanOut, "plan.json"), compiled.PlanJSON, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[dag compile] cannot write plan.json: %v\n", err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(cleanOut, "plan.md"), compiled.PlanMD, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[dag compile] cannot write plan.md: %v\n", err)
			return 1
		}
		fmt.Printf("compiled: plan written to %s (use --apply to materialize worktrees and contracts)\n", filepath.Join(cleanOut, "plan.json"))
	}

	return 0
}

type compiledPlan struct {
	Spec         *dagspec.Spec
	OutDir       string
	Plan         supervisor.Plan
	PlanJSON     []byte
	PlanMD       []byte
	Contracts    map[string]*contract.Contract
	Worktrees    map[string]string
	Harnesses    map[string][]string // task -> list of vendors for hooks.Install
	TaskProfiles map[string]*routing.TaskProfile
	ConfigPath   string
	ConfigDigest string
}

func compileDAG(spec *dagspec.Spec, cfg *providers.Config, cfgPath, cfgDigest string, ownerProf *contract.Profile, outDir string) (*compiledPlan, error) {
	// Index directory check
	indexDir := contract.IndexDir()
	if pathsOverlap(outDir, indexDir) {
		return nil, errors.New("out directory must not overlap contract index directory")
	}

	poolsMap := map[string]bool{}
	contracts := make(map[string]*contract.Contract)
	worktrees := make(map[string]string)
	harnesses := make(map[string][]string)
	taskProfiles := make(map[string]*routing.TaskProfile)

	tasks := make([]supervisor.Task, 0, len(spec.Nodes))

	for _, n := range spec.Nodes {
		taskName := spec.Run + "-" + n.ID
		wtPath := filepath.Join(outDir, "worktrees", taskName)
		reportPath := filepath.Join(outDir, "reports", taskName+".md")

		worktrees[taskName] = wtPath

		deps := make([]string, len(n.DependsOn))
		for i, d := range n.DependsOn {
			deps[i] = spec.Run + "-" + d
		}
		sort.Strings(deps)

		// Base contract
		ct := &contract.Contract{
			Name:       taskName,
			Worktree:   wtPath,
			Scope:      []string{taskName},
			ReportPath: reportPath,
			Deny:       append([]string(nil), contract.AlwaysDeny...),
			Profile:    *ownerProf,
		}

		t := supervisor.Task{
			Task:        taskName,
			Kind:        n.Kind,
			Base:        spec.Base,
			MaxAttempts: 1,
			DependsOn:   deps,
			Verify:      n.Verify,
		}

		// Convert Inputs and Outputs
		if len(n.Inputs) > 0 {
			t.Inputs = make([]supervisor.ArtifactInput, len(n.Inputs))
			for i, inp := range n.Inputs {
				t.Inputs[i] = supervisor.ArtifactInput{
					Producer: spec.Run + "-" + inp.Producer,
					Name:     inp.Name,
					As:       inp.As,
				}
			}
		}
		if len(n.Outputs) > 0 {
			t.Outputs = make([]supervisor.ArtifactOutput, len(n.Outputs))
			for i, out := range n.Outputs {
				t.Outputs[i] = supervisor.ArtifactOutput{
					Name:     out.Name,
					Path:     out.Path,
					MaxBytes: out.MaxBytes,
				}
			}
		}

		switch n.Kind {
		case "worker":
			// Worker node routing fields (Decision D1)
			if n.KindProfile == "" {
				return nil, fmt.Errorf("worker node %q requires kind_profile (e.g. backend, frontend, fullstack, docs, mechanical)", n.ID)
			}
			if len(n.RequiredCapabilities) == 0 {
				return nil, fmt.Errorf("worker node %q requires required_capabilities", n.ID)
			}
			if n.ExpectedContextTokens <= 0 {
				return nil, fmt.Errorf("worker node %q requires expected_context_tokens > 0", n.ID)
			}
			if len(n.Rationale) == 0 {
				return nil, fmt.Errorf("worker node %q requires at least one rationale entry", n.ID)
			}

			// Validate rationale paths match write globs
			plannedFiles := make([]string, 0, len(n.Rationale))
			rationales := make([]routing.Rationale, 0, len(n.Rationale))
			for _, r := range n.Rationale {
				cleanP := filepath.ToSlash(filepath.Clean(r.Path))
				if strings.HasPrefix(cleanP, "../") || filepath.IsAbs(cleanP) || cleanP == "." {
					return nil, fmt.Errorf("worker node %q rationale path %q must be a clean relative path", n.ID, r.Path)
				}
				if !glob.Match(cleanP, n.Write) {
					return nil, fmt.Errorf("worker node %q rationale path %q is not covered by write globs %v", n.ID, r.Path, n.Write)
				}
				plannedFiles = append(plannedFiles, cleanP)
				rationales = append(rationales, routing.Rationale{Path: cleanP, Reason: r.Reason})
			}
			sort.Strings(plannedFiles)

			ct.Allow = append([]string(nil), n.Write...)

			if len(n.Providers) == 0 {
				return nil, fmt.Errorf("worker node %q requires at least one provider in providers", n.ID)
			}

			promptText := renderDAGPrompt(n.Prompt)
			launches := make(map[string][]string)
			var vendors []string
			seenVendor := map[string]bool{}

			for _, pName := range n.Providers {
				prov, ok := cfg.Providers[pName]
				if !ok {
					return nil, fmt.Errorf("worker node %q provider %q not found in config", n.ID, pName)
				}
				if prov.Launch != "shell" {
					return nil, fmt.Errorf("worker node %q provider %q launch mode is %q, requires \"shell\"", n.ID, pName, prov.Launch)
				}
				pool, err := routing.QuotaPool(prov)
				if err != nil {
					return nil, fmt.Errorf("worker node %q provider %q: %w", n.ID, pName, err)
				}
				poolsMap[pool] = true

				argv, err := buildDAGLaunchArgv(prov, promptText)
				if err != nil {
					return nil, fmt.Errorf("worker node %q provider %q: %w", n.ID, pName, err)
				}
				if _, err := graphPromptSlot(argv); err != nil {
					return nil, fmt.Errorf("worker node %q provider %q invalid launch argv: %w", n.ID, pName, err)
				}
				launches[pName] = argv

				v := harnessVendor(prov.Agent)
				if !seenVendor[v] {
					seenVendor[v] = true
					vendors = append(vendors, v)
				}
			}
			sort.Strings(vendors)
			harnesses[taskName] = vendors

			t.ProcessPolicy = "non_detaching"
			t.ConfigPath = cfgPath
			t.ConfigDigest = cfgDigest
			t.Launches = launches
			if n.Calibration != nil {
				if err := checkDAGCalibration(n, cfg, ownerProf); err != nil {
					return nil, err
				}
				t.Calibration = &supervisor.TaskCalibration{Amount: n.Calibration.Amount}
			}

			// Initial TaskProfile (contract_hash will be bound after hooks)
			tp := &routing.TaskProfile{
				Phase:                "worker",
				Kind:                 n.KindProfile,
				RequiredCapabilities: append([]string(nil), n.RequiredCapabilities...),
				ExpectedContext:      n.ExpectedContextTokens,
				PlannedFiles:         plannedFiles,
				Rationale:            rationales,
			}
			taskProfiles[taskName] = tp

		case "action":
			// Action node contract allow = declared output paths (Decision D2)
			if len(n.Outputs) == 0 {
				return nil, fmt.Errorf("action node %q requires at least one output to form contract allow list", n.ID)
			}
			allowPaths := make([]string, len(n.Outputs))
			var totalBytes int64
			for i, o := range n.Outputs {
				allowPaths[i] = o.Path
				totalBytes += o.MaxBytes
			}
			ct.Allow = allowPaths

			if n.Action == nil {
				return nil, fmt.Errorf("action node %q missing action specification", n.ID)
			}
			if len(n.Action.Argv) == 0 || !filepath.IsAbs(n.Action.Argv[0]) {
				return nil, fmt.Errorf("action node %q argv[0] must be an absolute executable path", n.ID)
			}
			for i, v := range n.Verify {
				if len(v) == 0 || !filepath.IsAbs(v[0]) {
					return nil, fmt.Errorf("action node %q verify[%d][0] must be an absolute executable path", n.ID, i)
				}
			}

			maxFiles := max(len(n.Outputs), 10)
			if maxFiles > 1000 {
				maxFiles = 1000
			}
			maxPerFile := int64(1 << 20) // 1 MiB per file bound in supervisor
			maxTotal := int64(64 << 20)  // 64 MiB total bound in supervisor
			if totalBytes > 0 && totalBytes*2 < maxTotal {
				maxTotal = totalBytes * 2
			}

			t.Action = &supervisor.ActionSpec{
				Argv:                n.Action.Argv,
				TimeoutMS:           n.Action.TimeoutMS,
				MaxOutputBytes:      maxPerFile,
				MaxOutputFiles:      maxFiles,
				MaxOutputTotalBytes: maxTotal,
				Sandbox: supervisor.ActionSandbox{
					Backend: "docker",
					Image:   n.Action.Image,
				},
			}
		}

		contracts[taskName] = ct
		t.ContractHash = routing.ContractHash(ct)
		tasks = append(tasks, t)
	}

	pools := make([]string, 0, len(poolsMap))
	for p := range poolsMap {
		pools = append(pools, p)
	}
	sort.Strings(pools)

	plan := supervisor.Plan{
		Schema:         3,
		Run:            spec.Run,
		Repo:           spec.Repo,
		Terminal:       "clean",
		Source:         spec.Base,
		Version:        "v1",
		MaxChecks:      1000,
		TimeoutMS:      3600000,
		PollIntervalMS: 1000,
		Pools:          pools,
		Tasks:          tasks,
		Execution: &supervisor.ExecutionPolicy{
			MaxParallel:   spec.MaxParallel,
			FailurePolicy: "isolate_known",
		},
	}

	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cannot encode plan: %w", err)
	}

	planMD := renderPlanMD(plan, spec, outDir)

	return &compiledPlan{
		Spec:         spec,
		OutDir:       outDir,
		Plan:         plan,
		PlanJSON:     planJSON,
		PlanMD:       planMD,
		Contracts:    contracts,
		Worktrees:    worktrees,
		Harnesses:    harnesses,
		TaskProfiles: taskProfiles,
		ConfigPath:   cfgPath,
		ConfigDigest: cfgDigest,
	}, nil
}

func applyCompiledDAG(c *compiledPlan) error {
	// Create output directories
	if err := os.MkdirAll(c.OutDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(c.OutDir, "worktrees"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(c.OutDir, "reports"), 0755); err != nil {
		return err
	}

	reinBin, _, err := hooks.BinaryPath()
	if err != nil {
		return fmt.Errorf("cannot locate rein binary for hooks: %w", err)
	}

	// 1. Create worktrees for each task if they don't exist
	for taskName, wt := range c.Worktrees {
		if fi, err := os.Stat(wt); err == nil && fi.IsDir() {
			continue // worktree already exists
		}
		cmd := exec.Command("git", "-C", c.Spec.Repo, "worktree", "add", "--detach", wt, c.Spec.Base)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create worktree %s at %s: %v (%s)", taskName, wt, err, strings.TrimSpace(string(out)))
		}
	}

	// 2. Save contracts, install hooks on workers, re-compute contract hashes
	for i := range c.Plan.Tasks {
		t := &c.Plan.Tasks[i]
		ct := c.Contracts[t.Task]

		if t.Kind == "worker" {
			// Save contract first so hooks.Install finds it
			if _, err := ct.Save(); err != nil {
				return fmt.Errorf("failed to save contract %s: %w", t.Task, err)
			}

			vendors := c.Harnesses[t.Task]
			if len(vendors) > 0 {
				_, _, err := hooks.Install(ct, reinBin, vendors)
				if err != nil {
					return fmt.Errorf("failed to install hooks for %s: %w", t.Task, err)
				}
			}

			// Re-load contract to ensure exact disk representation
			reloaded, err := contract.Load(t.Task)
			if err != nil {
				return fmt.Errorf("failed to reload contract %s after hooks: %w", t.Task, err)
			}
			ct = reloaded
			c.Contracts[t.Task] = ct

			t.ContractHash = routing.ContractHash(ct)

			// Update TaskProfile contract hash and bind to task.Profile
			tp := c.TaskProfiles[t.Task]
			tp.ContractHash = t.ContractHash
			profBytes, err := json.Marshal(tp)
			if err != nil {
				return fmt.Errorf("failed to encode profile for %s: %w", t.Task, err)
			}
			t.Profile = profBytes

		} else if t.Kind == "action" {
			if _, err := ct.Save(); err != nil {
				return fmt.Errorf("failed to save action contract %s: %w", t.Task, err)
			}
			reloaded, err := contract.Load(t.Task)
			if err != nil {
				return fmt.Errorf("failed to reload action contract %s: %w", t.Task, err)
			}
			ct = reloaded
			c.Contracts[t.Task] = ct
			t.ContractHash = routing.ContractHash(ct)
		}
	}

	// 3. Re-encode plan.json and plan.md with final contract hashes
	planJSON, err := json.MarshalIndent(c.Plan, "", "  ")
	if err != nil {
		return err
	}
	c.PlanJSON = planJSON
	c.PlanMD = renderPlanMD(c.Plan, c.Spec, c.OutDir)

	planPath := filepath.Join(c.OutDir, "plan.json")
	if err := os.WriteFile(planPath, c.PlanJSON, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.OutDir, "plan.md"), c.PlanMD, 0644); err != nil {
		return err
	}

	// 4. In-process validation: exact execution path
	if err := validateLocalDAGPlan(c.Plan); err != nil {
		return fmt.Errorf("in-process validateLocalDAGPlan failed: %w", err)
	}

	return nil
}

func renderDAGPrompt(p *dagspec.Prompt) string {
	var b strings.Builder
	b.WriteString("TASK:\n" + p.Task + "\n\n")
	b.WriteString("DELIVERABLE:\n" + p.Deliverable + "\n\n")
	b.WriteString("SCOPE:\n" + p.Scope + "\n\n")
	b.WriteString("VERIFY:\n" + p.Verify + "\n\n")
	b.WriteString("STOP WHEN:\n" + p.StopWhen + "\n")
	return b.String()
}

func buildDAGLaunchArgv(prov providers.Provider, promptText string) ([]string, error) {
	switch prov.Agent {
	case "claude":
		argv := []string{"claude", "-p", "--permission-mode", "acceptEdits"}
		if prov.Model != "" {
			argv = append(argv, "--model", prov.Model)
		}
		if prov.Effort != "" {
			argv = append(argv, "--effort", prov.Effort)
		}
		argv = append(argv, "--", promptText)
		return argv, nil
	case "agy", "antigravity":
		argv := []string{"agy", "-p", "--mode", "accept-edits"}
		if prov.Model != "" {
			argv = append(argv, "--model", prov.Model)
		}
		if prov.Effort != "" {
			argv = append(argv, "--effort", prov.Effort)
		}
		argv = append(argv, "--", promptText)
		return argv, nil
	case "codex":
		argv := []string{"codex", "exec", "--sandbox", "workspace-write"}
		if prov.Model != "" {
			argv = append(argv, "-m", prov.Model)
		}
		argv = append(argv, promptText)
		return argv, nil
	case "opencode", "opencode2":
		name := prov.Agent
		argv := []string{name, "run", "--standalone"}
		if prov.Model != "" {
			argv = append(argv, "-m", prov.Model)
		}
		argv = append(argv, promptText)
		return argv, nil
	case "hermes":
		argv := []string{"hermes", "--cli", "chat", "--oneshot"}
		if prov.Model != "" {
			argv = append(argv, "--model", prov.Model)
		}
		argv = append(argv, "--query", promptText)
		return argv, nil
	default:
		return nil, fmt.Errorf("unsupported agent %q for DAG launch", prov.Agent)
	}
}

// checkDAGCalibration refuses at compile time a calibration worker the owner's
// profile could never fund, so the failure is visible before approval. Runtime
// (routing.PrepareCalibration) re-checks every gate against current state.
func checkDAGCalibration(n dagspec.Node, cfg *providers.Config, owner *contract.Profile) error {
	sel := owner.EffectiveSelection()
	if sel == nil || sel.Calibration == nil {
		return fmt.Errorf("worker node %q uses calibration but the owner profile has no selection.calibration authorization and caps", n.ID)
	}
	if problems := sel.Problems(); len(problems) > 0 {
		return fmt.Errorf("worker node %q uses calibration but the owner selection policy is invalid: %s", n.ID, problems[0])
	}
	prov := cfg.Providers[n.Providers[0]]
	if prov.Billing == nil {
		return fmt.Errorf("worker node %q calibration provider %q declares no billing", n.ID, n.Providers[0])
	}
	limit, ok := sel.Calibration.PoolCaps[prov.Billing.Pool]
	if !ok || n.Calibration.Amount > limit {
		return fmt.Errorf("worker node %q calibration amount %g exceeds the owner's calibration cap for pool %q", n.ID, n.Calibration.Amount, prov.Billing.Pool)
	}
	return nil
}

func harnessVendor(agent string) string {
	switch agent {
	case "claude":
		return "claude"
	case "agy", "antigravity":
		return "agy"
	case "codex":
		return "codex"
	case "opencode", "opencode2":
		return "opencode"
	case "hermes":
		return "hermes"
	default:
		return agent
	}
}

// planShellQuote quotes s for a POSIX shell so the owner commands in plan.md
// stay correct for paths and names containing spaces or quotes.
func planShellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:,@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderPlanMD writes the human summary and the exact owner commands. The plan
// path is the compiled --out directory: plan.json is never written into the repo.
func renderPlanMD(p supervisor.Plan, s *dagspec.Spec, outDir string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# DAG Execution Plan: %s\n\n", p.Run)
	fmt.Fprintf(&b, "- **Repository**: `%s`\n", p.Repo)
	fmt.Fprintf(&b, "- **Base Commit**: `%s`\n", p.Source)
	fmt.Fprintf(&b, "- **Goal**: %s\n", s.Goal)
	fmt.Fprintf(&b, "- **Max Parallel**: %d\n", p.Execution.MaxParallel)
	fmt.Fprintf(&b, "- **Quota Pools**: %s\n\n", strings.Join(p.Pools, ", "))

	fmt.Fprintln(&b, "## Tasks")
	fmt.Fprintln(&b, "| Task | Kind | Depends On | Details |")
	fmt.Fprintln(&b, "|---|---|---|---|")
	for _, t := range p.Tasks {
		deps := strings.Join(t.DependsOn, ", ")
		if deps == "" {
			deps = "*(none)*"
		}
		details := ""
		if t.Kind == "worker" {
			var provs []string
			for prov := range t.Launches {
				provs = append(provs, prov)
			}
			sort.Strings(provs)
			details = "Providers: " + strings.Join(provs, ", ")
			if t.Calibration != nil {
				details += fmt.Sprintf(" (calibration, unscored, worst case %g)", t.Calibration.Amount)
			}
		} else if t.Kind == "action" && t.Action != nil {
			details = fmt.Sprintf("Image: `%s`", t.Action.Sandbox.Image)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", t.Task, t.Kind, deps, details)
	}

	plan := planShellQuote(filepath.Join(outDir, "plan.json"))
	repo := planShellQuote(p.Repo)
	fmt.Fprintln(&b, "\n## Human Approval & Supervisor Execution")
	fmt.Fprintln(&b, "Run these in your own interactive terminal, not from an agent session: `run start --supervisor-plan` and `run allow` refuse agent sessions and non-TTY input.")
	fmt.Fprintln(&b, "```sh")
	// Placeholders are shell variables with working defaults: a literal <name>
	// pasted into a shell is parsed as a redirection.
	fmt.Fprintln(&b, "# 0. Your owner session id and the new integration branch (edit if you like)")
	fmt.Fprintln(&b, `OWNER_SESSION="owner-$(id -un)-$$"`)
	fmt.Fprintf(&b, "INTEGRATION_BRANCH=%s\n\n", planShellQuote("dag/"+p.Run))
	fmt.Fprintf(&b, "# 1. Validate the compiled plan\nrein supervisor validate --plan %s\n\n", plan)
	fmt.Fprintf(&b, "# 2. Start the supervised run (owner gate) with your session id and shell pid\nrein run start %s --run %s --session \"$OWNER_SESSION\" --pid $$ --supervisor-plan %s\n\n", repo, planShellQuote(p.Run), plan)
	fmt.Fprintln(&b, "# 3. Authorize each task of this plan")
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "rein run allow %s --task %s --reason %s\n", repo, planShellQuote(t.Task), planShellQuote("approved DAG plan "+p.Run))
	}
	fmt.Fprintf(&b, "\n# 4. Execute the DAG under supervision\nrein supervisor run --plan %s\n\n", plan)
	fmt.Fprintf(&b, "# 5. Observe progress (single-shot JSON for agents; add --watch in a terminal)\nrein supervisor status --plan %s --json\n\n", plan)
	fmt.Fprintf(&b, "# 6. After every task is done, integrate into a new branch\nrein dag integrate --plan %s --branch \"$INTEGRATION_BRANCH\"\n", plan)
	fmt.Fprintln(&b, "```")

	return b.Bytes()
}
