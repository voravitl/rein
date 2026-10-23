package guard

import (
	"os"
	"strings"
)

// orcaIPCSubs are the `orca orchestration` subcommands that only exchange messages and state with the Orca
// server. The Antigravity terminal sandbox blocks them: they read Orca's runtime metadata, which lives outside
// the workspace, and reach the local server, so a worker's `orca orchestration send` fails and no worker_done is
// ever recorded. The subcommands that put a payload somewhere else -- worker-start (its --spec becomes another
// agent's prompt), worker-stop, worker-abandon, worker-release, worker-retain, dispatch --inject -- stay
// sandboxed, as does `orca terminal send --text`, which types arbitrary text into another terminal.
var orcaIPCSubs = map[string]bool{
	"send": true, "check": true, "reply": true, "inbox": true, "ask": true,
	"run-current": true, "run-list": true, "run-show": true,
	"task-list":   true,
	"worker-show": true, "worker-list": true,
	"dispatch-show": true, "request-show": true,
	"gate-list": true,
}

// orcaIPC reports whether args invokes an allowlisted `orca orchestration` IPC subcommand.
func orcaIPC(args []string) bool {
	if len(args) < 3 {
		return false
	}
	if !isOrca(base(args[0])) {
		return false
	}
	return args[1] == "orchestration" && orcaIPCSubs[base(args[2])]
}

// classify records what the command does, so the hook can tell an IPC-only line from a line that also runs
// something else. Only a plain shell -c invocation is transparent: script files and startup options can
// execute code outside the inspected body, even when another command on the line is IPC.
func (x *ctx) classify(name string, args []string) {
	switch {
	case orcaIPC(args):
		x.ipc = true
	case name == "cd" || name == "pushd" || name == "popd":
	case name == "env":
		for _, arg := range args[1:] {
			key, _, assigned := strings.Cut(arg, "=")
			if assigned && (key == "BASH_ENV" || key == "ENV" || key == "ZDOTDIR") {
				x.other = true
			}
		}
	case name == "sudo" || name == "exec":
		x.other = true // these wrappers can start login shells beyond the inspected command
	case wrappers[name] || name == "xargs":
	case name == "bash" || name == "sh" || name == "zsh" || name == "dash" || name == "ksh":
		if name == "zsh" || len(args) < 3 || args[1] != "-c" || os.Getenv("BASH_ENV") != "" || os.Getenv("ENV") != "" {
			x.other = true
		}
	case name == "eval":
	default:
		x.other = true
	}
}
