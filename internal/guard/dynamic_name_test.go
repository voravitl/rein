package guard

import (
	"strings"
	"testing"
)

// A command name that expands at run time could be any command, so the guard cannot judge it by name: it
// must refuse instead of letting it skip the rules a literal name would face.
func TestDynamicCommandNameIsRefused(t *testing.T) {
	e, c := routedEnv(t)
	prepareGuardRouteFor(t, c, e.m.Run, "claude", "claude-sonnet", "orca", "worker:backend")
	workerStart := "orchestration worker-start --run sprint --agent claude --model claude-sonnet --worktree path:" + e.side

	for _, cmd := range []string{
		"$ORCA " + workerStart,
		"${ORCA} " + workerStart,
		"$(which orca) " + workerStart,
		"$CLAUDE --model claude-sonnet exec fix",
		"$(which claude) -p fix",
		"env $ORCA " + workerStart,
		"bash -c '$ORCA " + workerStart + "'",
		"xargs $ORCA orchestration worker-start",
		"eval '$ORCA orchestration worker-start'",
		"find . -name '*.go' -exec $ORCA orchestration worker-start " + ";",
		"timeout 5 $ORCA orchestration worker-start",
		// no rule names this command, and it could be one the guard does name
		"$MYTOOL orchestration worker-start",
	} {
		out := e.bash(cmd)
		if !isDeny(out) {
			t.Errorf("dynamic command name was not refused: %s -> %q", cmd, out)
			continue
		}
		if !strings.Contains(out, "expands at run time") {
			t.Errorf("dynamic command name refused without the reason: %s -> %s", cmd, out)
		}
	}

	// A literal basename with a dynamic directory is judged by its name, and an in-command assignment
	// still resolves, so these keep the message of the rule that matches them.
	for _, cmd := range []string{
		"$HOME/bin/orca " + workerStart,
		"ORCA=/opt/bin/orca-dev; $ORCA " + workerStart,
	} {
		out := e.bash(cmd)
		if !isDeny(out) {
			t.Errorf("resolved command name was not refused: %s -> %q", cmd, out)
		}
		if strings.Contains(out, "expands at run time") {
			t.Errorf("resolved command name took the dynamic-name path: %s -> %s", cmd, out)
		}
	}

	// The setup skill launches rein through an unknown dynamic directory, so that prefix must not be
	// mistaken for a dynamic command name.
	for _, cmd := range []string{
		"$HOME/bin/mytool --version",
		"$HOME/bin/mytool --help",
		`"${CLAUDE_PLUGIN_DATA}/bin/rein" version`,
	} {
		e.expect(false, e.bash(cmd), "literal basename with a dynamic directory: "+cmd)
	}
}

// The worker side has the same hole: a worker writing through a variable command name skips the write rules.
func TestDynamicCommandNameWorkerWrite(t *testing.T) {
	wt, _ := setup(t)
	for _, cmd := range []string{
		"$RM -rf VERSION",
		"$(which rm) -rf VERSION",
		"$TOUCH newfile",
		"$SED -i s/a/b/ VERSION",
		"$MYTOOL --version",
	} {
		out := bashCase(t, wt, cmd)
		if !isDeny(out) {
			t.Errorf("worker dynamic command name was not refused: %s -> %q", cmd, out)
		}
	}
	// A dynamic argument keeps the message of the write rule that matches it.
	out := bashCase(t, wt, "rm -rf $WHERE")
	if !isDeny(out) || !strings.Contains(out, "expands at run time") {
		t.Errorf("dynamic write target must keep the write message: %s", out)
	}
	if out := bashCase(t, wt, "$HOME/bin/mytool --version"); isDeny(out) {
		t.Errorf("literal basename with a dynamic directory was refused: %s", out)
	}
}

// The pre-existing rule that a dynamic command name could be rein keeps its own message.
func TestDynamicCommandNameUserOnlyStillNamed(t *testing.T) {
	e := newCoordEnv(t)
	out := e.bash("\"$REIN\" approve --mr 4")
	if !isDeny(out) || !strings.Contains(out, "for the user") {
		t.Errorf("dynamic rein name must still read as the user's command: %s", out)
	}
}
