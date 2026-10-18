package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/orca"
)

type callRecordingRunner struct {
	t        *testing.T
	outputs  map[string]string
	failures map[string]error
	commands []string
}

func (r *callRecordingRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	command := name + " " + strings.Join(args, " ")
	r.commands = append(r.commands, command)
	out, ok := r.outputs[command]
	if !ok {
		r.t.Errorf("unexpected command: %s", command)
		return nil, errors.New("unexpected command")
	}
	return []byte(out), r.failures[command]
}
func callInputModes(t *testing.T, action string, fields map[string]interface{}) map[string][]string {
	t.Helper()
	payload, _ := json.Marshal(orca.CallRequest{Action: action, Args: fields})
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	positional := []string{action}
	flagged := []string{"--action", action}
	for key, value := range fields {
		flag := "--" + strings.ReplaceAll(key, "_", "-")
		text := fmt.Sprint(value)
		if b, ok := value.(bool); ok {
			flag += "=" + fmt.Sprint(b)
			positional = append(positional, flag)
			flagged = append(flagged, flag)
		} else {
			positional = append(positional, flag, text)
			flagged = append(flagged, flag, text)
		}
	}
	return map[string][]string{"positional": positional, "action": flagged, "json": {string(payload)}, "input": {"--input", string(payload)}, "file": {"--input", path}, "stdin": {"--stdin"}}
}
func withCallStdin(t *testing.T, payload []byte, fn func() int) int {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(payload); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, 0)
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	return fn()
}
func TestCallCLIHardeningProtectedTargets(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	for _, action := range []string{"close", "send", "read"} {
		for _, terminal := range []string{"", "coord"} {
			fields := map[string]interface{}{"terminal": terminal}
			for mode, args := range callInputModes(t, action, fields) {
				t.Run(action+"/"+terminal+"/"+mode, func(t *testing.T) {
					r := &callRecordingRunner{t: t}
					orca.SetRunner(r)
					defer orca.SetRunner(nil)
					payload, _ := json.Marshal(orca.CallRequest{Action: action, Args: fields})
					output, err := os.CreateTemp(t.TempDir(), "stdout")
					if err != nil {
						t.Fatal(err)
					}
					defer output.Close()
					oldStdout := os.Stdout
					os.Stdout = output
					code := withCallStdin(t, payload, func() int { return cmdOrcaCall(args) })
					os.Stdout = oldStdout
					if _, err := output.Seek(0, 0); err != nil {
						t.Fatal(err)
					}
					var response orca.CallResponse
					if err := json.NewDecoder(output).Decode(&response); err != nil || response.OK || response.Error == "" {
						t.Errorf("invalid JSON refusal: %+v %v", response, err)
					}
					if code != 2 {
						t.Errorf("exit=%d want 2", code)
					}
					if len(r.commands) != 0 {
						t.Errorf("guard commands=%v", r.commands)
					}
				})
			}
		}
	}
}
func TestCallCLIHardeningCloseExitCodes(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	for _, scenario := range []string{"clean", "dirty", "unique", "runtime"} {
		t.Run(scenario, func(t *testing.T) {
			wt := t.TempDir()
			fields := map[string]interface{}{"terminal": "worker", "worktree": wt, "check_only": true}
			for mode, args := range callInputModes(t, "close", fields) {
				t.Run(mode, func(t *testing.T) {
					outputs := map[string]string{"git status --porcelain": "", "git branch --show-current": "worker", "git for-each-ref --contains HEAD --format=%(refname) refs/heads refs/remotes refs/tags refs/keep": "refs/heads/main\n", "git rev-parse --short HEAD": "abc"}
					failures := map[string]error{}
					want := 0
					switch scenario {
					case "dirty":
						outputs["git status --porcelain"] = " M file"
						want = 3
					case "unique":
						outputs["git for-each-ref --contains HEAD --format=%(refname) refs/heads refs/remotes refs/tags refs/keep"] = "refs/heads/worker\n"
						want = 4
					case "runtime":
						failures["git status --porcelain"] = errors.New("git failed")
						want = 1
					}
					r := &callRecordingRunner{t: t, outputs: outputs, failures: failures}
					orca.SetRunner(r)
					defer orca.SetRunner(nil)
					payload, _ := json.Marshal(orca.CallRequest{Action: "close", Args: fields})
					code := withCallStdin(t, payload, func() int { return cmdOrcaCall(args) })
					if code != want {
						t.Fatalf("exit=%d want %d", code, want)
					}
				})
			}
		})
	}
}
func TestCallCLIHardeningNoArgsPipe(t *testing.T) {
	if os.Getenv("REIN_CALL_NOARGS_HELPER") == "1" {
		os.Exit(cmdOrcaCall(nil))
	}
	for _, payload := range []string{"", `{"action":"status"}`} {
		t.Run(payload, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCallCLIHardeningNoArgsPipe$")
			cmd.Env = append(os.Environ(), "REIN_CALL_NOARGS_HELPER=1")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			if _, err = writer.WriteString(payload); err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = reader
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if ctx.Err() != nil {
				t.Fatalf("waited for stdin EOF: %v", ctx.Err())
			}
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("exit=%v output=%s", err, out)
			}
		})
	}
}
func TestCallCLIHardeningInvalidJSON(t *testing.T) {
	for _, payload := range []string{`{bad}`, `{}`, `{"action":"unknown"}`} {
		if code := cmdOrcaCall([]string{"--input", payload}); code != 2 {
			t.Errorf("payload %s exit=%d want 2", payload, code)
		}
	}
}

func TestCallCLIHardeningCoordinatorFallback(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	for _, action := range []string{"wait", "drain"} {
		t.Run(action, func(t *testing.T) {
			command := "orca orchestration check --terminal coord --wait --types worker_done,escalation,question --timeout-ms 60000 --json"
			output := `{"ok":true,"result":{"messages":[{"type":"worker_done"}]}}`
			if action == "drain" {
				command = "orca orchestration check --terminal coord --json"
				output = `{"ok":true,"result":{"messages":[]}}`
			}
			r := &callRecordingRunner{t: t, outputs: map[string]string{command: output}}
			orca.SetRunner(r)
			defer orca.SetRunner(nil)
			if code := cmdOrcaCall([]string{action}); code != 0 || len(r.commands) != 1 {
				t.Fatalf("exit=%d commands=%v", code, r.commands)
			}
		})
	}
}
