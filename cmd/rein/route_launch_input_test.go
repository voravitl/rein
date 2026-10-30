package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/supervisor"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphPromptSlotCanonicalAndRejectAmbiguity(t *testing.T) {
	prompt := "ทำงาน\n'\" $() `date`"
	for _, argv := range [][]string{
		{"claude", "-p", prompt, "--model", "m"},
		{"codex", "exec", "-m", "m", prompt},
		{"agy", "--model", "m", "--effort", "high", "--print", prompt},
		{"agy", "--prompt", prompt, "--model", "m"},
		{"opencode2", "run", "--standalone", "-m", "m", prompt},
		// The worktree-scoped edit mode compile emits for print-mode workers.
		{"claude", "-p", "--permission-mode", "acceptEdits", "--model", "m", "--", prompt},
		{"codex", "exec", "--sandbox", "workspace-write", "-m", "m", prompt},
		{"agy", "--mode", "accept-edits", "--model", "m", "--print", prompt},
		{"agy", "-p", prompt, "--mode", "accept-edits"},
	} {
		i, err := graphPromptSlot(argv)
		if err != nil || argv[i] != prompt {
			t.Fatalf("%v: slot=%d err=%v", argv, i, err)
		}
	}
	for _, argv := range [][]string{
		{"claude", "--model", "m", "task"},
		{"claude", "-p", "task", "another"},
		{"codex", "exec", "resume", "--model", "m", "task"},
		{"codex", "exec", "--model", "m", "-"},
		{"agy", "--print", "--input-format", "stream-json", "task"},
		{"agy", "--print", "--resume", "session", "task"},
		// agy -p takes the next argument as the prompt: a flag there would be
		// sent as the prompt and the real prompt dropped (live agy 1.4.10).
		{"agy", "-p", "--mode", "accept-edits", "--model", "m", "--", "task"},
		{"agy", "-p", "--", "task"},
		{"agy", "--print", "task", "--", "other"},
		{"agy", "--print", "a", "--prompt", "b"},
		{"opencode2", "run", "--standalone", "--variant", "high", "task"},
		{"opencode2", "run", "-m", "m", "task"},
		{"opencode2", "run", "--standalone", "--session", "s", "task"},
	} {
		if _, err := graphPromptSlot(argv); err == nil {
			t.Fatalf("accepted %s", strings.Join(argv, " "))
		}
	}
}

