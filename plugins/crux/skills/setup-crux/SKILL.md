---
name: setup-crux
description: Configure Crux role models and reasoning budgets for the current supported harness without changing unrelated global settings. Use for setup, budget, role model changes, ตั้งค่าโมเดล หรือกำหนดงบ.
---

Read [Runtime guide](../../references/runtime.md) before executing this skill.

# Setup Crux

## Rein provider setup

When setting up Rein + Crux providers, create the configuration instead of asking the user to find an existing one:

1. Run `rein config init --dir <new-project-config-dir>`. Optionally repeat `--model claude=<id>`, `--model codex=<id>`, `--model antigravity=<id>`, or `--model opencode2=<id>` only for verified or user-selected model IDs. Existing directories are refused. This writes private `fallback-chain.json` and `profile.json`; it does not alter global harness settings.
2. Run `rein config check --dir <dir> --quota orca`. The optional Orca account-list lookup reads quota without a model prompt. Without that flag the command is offline. Summarize only normalized quota windows, timestamps and unknown states; never include raw account records or credentials. Report `account_evidence` literally: `credential_metadata_match` is a Codex credential-file metadata match, not proof of worker or quota-snapshot account binding. Known metadata mismatches refuse execution under the quota gate; unsupported scopes remain `unverified`.
3. Keep generated `model_calls_enabled: false` until actual model IDs, bounded probes, budget pools/caps and independent review chains are configured. Missing or zero ordinary budget caps are uncapped. Do not enable calls merely because a CLI is installed.
4. New setups also enable `require_orca_quota`. Configure exact `quota_windows` per verified model and optional `quota_reserve_percent`; missing/stale/unknown/exhausted windows refuse probes and launches. This conservative veto does not authorize spending or establish launch-account identity. Quota must still be bound to the exact launch account, model and budget pool. Keep Antigravity model buckets separate, reject stale measurements, and treat OpenCode free-model quota as unknown. OpenCode Go subscription quota does not establish free-model limits.
5. Pass the generated file with the routing command's `--config <dir>/fallback-chain.json` and set `REIN_PROFILE=<dir>/profile.json` for the owner profile (contract creation also supports `--profile`). Do not probe models or spawn agents just to validate setup.

## Harness role preferences


1. Read the selected harness adapter, then inspect the currently exposed native spawn schema, role catalog, fixed-role model restrictions, and any supported model inventory. List only models actually callable by the selected surface. A provider model list alone is insufficient. Inherit available role settings when no override is supported.
2. For Codex, read `${CODEX_HOME:-~/.codex}/crux/models.json` if present. For another harness, use only a preference/config location explicitly supported by its adapter; if none exists, report inherited defaults without inventing a file. Preserve supported role choices; report invalid or retired keys before removing them. Do not touch Cursor rules, Codex config.toml, hooks, or unrelated settings.
3. If the user supplied a budget or models, apply those. Otherwise show the current mapping and ask one concise preference question using the available input surface. Offer inherit, small (medium effort), medium (high), and large (highest supported effort). Explicit effort applies only to confirmed real model overrides; inherit remains inherit. Never rewrite model identifiers to encode effort or speed.
4. Present the effective role mapping and any unavailable overrides. Use the following role keys: `feature, refactoring`, `bug-fix`, `perf-issue`, `hillclimb`, `judgment and prose`, `hardest tasks`, `how explorer`, `how explainer`, `why investigators`, `why synthesizer`, `reflect tooling`, `reflect judgment, divergent, synthesizer`, `swarm workers`, `arena runners`, `arena cross-judge pool`, `architect runners`, and `interrogate reviewers`. The last four are panels. Default panels have two inherited seats; other defaults inherit. Explain if the callable models do not offer cross-family diversity.
5. Only for an adapter that documents this role-preference JSON format, write the Crux-owned JSON atomically, preserving private file permissions (directory 0700, file 0600). Use the adapter's schema, `version: 1`, the selected `budget`, and the mapping under `roles`. Validate model names and effort against the actual selected spawn surface before writing. Do not write unavailable settings.
6. Read the file back and report the effective choices. The adapter reads it before each future spawn; this is not a global harness model switch. If role overrides are fixed by the harness, report that the role defaults remain effective.
7. If the project lacks a real-app verification harness, mention `crux:create-verification-skill` once. Generate it only when requested.
