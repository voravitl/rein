package dagspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/supervisor"
)

// OwnerProvider describes an authorized worker provider.
type OwnerProvider struct {
	Launch []string `json:"launch"`
}

// OwnerConfig defines the execution environment outside the worktree.
type OwnerConfig struct {
	Providers  map[string]OwnerProvider `json:"providers"`
	Profile    json.RawMessage          `json:"profile"`
	ConfigPath string                   `json:"config_path"`
}

// Compiled holds the result of compiling a DAG spec.
type Compiled struct {
	Spec        *Spec
	Owner       OwnerConfig
	OwnerDigest string
	ConfigPath  string
	OutDir      string
	Plan        supervisor.Plan
	PlanJSON    []byte
	PlanMD      []byte
	Contracts   map[string]*contract.Contract
}

// Compile validates a graph spec with owner config and builds a deterministic schema-3 plan.
func Compile(specData, ownerData []byte, outDir, configPath string) (*Compiled, error) {
	if !filepath.IsAbs(outDir) || filepath.Clean(outDir) != outDir {
		return nil, fmt.Errorf("out directory %q must be an absolute, clean path", outDir)
	}

	spec, lintRes := Lint(specData)
	if !lintRes.Valid {
		return nil, fmt.Errorf("spec lint failed: %s", lintRes.Errors[0].Message)
	}

	var owner OwnerConfig
	if err := json.Unmarshal(ownerData, &owner); err != nil {
		return nil, fmt.Errorf("owner config invalid JSON: %w", err)
	}
	if len(owner.Providers) == 0 {
		return nil, errors.New("owner config requires at least one provider")
	}

	if configPath == "" {
		configPath = owner.ConfigPath
	}
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return nil, fmt.Errorf("config path %q must be an absolute, clean path", configPath)
	}

	// Validate node providers against owner providers
	for _, n := range spec.Nodes {
		for _, p := range n.Providers {
			if _, ok := owner.Providers[p]; !ok {
				return nil, fmt.Errorf("node %s requests unknown provider %q not in owner config", n.ID, p)
			}
		}
	}

	ownerDigestBytes := sha256.Sum256(ownerData)
	ownerDigest := hex.EncodeToString(ownerDigestBytes[:])

	order := topologicalSort(spec.Nodes)
	if order == nil {
		return nil, errors.New("cannot topologically sort nodes (dependency cycle or missing dependency)")
	}

	plan := supervisor.Plan{
		Schema:         3,
		Run:            spec.Run,
		Repo:           spec.Repo,
		Terminal:       "local",
		Source:         "rein-dag-spec",
		Version:        "1",
		MaxChecks:      4,
		TimeoutMS:      600000,
		PollIntervalMS: 1000,
		Execution: &supervisor.ExecutionPolicy{
			MaxParallel:   spec.MaxParallel,
			FailurePolicy: "isolate_known",
		},
	}

	contracts := make(map[string]*contract.Contract)

	for _, idx := range order {
		n := spec.Nodes[idx]
		taskName := spec.Run + "-" + n.ID

		deps := make([]string, 0, len(n.DependsOn))
		for _, d := range n.DependsOn {
			deps = append(deps, spec.Run+"-"+d)
		}
		sort.Strings(deps)

		// Create deterministic contract for this node
		ct := &contract.Contract{
			Name:       taskName,
			Worktree:   filepath.Join(outDir, "worktrees", taskName),
			Allow:      append([]string(nil), n.Write...),
			Deny:       append([]string(nil), contract.AlwaysDeny...),
			Scope:      []string{taskName},
			ReportPath: filepath.Join(outDir, "reports", taskName+".md"),
		}
		contracts[taskName] = ct

		t := supervisor.Task{
			Task:         taskName,
			Kind:         n.Kind,
			Base:         spec.Base,
			ContractHash: routing.ContractHash(ct),
			MaxAttempts:  1,
			DependsOn:    deps,
			Verify:       n.Verify,
		}

		if n.Kind == "worker" {
			t.ProcessPolicy = "non_detaching"
			t.Profile = append(json.RawMessage(nil), owner.Profile...)
			t.ConfigPath = configPath
			t.ConfigDigest = ownerDigest

			providers := n.Providers
			if len(providers) == 0 {
				for p := range owner.Providers {
					providers = append(providers, p)
				}
				sort.Strings(providers)
			}
			t.Launches = make(map[string][]string)
			for _, p := range providers {
				t.Launches[p] = append([]string(nil), owner.Providers[p].Launch...)
			}
		} else if n.Kind == "action" {
			a := n.Action
			t.Action = &supervisor.ActionSpec{
				Argv:                a.Argv,
				TimeoutMS:           a.TimeoutMS,
				MaxOutputBytes:      1 << 20,
				MaxOutputFiles:      100,
				MaxOutputTotalBytes: 64 << 20,
				Sandbox: supervisor.ActionSandbox{
					Backend: "docker",
					Image:   a.Image,
				},
			}
		}

		for _, in := range n.Inputs {
			t.Inputs = append(t.Inputs, supervisor.ArtifactInput{
				Producer: spec.Run + "-" + in.Producer,
				Name:     in.Name,
				As:       in.As,
			})
		}
		for _, out := range n.Outputs {
			t.Outputs = append(t.Outputs, supervisor.ArtifactOutput{
				Name:     out.Name,
				Path:     out.Path,
				MaxBytes: out.MaxBytes,
			})
		}

		plan.Tasks = append(plan.Tasks, t)
	}

	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal plan: %w", err)
	}
	planJSON = append(planJSON, '\n')

	planMD := renderPlanMarkdown(spec, plan, ownerDigest, configPath, outDir)

	return &Compiled{
		Spec:        spec,
		Owner:       owner,
		OwnerDigest: ownerDigest,
		ConfigPath:  configPath,
		OutDir:      outDir,
		Plan:        plan,
		PlanJSON:    planJSON,
		PlanMD:      []byte(planMD),
		Contracts:   contracts,
	}, nil
}

