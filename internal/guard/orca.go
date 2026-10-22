package guard

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
// something else. A wrapper or a shell is transparent: the guard judges the command it wraps, and so does this.
// A navigation command changes the directory and nothing else. `sh worker.sh` reads a body the guard cannot see,
// so it can never be IPC-only: no orca call is found inside it.
func (x *ctx) classify(name string, args []string) {
	switch {
	case orcaIPC(args):
		x.ipc = true
	case name == "cd" || name == "pushd" || name == "popd":
	case wrappers[name] || name == "xargs":
	case name == "bash" || name == "sh" || name == "zsh" || name == "dash" || name == "ksh":
	case name == "eval":
	default:
		x.other = true
	}
}