func TestGraphPromptSlotNativeCruxSelection(t *testing.T) {
	prompt := "approved task\n'\" $(touch must-not-run)"
	for _, argv := range [][]string{
		{"claude", "--plugin-dir", "/absolute/crux plugin", "--agent", "crux:crux-agent", "--model", "m", "-p", prompt},
		{"claude", "-p", prompt, "--agent", "crux:crux-comment-reviewer", "--plugin-dir", "/absolute/crux"},
		{"agy", "--agent", "crux", "--model", "m", "--effort", "low", "--print", prompt},
	} {
		slot, err := graphPromptSlot(argv)
		if err != nil || argv[slot] != prompt {
			t.Fatalf("native Crux selection rejected: %v slot=%d err=%v", argv, slot, err)
		}
	}
	for _, argv := range [][]string{
		{"claude", "--plugin-dir", "relative/crux", "-p", prompt},
		{"claude", "--plugin-dir", "/a/../crux", "-p", prompt},
		{"claude", "--plugin-dir", "/crux", "--plugin-dir", "/other", "-p", prompt},
		{"claude", "--agent", "crux:crux-agent", "--agent", "other", "-p", prompt},
		{"agy", "--agent", "crux", "--agent", "other", "--print", prompt},
		{"agy", "--agent", "", "--print", prompt},
		{"agy", "--agent", "--print", prompt},
		{"agy", "--plugin-dir", "/crux", "--print", prompt},
		{"codex", "exec", "--agent", "crux-agent", prompt},
		{"opencode2", "run", "--standalone", "--agent", "crux", prompt},
		{"claude", "--plugin-dir", "/crux", "--permission-mode", "bypassPermissions", "-p", prompt},
		// Only the worktree-scoped edit mode is allowed; every escalation stays refused.
		{"claude", "--permission-mode", "auto", "-p", prompt},
		{"claude", "--permission-mode", "dontAsk", "-p", prompt},
		{"claude", "--permission-mode", "acceptEdits", "--permission-mode", "acceptEdits", "-p", prompt},
		{"claude", "--permission-mode", "-p", prompt},
		{"claude", "--dangerously-skip-permissions", "-p", prompt},
		{"codex", "exec", "--sandbox", "danger-full-access", prompt},
		{"codex", "exec", "--sandbox", "read-only", "--sandbox", "workspace-write", prompt},
		{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", prompt},
		{"codex", "exec", "-s", "danger-full-access", prompt},
		{"agy", "--mode", "auto", "--print", prompt},
		{"agy", "--dangerously-skip-permissions", "--print", prompt},
		{"agy", "--permission-mode", "acceptEdits", "--print", prompt},
		{"claude", "--mode", "accept-edits", "-p", prompt},
		{"codex", "exec", "--permission-mode", "acceptEdits", prompt},
	} {
		if _, err := graphPromptSlot(argv); err == nil {
			t.Fatalf("accepted ambiguous or unsupported native selection: %v", argv)
		}
	}
}

func TestGraphLaunchInputFourHarnessSubprocesses(t *testing.T) {
	e, path, p := localGraphFixture(t, "exit 0", "true")
	_, hash, err := supervisor.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control, err := run.AcquireController(p.Repo, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	d := &routing.Decision{Run: p.Run, Task: "task", Attempt: "current", Worktree: e.wt, ContractHash: e.hash}
	original := "งานที่อนุมัติ\n'\" $(touch should-not-exist) `echo bad`"
	t.Setenv("REIN_HANDOFF_PATH", "untrusted-env-receipt")
	for _, argv := range [][]string{
		{"claude", "-p", original, "--model", "m"},
		{"claude", "--plugin-dir", "/absolute/crux plugin", "--agent", "crux:crux-agent", "-p", original, "--model", "m"},
		{"codex", "exec", "-m", "m", original},
		{"agy", "--print", original, "--model", "m"},
		{"agy", "--agent", "crux", "--print", original, "--model", "m"},
		{"opencode2", "run", "--standalone", "-m", "m", original},
	} {
		t.Run(argv[0], func(t *testing.T) {
			binary := filepath.Join(e.root, argv[0])
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\0' \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			argv[0] = binary
			enriched, digest, err := graphLaunchInput(control, p, p.Tasks[0], d, argv, graphHandoffRef{})
			if err != nil {
				t.Fatal(err)
			}
			slot, _ := graphPromptSlot(enriched)
			b, err := exec.Command(enriched[0], enriched[1:]...).Output()
			if err != nil {
				t.Fatal(err)
			}
			actual := strings.Split(string(b), "\x00")
			input := actual[slot-1]
			if !strings.HasSuffix(input, original) || !strings.Contains(input, `"attempt":"current"`) || !strings.Contains(input, `"allow"`) || !strings.Contains(input, "concise Thai") || strings.Contains(input, "untrusted-env-receipt") || digest != graphHash([]byte(input)) {
				t.Fatalf("missing/lossy context %q", input)
			}
			for i := 1; i < len(argv); i++ {
				if i != slot && actual[i-1] != argv[i] {
					t.Fatalf("changed nonprompt argv %d", i)
				}
			}
		})
	}
	for _, field := range []string{"run", "task", "attempt", "worktree", "contract"} {
		changed := *d
		switch field {
		case "run":
			changed.Run = "wrong"
		case "task":
			changed.Task = "wrong"
		case "attempt":
			changed.Attempt = ""
		case "worktree":
			changed.Worktree = "wrong"
		case "contract":
			changed.ContractHash = "wrong"
		}
		if _, _, err := graphLaunchInput(control, p, p.Tasks[0], &changed, []string{"agy", "--print", original}, graphHandoffRef{}); err == nil {
			t.Fatalf("accepted changed %s", field)
		}
	}
}

func TestGraphCLIConsumesPromptAndLogsOnlyInputDigest(t *testing.T) {
	e, path, _ := localGraphFixture(t, `for arg do case "$arg" in *"Original approved task prompt (verbatim):"*) printf '%s' "$arg" > "$GRAPH_INPUT_CAPTURE";; esac; done`, "true")
	output := filepath.Join(e.root, "input-capture")
	t.Setenv("GRAPH_INPUT_CAPTURE", output)
	t.Setenv("REIN_HANDOFF_PATH", "spoofed")
	if out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "approved task") || strings.Contains(string(raw), "spoofed") {
		t.Fatalf("bad prompt %s", raw)
	}
	ledgerRaw, err := os.ReadFile(ledger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ledgerRaw), graphHash(raw)) || strings.Contains(string(ledgerRaw), "Original approved task prompt") {
		t.Fatal("input digest missing or input leaked to ledger")
	}
}

