---
name: swarm
description: "Fan out N parallel workers, drain them, and return one report. Use for /swarm, 'swarm this', or parallel coverage, races, gauntlets, and exploration."
---

Read [Runtime guide](../../references/runtime.md) before executing this skill. It defines native tools, role/model resolution, history scope, and unsupported integrations.


# Swarm

## Start

Open a todolist with one entry per phase before launching anything.

1. Frame
2. Fan out
3. Aggregate
4. Report

## Phase A: Frame

1. State the done predicate and the artifact or report the swarm must return.
2. Choose the shape. Partition into slices, race N workers on identical briefs, or mix both. For a race or mixed shape, declare `first pass`, `rank all`, or `best-of` before spawning.
4. Pick the worker model from the `swarm workers` line in `~/.codex/crux/models.json`. If the rule or that line is missing, use `inherit-parent`. For `auto` or `inherit-parent`, omit `model` so the workers run on the parent model. If the native collaboration tools rejects a slug, use the default and say so. If it rejects the default, use the closest valid slug of the same family from its error message. For a model race, name each arm's model up front. Resolve callable role/model settings per the Codex runtime adapter.
5. Give each worker its own writable output when it writes. When workers verify or measure commits, each brief names the exact SHAs. A measurement brief also names the method (sample count, what one sample is, order). The worker records both in its result.

## Phase B: Fan out

Spawn native agents up to the current concurrency limit, then replenish slots as results arrive. Codex agents share this filesystem. Give each writer a distinct worktree or disjoint owned paths. Use only exposed spawn parameters; cloud environments are not supplied by this port.

For a non-default branch, prepare a separate worktree at the exact ref before spawning, and include its absolute path in the brief.

Every brief stands alone. Include the goal, scope, exact slice or race arm, how to verify, and what to report. Reports use `PASS`, `ISSUES`, or `BLOCKED` with evidence. A worker that can prove a defect reports `ISSUES` and lists every issue it can prove, not only the first.

If a worker drops out, proceed with N-1 and note it.

## Phase C: Aggregate

Read the terminal results. Drop a result that does not record the SHAs and method its brief names, and respawn that worker once. After a second miss, record a gap. A gap does not count as a pass. For coverage, every required slice needs a result. For a race, apply the selection rule declared up front. Use first pass, rank all, or best-of. Do not paste raw worker dumps.

## Phase D: Report

Return one consolidated in-chat report with the table, issue one-liners, gaps or dropouts, and the race rule when used.