func topologicalSort(nodes []Node) []int {
	byID := make(map[string]int)
	for i, n := range nodes {
		byID[n.ID] = i
	}
	done := make(map[string]bool)
	var order []int

	for len(order) < len(nodes) {
		progressed := false
		for i, n := range nodes {
			if done[n.ID] {
				continue
			}
			ready := true
			for _, d := range n.DependsOn {
				if !done[d] {
					ready = false
					break
				}
			}
			if ready {
				done[n.ID] = true
				order = append(order, i)
				progressed = true
			}
		}
		if !progressed {
			return nil
		}
	}
	return order
}

func renderPlanMarkdown(s *Spec, p supervisor.Plan, ownerDigest, configPath, outDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Run: %s\n\n", s.Run)
	fmt.Fprintf(&b, "Goal: %s\n\n", s.Goal)
	fmt.Fprintf(&b, "- Config: `%s`\n", configPath)
	fmt.Fprintf(&b, "- Config Digest: `%s`\n\n", ownerDigest)

	fmt.Fprintln(&b, "## Graph by Layer")
	fmt.Fprintln(&b)
	layers := computeLayers(s)
	for layerIdx, nodeIDs := range layers {
		fmt.Fprintf(&b, "### Layer %d\n", layerIdx+1)
		for _, nid := range nodeIDs {
			fmt.Fprintf(&b, "- `%s`\n", nid)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "## Tasks")
	fmt.Fprintln(&b)
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "### %s (%s)\n\n", t.Task, t.Kind)
		if len(t.Launches) > 0 {
			fmt.Fprintln(&b, "**Launches:**")
			var providers []string
			for prov := range t.Launches {
				providers = append(providers, prov)
			}
			sort.Strings(providers)
			for _, prov := range providers {
				fmt.Fprintf(&b, "- `%s`: `%s`\n", prov, strings.Join(t.Launches[prov], " "))
			}
			fmt.Fprintln(&b)
		}
		if t.Action != nil {
			fmt.Fprintf(&b, "**Action:** `%s` (image: `%s`)\n\n", strings.Join(t.Action.Argv, " "), t.Action.Sandbox.Image)
		}
		fmt.Fprintf(&b, "- Contract Digest: `%s`\n", t.ContractHash)
		if t.ConfigDigest != "" {
			fmt.Fprintf(&b, "- Config Digest: `%s`\n", t.ConfigDigest)
		}
		if len(t.Verify) > 0 {
			fmt.Fprintln(&b, "- Verify:")
			for _, v := range t.Verify {
				fmt.Fprintf(&b, "  - `%s`\n", strings.Join(v, " "))
			}
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "## Owner Approval Commands")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "```sh\n")
	fmt.Fprintf(&b, "rein run start %s --supervisor-plan %s/plan.json\n", s.Repo, outDir)
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "rein run allow %s --task %s\n", s.Repo, t.Task)
	}
	fmt.Fprintf(&b, "```\n")

	return b.String()
}

func computeLayers(s *Spec) [][]string {
	depth := make(map[string]int)
	nodesByID := make(map[string]Node)
	for _, n := range s.Nodes {
		nodesByID[n.ID] = n
	}

	var getDepth func(id string) int
	getDepth = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		n, ok := nodesByID[id]
		if !ok || len(n.DependsOn) == 0 {
			depth[id] = 0
			return 0
		}
		maxD := 0
		for _, dep := range n.DependsOn {
			d := getDepth(dep) + 1
			if d > maxD {
				maxD = d
			}
		}
		depth[id] = maxD
		return maxD
	}

	maxDepth := 0
	for _, n := range s.Nodes {
		d := getDepth(n.ID)
		if d > maxDepth {
			maxDepth = d
		}
	}

	layers := make([][]string, maxDepth+1)
	for _, n := range s.Nodes {
		d := depth[n.ID]
		layers[d] = append(layers[d], n.ID)
	}
	for i := range layers {
		sort.Strings(layers[i])
	}
	return layers
}

// WriteOutputs writes plan.json and plan.md to outDir.
// It refuses to overwrite differing existing files.
func (c *Compiled) WriteOutputs() error {
	if err := os.MkdirAll(c.OutDir, 0755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	files := map[string][]byte{
		"plan.json": c.PlanJSON,
		"plan.md":   c.PlanMD,
	}

	for name, content := range files {
		path := filepath.Join(c.OutDir, name)
		existing, err := os.ReadFile(path)
		if err == nil {
			if string(existing) != string(content) {
				return fmt.Errorf("refusing to overwrite existing differing file %s", path)
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