func TestGraphCLIPreservesApprovedCruxAgent(t *testing.T) {
	worker := `printf '%s\0' "$@" > "$GRAPH_ARGV_CAPTURE"
for arg do
 case "$arg" in *"Original approved task prompt (verbatim):"*) printf '%s' "$arg" > "$GRAPH_INPUT_CAPTURE";; esac
done`
	e, path, _ := localGraphFixture(t, worker, "true", func(_ *e2eEnv, p *supervisor.Plan) {
		argv := p.Tasks[0].Launches["w"]
		p.Tasks[0].Launches["w"] = append([]string{argv[0], "--agent", "crux"}, argv[1:]...)
	})
	argvPath, inputPath := filepath.Join(e.root, "selected-argv"), filepath.Join(e.root, "selected-input")
	t.Setenv("GRAPH_ARGV_CAPTURE", argvPath)
	t.Setenv("GRAPH_INPUT_CAPTURE", inputPath)
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 {
		t.Fatalf("native agent graph rejected: code=%d output=%s", code, out)
	}
	var result supervisor.GraphResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Completed) != 1 || result.State != routing.WaitHuman {
		t.Fatalf("graph did not complete: %+v", result)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(raw), "\x00")
	if len(args) < 3 || args[0] != "--agent" || args[1] != "crux" {
		t.Fatalf("approved native agent selection changed: %q", args)
	}
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input), "concise Thai") || !strings.HasSuffix(string(input), "approved task") {
		t.Fatal("native agent selection lost approved task context or Thai policy")
	}
}

func TestGraphOpenCodeConsumesExactPromptViaStdin(t *testing.T) {
	for _, executable := range []string{"opencode", "opencode2"} {
		for _, placement := range []string{"last", "terminal", "before-model"} {
			t.Run(executable+"/"+placement, func(t *testing.T) {
				original := "งานที่อนุมัติ\n'\" $(touch should-not-exist) `echo bad`\n"
				// Match V2's positional quoting, then consume stdin without rewriting bytes.
				worker := `case "$1" in
--version) echo 2.0.32; exit;;
models) echo openai/gpt-test; exit;;
run) shift;;
*) exit 2;;
esac
printf '%s\0' "$@" > "$GRAPH_ARGV_CAPTURE"
positional=0
while [ "$#" -gt 0 ]; do
 case "$1" in
 --standalone) shift;;
 -m) shift 2;;
 --) shift; break;;
 *) break;;
 esac
done
for arg do
 positional=$((positional + 1))
 case "$arg" in *' '*) printf '"%s"' "$arg";; *) printf '%s' "$arg";; esac
done > "$GRAPH_INPUT_CAPTURE"
if [ "$positional" = 0 ]; then cat > "$GRAPH_INPUT_CAPTURE"; fi`
				e, path, _ := localGraphFixture(t, "exit 0", "true", func(e *e2eEnv, p *supervisor.Plan) {
					e.c.HooksInstalled = []string{"opencode"}
					if _, err := e.c.Save(); err != nil {
						t.Fatal(err)
					}
					e.hash = routing.ContractHash(e.c)
					var profile routing.TaskProfile
					if err := json.Unmarshal(e.pj, &profile); err != nil {
						t.Fatal(err)
					}
					profile.ContractHash = e.hash
					e.pj, _ = json.Marshal(profile)
					p.Tasks[0].Profile, p.Tasks[0].ContractHash = e.pj, e.hash
					cli := filepath.Join(e.root, executable)
					if err := os.WriteFile(cli, []byte("#!/bin/sh\n"+worker+"\n"), 0700); err != nil {
						t.Fatal(err)
					}
					cfg, err := providers.Load(e.cfgPath)
					if err != nil {
						t.Fatal(err)
					}
					w := cfg.Providers["w"]
					w.Agent, w.Model = executable, "openai/gpt-test"
					w.Billing = &providers.Billing{Mode: "subscription", Unit: "subscription_percent", Pool: "opencode/subscription"}
					cfg.Providers["w"] = w
					cfg.Discovery[executable] = providers.DiscoverySpec{Command: []string{cli}}
					raw, _ := json.Marshal(cfg)
					if err := os.WriteFile(e.cfgPath, raw, 0600); err != nil {
						t.Fatal(err)
					}
					for i := 0; i < 5; i++ {
						id := fmt.Sprintf("stdin-seed-%d", i)
						launch := ledger.Row{Kind: "routing_launch", Attempt: id, Run: "old", Task: "old", Worker: &ledger.AgentModel{Agent: w.Agent, Model: w.Model}, Type: "backend", Effort: w.Effort, Config: routing.ProviderFingerprint(w), Suite: "s1", Tier: "T1"}
						outcome := ledger.Row{Kind: "task", Attempt: id, Run: "old", Task: "old", Source: "measured", GatesPassed: yes(), Reviewer: &ledger.AgentModel{Agent: "claude", Model: "claude-opus-5-5-high"}, ReviewSHA: "sha", Approved: true, Blockers: zero(), Highs: zero(), FalseClaims: zero(), Drift: zero(), ReviewRounds: one()}
						charge, err := ledger.NewChargeRow(id, "w", "worker", w.Billing.Pool, w.Billing.Unit, w.Agent, w.Model, 1.5)
						if err != nil {
							t.Fatal(err)
						}
						seedRows(t, launch, outcome, charge)
					}
					p.Pools = []string{executable}
					argv := []string{cli, "run", "--standalone", "-m", "{model}#{effort}"}
					if placement == "terminal" {
						argv = append(argv, "--")
					}
					if placement == "before-model" {
						argv = append([]string{cli, "run", "--standalone", original}, "-m", "{model}#{effort}")
					} else {
						argv = append(argv, original)
					}
					p.Tasks[0].Launches["w"] = argv
				})
				inputPath, argvPath := filepath.Join(e.root, "input"), filepath.Join(e.root, "argv")
				t.Setenv("GRAPH_INPUT_CAPTURE", inputPath)
				t.Setenv("GRAPH_ARGV_CAPTURE", argvPath)
				if out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) }); code != 0 {
					t.Fatalf("graph code=%d output=%s", code, out)
				}
				input, err := os.ReadFile(inputPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(input), "Rein approved task context") || !strings.HasSuffix(string(input), original) {
					t.Fatalf("consumed input was rewritten: %q", input)
				}
				argv, err := os.ReadFile(argvPath)
				if err != nil || string(argv) != "--standalone\x00-m\x00openai/gpt-test#high\x00" {
					t.Fatalf("provider retained positional input/delimiter or changed flags: %q err=%v", argv, err)
				}
				rows, err := ledger.LoadStrict("")
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range rows {
					if row.Kind == "routing_launch" && row.Run == "graph" {
						var note struct {
							InputDigest string `json:"input_digest"`
						}
						if err := json.Unmarshal([]byte(row.Notes), &note); err != nil || note.InputDigest != graphHash(input) {
							t.Fatalf("consumed bytes differ from approved input digest: %+v err=%v", note, err)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("missing attributable input digest")
				}
				if _, err := os.Stat(filepath.Join(e.wt, "should-not-exist")); !os.IsNotExist(err) {
					t.Fatal("prompt executed as shell code")
				}
			})
		}
	}
}

