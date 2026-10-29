package guard

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func userOnlyVendorEvent(vendor, command string) string {
	input := `{"command":` + jstr(command) + `}`
	switch vendor {
	case "claude", "codex", "zcode":
		return strings.Replace(f2(codexPre, `"Bash"`, input), `"session_id":"s"`, `"session_id":"`+coordSession+`"`, 1)
	case "agy":
		return f2(agyPre, `{"CommandLine":`+jstr(command)+`,"Cwd":"@WT@"}`, `"run_command"`)
	case "kiro":
		return f2(kiroPre, `"execute_bash"`, input)
	case "opencode":
		return f2(opencodeP, `"shell"`, input)
	case "hermes":
		return f2(hermesPre, `"terminal"`, input)
	}
	panic("unknown fixture vendor")
}

var userOnlyCommands = []string{
	"rein run allow --task t --reason r",
	"rein run end --abandon --reason r",
	"rein budget raise --pool p --amount 1 --reason r",
	"rein route reconcile --hold h --reason r",
	"rein run start --run r --supervisor-plan plan.json",
	"rein run start --run r --supervisor-plan=plan.json",
	"rein run start --run r -supervisor-plan plan.json",
	"rein run start --run r -supervisor-plan=plan.json",
	"timeout 5 env X=1 rein run start --supervisor-plan plan.json",
	"env -u CODEX_THREAD_ID rein run allow --task t --reason r",
	`r=rein; "$r" run start --supervisor-plan=plan.json`,
	`r=rein; command "$r" run start -supervisor-plan plan.json`,
	`bash -lc 'rein run start --supervisor-plan plan.json'`,
	`echo "$(rein run allow --task t --reason r)"`,
	`echo ok; rein budget raise --pool p --amount 1 --reason r`,
}

func assertUserOnlyDecision(t *testing.T, vendor, out, errb string, code int) {
	t.Helper()
	if !strings.Contains(out+errb, "for the user") {
		t.Fatalf("%s: missing user-only reason: %q %q (exit %d)", vendor, out, errb, code)
	}
	wantCode, decision := 0, `"permissionDecision":"deny"`
	switch vendor {
	case "kiro", "opencode":
		wantCode, decision = 2, "[rein]"
	case "agy":
		decision = `"decision":"deny"`
	case "hermes":
		decision = `"action":"block"`
	}
	if code != wantCode || !strings.Contains(out+errb, decision) {
		t.Errorf("%s: wrong vendor denial: %q %q (exit %d)", vendor, out, errb, code)
	}
}

func TestUserOnlyWorkersEveryVendor(t *testing.T) {
	for _, vendor := range []string{"claude", "codex", "agy", "kiro", "opencode", "hermes", "zcode"} {
		t.Run(vendor, func(t *testing.T) {
			wt, c := setup(t)
			for _, bound := range []bool{false, true} {
				invoke := func(command string) (string, string, int) {
					event := userOnlyVendorEvent(vendor, command)
					if !bound {
						return call(t, vendor, event, wt)
					}
					var out, errb bytes.Buffer
					event = strings.ReplaceAll(event, "@WT@", strings.Trim(jstr(wt), `"`))
					code := RunTask(vendor, c.Name, strings.NewReader(event), &out, &errb)
					return out.String(), errb.String(), code
				}
				for _, command := range userOnlyCommands {
					t.Run(command, func(t *testing.T) {
						out, errb, code := invoke(command)
						assertUserOnlyDecision(t, vendor, out, errb, code)
					})
				}
				for _, command := range []string{"rein version", "echo ok", "cat backend/Routing/A.cs", "rein run audit", "rein run start --run ordinary"} {
					if out, errb, code := invoke(command); code != 0 || out != "" || errb != "" {
						t.Errorf("ordinary worker command %s: %q %q (exit %d)", command, out, errb, code)
					}
				}
			}
		})
	}
}

func TestUserOnlyCoordinatorsEveryVendor(t *testing.T) {
	for _, vendor := range []string{"claude", "codex", "agy", "kiro", "opencode", "hermes", "zcode"} {
		t.Run(vendor, func(t *testing.T) {
			e := newCoordEnv(t)
			for _, command := range userOnlyCommands {
				t.Run(command, func(t *testing.T) {
					out, errb, code := call(t, vendor, userOnlyVendorEvent(vendor, command), e.root)
					assertUserOnlyDecision(t, vendor, out, errb, code)
				})
			}
			ordinary := []string{"rein version", "echo ok", "cat README.md", "rein run audit", "rein run start --run ordinary"}
			if vendor != "claude" {
				// The narrow policy must not turn on Claude's write or routing protocol.
				ordinary = append(ordinary, "printf x > src/a.go", "git push origin HEAD", "orca orchestration worker-start --task t")
			}
			for _, command := range ordinary {
				if out, errb, code := call(t, vendor, userOnlyVendorEvent(vendor, command), e.root); code != 0 || out != "" || errb != "" {
					t.Errorf("ordinary run command %s: %q %q (exit %d)", command, out, errb, code)
				}
			}
			for _, command := range userOnlyCommands {
				if out, errb, code := call(t, vendor, userOnlyVendorEvent(vendor, command), e.outside); code != 0 || out != "" || errb != "" {
					t.Errorf("outside-run session: %q %q (exit %d)", out, errb, code)
				}
			}
		})
	}
}

func TestUserOnlyNoRunSessionsStaySilent(t *testing.T) {
	for _, vendor := range []string{"claude", "codex", "agy", "kiro", "opencode", "hermes", "zcode"} {
		t.Run(vendor, func(t *testing.T) {
			e := newCoordEnv(t)
			// Remove only this disposable fixture marker, leaving a repository with no run.
			if err := os.Remove(e.marker); err != nil {
				t.Fatal(err)
			}
			for _, command := range append([]string{"rein version", "echo ok"}, userOnlyCommands...) {
				if out, errb, code := call(t, vendor, userOnlyVendorEvent(vendor, command), e.root); code != 0 || out != "" || errb != "" {
					t.Errorf("no-run session: %q %q (exit %d)", out, errb, code)
				}
			}
		})
	}
}

func TestUserOnlyRunAnchorAndPostToolControls(t *testing.T) {
	e := newCoordEnv(t)
	for _, vendor := range []string{"codex", "agy", "opencode", "hermes"} {
		t.Run(vendor, func(t *testing.T) {
			event := userOnlyVendorEvent(vendor, "rein run allow --task t --reason r")
			switch vendor {
			case "agy":
				event = strings.Replace(event, `"Cwd":"@WT@"`, `"Cwd":`+jstr(e.outside), 1)
			case "codex", "opencode", "hermes":
				event = strings.Replace(event, `"command":`, `"workdir":`+jstr(e.outside)+`,"command":`, 1)
			}
			out, errb, code := call(t, vendor, event, e.root)
			assertUserOnlyDecision(t, vendor, out, errb, code)
		})
	}
	event := userOnlyVendorEvent("codex", "rein run allow --task t --reason r")
	event = strings.Replace(event, `"PreToolUse"`, `"PostToolUse"`, 1)
	if out, errb, code := call(t, "codex", event, e.root); code != 0 || out != "" || errb != "" {
		t.Errorf("posttool event must stay silent: %q %q (exit %d)", out, errb, code)
	}
}
