package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/orca"
)

func TestOrcaLaunchFlagsAfterOperands(t *testing.T) {
	for _, call := range []bool{false, true} {
		for _, flagsFirst := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "launch", true: "call"}[call], map[bool]string{false: "positional-first", true: "flags-first"}[flagsFirst]}, "/"), func(t *testing.T) {
				for _, env := range []string{"ORCA_RUN", "ORCA_COORD", "ORCA_REPO_ID", "ORCA_REPO_DIR"} {
					t.Setenv(env, "")
				}
				wt := t.TempDir()
				spec := filepath.Join(t.TempDir(), "spec.md")
				if err := os.WriteFile(spec, []byte("task"), 0600); err != nil {
					t.Fatal(err)
				}
				prefix := []string{"launch"}
				flags := []string{"--model=gpt-test", "--effort", "low", "--run", "run", "--coord", "coord", "--repo", "repo", "--repo-dir", wt}
				if call {
					prefix = []string{"call", "launch"}
					flags = append(flags, "--enter", "--check-only=false")
				} else {
					flags = append(flags, "--json")
				}
				operands := []string{"worker", "main", spec, "codex"}
				args := append([]string{}, prefix...)
				if flagsFirst {
					args = append(args, flags...)
					args = append(args, operands...)
				} else {
					args = append(args, operands...)
					args = append(args, flags...)
				}
				outputs := map[string]string{
					"orca worktree create --name worker --repo id:repo --base-branch main --setup skip --no-parent --json":                                                                                       `{"result":{"worktree":{"path":"` + wt + `"}}}`,
					"orca orchestration worker-start --spec task --worktree path:" + wt + " --agent codex --model gpt-test --effort low --run run --from coord --task-title rein worker · codex:gpt-test --json": `{"ok":true,"result":{"state":"ready","taskId":"task","dispatchId":"dispatch","effects":[{"kind":"dispatch_input","role":"agent","id":"terminal","state":"accepted"}]}}`,
					"orca orchestration worker-show --dispatch dispatch --json":                                                                                                                                  `{"ok":true,"result":{"dispatch":{"id":"dispatch","taskId":"task","runId":"run","assigneeHandle":"terminal"},"worker":{"dispatchId":"dispatch","state":"ready","agentTerminalHandle":"terminal"},"observation":{"exactWorker":true,"status":"live"},"terminal":{"handle":"terminal"}}}`,
				}
				runner := &callRecordingRunner{t: t, outputs: outputs}
				orca.SetRunner(runner)
				defer orca.SetRunner(nil)
				if code := cmdOrca(args); code != 0 {
					t.Fatalf("exit=%d want 0; commands=%v", code, runner.commands)
				}
				if len(runner.commands) != len(outputs) {
					t.Fatalf("commands=%v", runner.commands)
				}
			})
		}
	}
}

func TestParseOrcaFlagsBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      []string
		wantErr   bool
		wantEnter bool
		wantModel string
	}{
		{name: "boolean before operand", args: []string{"--enter", "worker"}, want: []string{"worker"}, wantEnter: true},
		{name: "explicit false", args: []string{"worker", "--enter=false"}, want: []string{"worker"}},
		{name: "dash value", args: []string{"worker", "--model", "-model"}, want: []string{"worker"}, wantModel: "-model"},
		{name: "terminator", args: []string{"worker", "--", "--enter", "--model=x"}, want: []string{"worker", "--enter", "--model=x"}},
		{name: "unknown flag", args: []string{"worker", "--unknown"}, wantErr: true},
		{name: "missing value", args: []string{"worker", "--model"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			enter := fs.Bool("enter", false, "")
			model := fs.String("model", "", "")
			err := parseOrcaFlags(fs, tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(fs.Args(), tt.want) || *enter != tt.wantEnter || *model != tt.wantModel {
				t.Fatalf("args=%v enter=%v model=%q", fs.Args(), *enter, *model)
			}
		})
	}
}