func TestGraphLaunchRevalidatesRetryEvidenceImmediately(t *testing.T) {
	for _, kind := range []string{"receipt", "execution", "checkpoint", "run", "task", "digest"} {
		t.Run(kind, func(t *testing.T) {
			e, path, p := quotaGraphFixture(t)
			_, hash, err := supervisor.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := run.AcquireController(p.Repo, hash)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ops := graphOperations(p)
			first, err := ops.Prepare(context.Background(), c, p.Tasks[0])
			if err != nil {
				t.Fatal(err)
			}
			expand := func(d *routing.Decision) []string {
				a := append([]string(nil), p.Tasks[0].Launches[d.Provider]...)
				for i, v := range a {
					a[i] = strings.ReplaceAll(strings.ReplaceAll(v, "{model}", d.Model), "{effort}", d.Effort)
				}
				return a
			}
			previous, err := ops.Launch(context.Background(), c, p.Tasks[0], first.Receipt, expand(first.Receipt))
			if err != nil {
				t.Fatal(err)
			}
			observed, err := ops.Observe(context.Background(), c, p.Tasks[0], previous)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := ops.Finish(context.Background(), c, p.Tasks[0], previous, observed)
			if err != nil {
				t.Fatal(err)
			}
			if err := ops.Resume(context.Background(), c, p.Tasks[0], previous, outcome); err != nil {
				t.Fatal(err)
			}
			next, err := ops.Prepare(context.Background(), c, p.Tasks[0])
			if err != nil {
				t.Fatal(err)
			}
			mutatedTask := p.Tasks[0]
			switch kind {
			case "execution":
				os.WriteFile(graphEvidencePath(previous.Attempt, "execution"), []byte("{}"), 0600)
			case "checkpoint":
				os.WriteFile(filepath.Join(e.wt, "src/a.go"), []byte("tampered"), 0600)
			case "receipt", "run", "task", "digest":
				receipt := graphEvidencePath(previous.Attempt, "handoff")
				raw, _ := os.ReadFile(receipt)
				var body map[string]any
				json.Unmarshal(raw, &body)
				field := map[string]string{"receipt": "progress", "run": "run", "task": "task", "digest": "checkpoint_digest"}[kind]
				body[field] = "tampered"
				raw, _ = json.Marshal(body)
				os.WriteFile(receipt, raw, 0600)
			}
			if _, err := ops.Launch(context.Background(), c, mutatedTask, next.Receipt, expand(next.Receipt)); err == nil {
				t.Fatal("changed continuation launched")
			}
			if routing.ReceiptUsed(next.Attempt) {
				t.Fatal("changed continuation consumed fresh receipt")
			}
		})
	}
}
