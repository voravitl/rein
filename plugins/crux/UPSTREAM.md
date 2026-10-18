# Upstream and attribution

Crux is an independently maintained fork and local runtime port of
[pstack](https://github.com/cursor/plugins/tree/019a6a20592c10bf92869e5740368f578a9997fd/pstack)
from `cursor/plugins`, version `0.15.25`, pinned to revision
`019a6a20592c10bf92869e5740368f578a9997fd`. It is not an official Cursor or
upstream pstack release.

The initial import uses the local Codex adaptation `0.15.25-codex.1`: 54
skills, 23 playbooks, principle references, optional helper scripts, two
agent prompts, Codex native agent templates and session-mode hooks.

Crux changes the plugin identity, invocation names, role names and
private state namespace; removes machine-specific paths and residual Cursor
runtime assumptions; separates shared runtime guidance from Codex integration;
and provides portable native-agent templates. The hook recognizes only Crux
activation and deactivation commands and does not migrate upstream state.

The common playbooks describe an agent development lifecycle. Their actual
execution still depends on exposed harness capabilities. Codex integration
has local hook/structure checks; those checks do not establish live runtime
parity, provider interoperability or other harness support.

The original MIT notice in [LICENSE](LICENSE), including Lauren Tan's
copyright, is preserved verbatim. Original source and documentation URLs stay
intact for attribution. Linked upstream guides explain the original project;
Cursor-specific UI or hosted services in them are not installed by this fork.
The upstream orch coordinator and its independent inbox/unit/verdict stores are
excluded: Rein owns runtime dispatch, retry, lease and approval state.
Dormant automations, upstream artwork and machine-local installation reports
are deliberately excluded. Optional helper tools retain their existing
prerequisites; importing them does not install dependencies or run them.
