# Rein + Crux readiness

## ผลสด: routing และ independent review วันที่ 18 ตุลาคม 2026

เลือก AGY worker และ Codex reviewer อัตโนมัติได้จริง ผู้ตรวจรับ source, revision และ nonce ตรงกัน แล้วพบข้อผิดพลาดจริง: จำนวนเต็มขนาดใหญ่ทำให้ `math.isfinite` โยน `OverflowError` แทน `ValueError` ตามโจทย์ ระบบบันทึกไม่อนุมัติ ปิดยอดจองครบ และตรวจว่าไม่มี hold ค้างแล้ว ผลรวมยังไม่ผ่าน ไม่ใช่การรับรองทั้งระบบ

แก้โค้ดตัวอย่างในเครื่องและเพิ่มกรณีจำนวนเต็มขนาดใหญ่ทั้งบวกและลบ ผ่าน 17 กรณี แต่ยังไม่ได้ตรวจอิสระซ้ำใน revision ที่แก้ ไม่เรียกโมเดลเกินเพดานของรอบเดิม: 6 inference requests; มี native process ที่ถูกปฏิเสธก่อน inference อีก 1 ครั้ง เก็บบันทึกและ conservative probe charge ไว้ตามเดิม ค่าใช้จ่ายเงินจริงยังไม่ทราบ

**ข้อจำกัดของ calibration:** clean fixtures ใน suite v1 มีบั๊กเดียวกัน จึงใช้ผลแยก 4 ผิด/4 ถูกเป็นหลักฐานคุณภาพที่สมบูรณ์ไม่ได้ เก็บ ledger เดิมไว้ และเปลี่ยน reference fixture กับ suite เป็น v2 เพื่อไม่ใช้หลักฐานผิดซ้ำ ชุดตรวจ runner ออฟไลน์ผ่าน 16 tests รวมบั๊กเดิม ผลตรวจ FAIL จริง การปิดยอดจอง และการปฏิเสธ handoff ที่ถูกแก้ไข

แผนต่อยังต้องปิด independent re-review, production supervisor graph ด้วยโมเดลจริงผ่าน Orca terminal, เกณฑ์จริงทั้งสี่ harness และ native Orca lifecycle ตาม [integration plan](integration-plan.md) ผล smoke ที่ประกอบ inline handoff เองไม่ใช้แทน production graph/checkpoint recovery ส่วน graph ที่ใช้ worker จำลองมีผลสำเร็จเพิ่มแล้วตามรายละเอียดด้านล่าง

Status: **NOT COMPLETE / not 100% verified**. Evaluation: 18 October 2026, base commit `2b118c46a30b4aa00c766e15f01eb6095b1642c9` plus uncommitted local changes. Local test success cannot prove behavior across live accounts, models and remote runtimes.

ตรวจซ้ำจาก source ปัจจุบันได้ด้วย `python3 -B plugins/crux/scripts/harness-acceptance.py` รายงานไทยแยก adapter ทั้งสี่ตัวกับแพ็กเกจร่วม รันเฉพาะ Node tests ในเครื่อง ไม่มี provider/model dispatch ผลรอบรวมล่าสุดผ่าน 5 suites (18 Node tests) และ Python ทั้งหมด 55 tests ผ่าน `--format json --require-live` คืน exit 2 ตามที่ตั้งใจ เพราะ local PASS ยังไม่ใช่ live acceptance รายการ native/ภาษาไทยจากโมเดล/guard/delegate/stop/handoff ในรายงานนี้เป็น NOT_VERIFIED ไม่ลบล้างหลักฐานเฉพาะกรณีที่บันทึกไว้ใน runtime docs และไม่แอบรับรองโดยใช้ผลของ harness อื่น

หมายเหตุจำนวน skill: ตัวเลข 55 skills ในตารางและหัวข้อด้านล่างเป็นผลวัดจาก native validator/initialize ณ วันที่ 18 ตุลาคม 2026 ปัจจุบัน source มี 56 skills หลังเพิ่ม `dag` (PR #41, 30 ตุลาคม 2026) แต่ยังไม่ได้รัน native discovery ของแต่ละ harness ซ้ำ จึงยังไม่ยืนยันว่า harness จริงโหลดครบ 56

| Capability | Evidence | Limit |
| --- | --- | --- |
| Shared Crux package and four harness adapters | Codex hook and package tests; Claude native strict manifest validator; Antigravity native strict validator (55 skills, 3 agents); OpenCode2 adapter tests | Package/schema and deterministic behavior, not all live model behavior |
| Claude resolved-model discovery | Metadata-only SDK initialize on native Claude Code 2.1.302: 12 resolved models; adapter tests | Client catalog, not entitlement/quota/quality proof |
| Provider setup and call-off guard | `cmd/rein/config_test.go`, `internal/providers/config_policy_test.go`, `internal/routing/config_policy_test.go` | Defaults intentionally have no model/probe and do not launch work |
| Read Orca quota without model prompts | `internal/providers/config_quota_test.go`; real read-only account-list/check command | Cached observations can be stale; no free-model OpenCode2 inference |
| Read Codex quota directly without Orca | `config_native_quota_test.go`; actual Codex 0.163.0 app-server read and `config check --quota native --format text` | Display-only; other three harnesses unsupported, upstream age/worker account binding unverified; does not replace Orca routing veto |
| Veto probes on exhausted/unknown quota | `internal/providers/config_gate_test.go` | Owner maps model windows; positive quota is not account or spending authorization |
| Veto before automatic reservations and recheck at launch | `internal/routing/config_quota_test.go` | Simulated quota changes and probe processes; no live exhaustion experiment |
| Default Codex credential metadata vs Orca default account | `internal/providers/config_identity_test.go`; real read-only check | Metadata match only; no identity bound to quota observation or native worker; known mismatches veto execution |
| Budget reservations, settlement and unknown usage | `internal/budget/reserve_test.go`; full Go suite | Local accounting, not a hard provider-side cash cap |
| Bounded supervised recovery, partial work and context handoff | `cmd/rein/graph_recovery_test.go` | Controlled local worker fixtures; not an authenticated cross-provider handoff |
| Orca exact worker observation | `cmd/rein/route_launch_orca_test.go`, `internal/orca/worker_observation_test.go` | Exact effective agent/model/effort also checked; readiness is not account binding or independent proof of remote process termination |

## Outstanding acceptance gates

### ผลเพิ่ม: Orca ส่งเข้า production graph แบบไม่เรียกโมเดล

วันที่ 18 ตุลาคม 2026 run `crux-negative-56c4c32592` ผูก plan hash ตรง marker/receipt/journal แล้วหยุดที่ `prepare:error` ตาม config ที่ปิด model calls ไม่มี discovery/probe/worker หรือ ledger/hold ค้าง ผลนี้พิสูจน์ทางปฏิเสธก่อนใช้โควตา ส่วน prompt observation ยังเป็น `unsupported` และ process stop ยังเป็น `stop_unverified` จึงยังไม่ผ่าน transport/lifecycle ครบวงจร ไม่ส่งซ้ำ หลักฐาน: `crux-orca-graph-negative-tn_2t1ja/REPORT.md` และ `verification.json` ในพื้นที่ run ภายนอก repo

### ผลเพิ่ม: production graph สำเร็จด้วย worker จำลอง

Run `crux-fixture-39aa40cf6d` ผ่าน Orca terminal จริงและ production Rein graph จนถึง `WAITING_HUMAN` พร้อมงานเสร็จ 1 งาน เส้นทาง prepare → launch → observe → finish สร้าง execution receipt, checkpoint และผลตรวจไฟล์ที่ตรงตามโจทย์ คำสั่งตรวจ checkpoint คืน exit 0 และไม่มี hold ค้าง Process แม่ยืนยัน supervisor exit 0; การตรวจแยกพบ worker/verifier PID และ process group ไม่มีอยู่แล้ว

ข้อจำกัด: discovery, probe, worker, verifier, quality และงบทั้งหมดในรอบนี้เป็น fixtures แยกส่วน เรียก executable จำลองเท่านั้น ไม่เรียกโมเดลจริง และไม่ใช้ผลนี้รับรองคุณภาพ AGY หรือ provider อื่น Usage ของ worker ที่ไม่ทราบถูกคิดตามยอดจอง ไม่สมมติว่าเป็นศูนย์ Observer แสดงผลไทย/JSON โดยไม่แก้ transport/process receipt หลักฐานอยู่ใน run ภายนอก repo `crux-orca-graph-fixture-c3nuzmym` ได้แก่ `observe-json.json`, `checkpoint-verify-result.json`, `process-liveness-after.json`, `route-holds-after.json` และ `fixture-policy-disclosure.json`

รอบนี้ปิดหลักฐานทางสำเร็จของ local graph กับ worker จำลอง ยังไม่ปิด native model handoff, independent review, active cancellation, process ที่หลุดออกจาก group หรือ remote/federation lifecycle

ทดสอบเข้า graph ที่จบแล้วซ้ำด้วย terminal และ nonce ใหม่โดยใช้ plan เดิม ผลยังเป็น `WAITING_HUMAN` และ supervisor exit 0 ไม่เพิ่ม catalog/probe/worker/verifier หรือ ledger/hold หลักฐานก่อน–หลังตรงกัน 11 ไฟล์; journal เพิ่มเฉพาะ START/RETURN ของ invocation ใหม่ ดู `resume-verification.json` และ manifest 64 ไฟล์ใน run เดียวกัน ผลนี้เป็น completed-graph resume ของ fixture ไม่ใช่การกู้คืนจาก crash หรือ handoff โมเดลจริง

ปิดช่องว่าง OS exit ของ **supervisor ตัวเดียว** เพิ่มแล้วใน run `crux-wait-a7483a158d`: process แม่เปิด Rein โดยตรงและได้ผล `Popen.wait()` เป็น exit 1 ตามนโยบายปิด model calls Observer จับคู่ child PID/nonce/run/plan/profile กับ graph journal แล้วอ่านผลไทย/JSON ได้โดยไม่ส่งซ้ำ ข้อจำกัดยังเป็น descendants, การยกเลิกขณะทำงาน, เส้นทางโมเดลสำเร็จ และทั้งสี่ harness หลักฐานอยู่ใน `crux-orca-parent-wait-slbop24i` ภายนอก repo

เพิ่ม invocation observer แล้วและทดสอบผ่าน Orca จริงใน run `crux-invocation-bd51acf4b0`: START/RETURN จับคู่ run/plan/marker/PID-start ตรงกัน อ่านผลภาษาไทยและ JSON ได้โดยไม่ส่งงานซ้ำหรือแก้ receipt ผลยังเป็น calls-disabled refusal ไม่ใช่งานโมเดลสำเร็จ `graph_returned` ไม่พิสูจน์ OS exit หรือ descendants และไม่เปิดสิทธิ์ cancel/retry จากบันทึกนี้ ดูรายละเอียดใน [Orca bridge](orca-bridge.md#อ่านผลของ-rein-แยกจาก-orca-prompt-observation)

### เกณฑ์ที่ยังต้องปิด

- **Launch-account binding:** prove the provider account used by each native worker matches the account whose quota Orca reports. Current positive quota readings do not prove this. Default Codex credential-file metadata can now be compared with Orca default-account metadata, and known mismatches veto execution; equality still does not prove worker or quota-observation identity. Antigravity model-to-bucket mapping is owner-declared and also needs runtime confirmation. Remote environment overrides fail closed under the quota gate.
- **Real cross-provider handoff:** use a tiny contracted task, known charge basis, explicit call/retry limits and a positive spend cap; verify exact context, worktree changes, independent review, settlement and recovery. No additional model allowance is inferred from setup or tests.
- **Claude live behavior:** repository adapter documentation records successful SDK discovery but a quota-blocked model smoke. A package validation pass does not prove native model compliance.
- **OpenCode2 quality and quota:** adapter documentation records context activation and transport proof, but failed semantic Thai samples on tested free models and a quota-blocked paid sample. Free does not imply unlimited usage.
- **Remote Orca graph lifecycle:** graph execution rejects remote routes until independent termination and ownership evidence is available. Do not release a lease or reservation based only on readiness or transport success.

ขั้นต่อไปคือพิสูจน์ production supervisor graph ผ่าน Orca และตรวจ revision ที่แก้แล้วซ้ำ พร้อมปิดเกณฑ์บัญชีและทั้งสี่ harness ตามแผนด้านบน ข้อดีคือเติมหลักฐานที่ smoke และการตรวจในเครื่องยังให้ไม่ได้ ข้อเสียคือบางขั้นใช้โควตาเพิ่ม จึงต้องแยกการทดสอบที่ไม่เรียกโมเดลออกมาทำก่อน และกำหนดเพดานของรอบใหม่ให้ชัด ค่าใช้จ่ายเงินจริงยังไม่ทราบ ไม่ใช้สิทธิ์ที่เหลือจากรอบเก่าหรือ calibration v1 แทนหลักฐานที่ต้องมี

## Verification receipt (18 October 2026)

- Integrated main: `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test ./... -count=1` — exit 0, all 21 packages; cmd/rein 83.600s, routing 60.410s.
- Targeted quota/config race checks: `go test -race ./internal/providers ./internal/routing ./cmd/rein -run 'TestQuota|TestOrcaQuota|TestConfig' -count=1` — exit 0.
- `go vet ./...` and `git diff --check` — exit 0.
- `node --test plugins/crux/adapters/codex/hooks/*.test.mjs plugins/crux/tests/*.test.mjs` — 18/18 pass.
- Native Claude strict plugin validation — exit 0; native Antigravity strict bundle validation — exit 0, 55 skills, 3 agents, no findings.
- Read-only real Orca inspection — exit 0; Claude/Codex/Antigravity snapshots `ok`, OpenCode2 `unknown` at inspection time. This is a historical observation, not a current remaining allowance.
- No additional model prompts, new agents, global installation or account changes were used for this verification. The changes are uncommitted.


## Account metadata verification receipt (18 October 2026)

- Integrated main: `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test ./... -count=1` — exit 0, all 21 packages; cmd/rein 84.868s, routing 57.317s.
- Targeted race check: `go test -race ./internal/providers ./internal/routing ./cmd/rein -run 'TestCodexAccount|TestCodexMetadata|TestQuota|TestOrcaQuota|TestConfig' -count=1` — exit 0.
- `go vet ./...` and `git diff --check` — exit 0; Node package/hook tests — 18/18 pass.
- Real read-only `config check --dir plugins/crux/config --quota orca` — exit 0, calls disabled; Codex `credential_metadata_match`, other providers `unverified`. Claude, Codex and Antigravity quota snapshots were stale; OpenCode2 quota was unknown. Metadata equality does not make stale quota usable.
- Regression proof: a changed local Codex account ID vetoes the next probe before its model process runs; snapshots expose no account IDs or tokens. This is a conservative metadata check, not complete account binding.
- No additional model prompts, new agents or account changes were used. Live cross-provider and remote lifecycle gates remain outstanding.


## Thai CLI summary receipt (18 October 2026)

- Added `rein config check --dir PATH --format text` for deterministic Thai output; default JSON remains unchanged. `--quota orca` remains an explicit read-only option.
- Reports call-off, missing models/probes, unknown/stale quota and partial account evidence without calling a model. Stale remaining percentages are not presented as current allowance. A check never declares live routing ready.
- Tests: `go test ./cmd/rein -run TestConfig -count=1` exit 0; integrated `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test ./... -count=1` exit 0, all 21 packages (cmd/rein 81.034s, routing 57.428s); `go vet ./...` and `git diff --check` exit 0.
- Built CLI smoke: default JSON parses with calls disabled and four providers; Thai text exits 0; invalid format exits 2. No quota/model calls used in this smoke.
- Full interoperability remains unverified under the outstanding acceptance gates above. No additional model calls or agents were used.


## Routed Orca quota scope receipt (18 October 2026)

- Closed a local-quota/remote-dispatch gap: routed Orca launches with `require_orca_quota` now reject explicit `--environment`, `--pairing-code` or `--on` selectors before the lease/launch path. Guard-off legacy behavior is preserved; positive local quota still does not prove an unqualified worker's account.
- Installed `orca orchestration worker-start --help` confirms that `--on` selects the worker server independently of the command's current Orca server. No model was called.
- `TestRouteQuotaScopeRejectsExplicitOrcaTargets` checks separated/equal-sign flags, legacy behavior and private selector-value suppression. `TestRouteQuotaScopeStopsDispatchAfterHealthyLocalQuota` prepares a route with healthy fake local quota, confirms receipt validation, then proves remote dispatch never starts.
- Mutation check: bypassing the production scope check started the fake remote dispatch and failed the integration test as expected. Restored production bytes and reran successfully.
- Targeted route scope/Orca tests exit 0; integrated `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test ./... -count=1` exit 0, all 21 packages (cmd/rein 84.228s, routing 58.589s). `go vet ./...` and `git diff --check` exit 0.
- No additional model calls, agents or account changes. Account/worker identity binding, live cross-provider handoff and remote termination proof remain outstanding.


## Orca effective launch settings receipt (18 October 2026)

- Read-only inspection: ten existing worker-list rows and the field shape of one existing worker-show response. Provider projection exposed id/model; effective launch settings exposed agent/model/effort. No account binding reference was found in these inspected responses. This is not a claim about every Orca host or dispatch. No task text, transcript, account identifiers or credentials were printed or persisted.
- Routed launches now require `worker.startOptions.launch.effective` to match the exact prepared agent/model/effort after exact live-dispatch validation. Missing, requested-only or different settings refuse success and preserve the remote fence. Older hosts without this evidence fail closed. This does not prove account, provider model compliance or completion.
- Added effective-setting unit cases and route integration cases; mutation bypass of production verification falsely accepted a model mismatch and was caught by the integration test. Restored and reran successfully.
- Targeted worker/route tests exit 0; integrated `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test ./... -count=1` exit 0, all 21 packages (cmd/rein 85.237s, routing 58.763s). `go vet ./...` and `git diff --check` exit 0.
- Account evidence requirement documented in integration-plan.md as a future runtime API requirement, not a supported wire schema. No additional model calls, agents or account changes. Overall status remains NOT COMPLETE.


## Claude resolved-model adapter receipt (18 October 2026)

- New native evidence supersedes the earlier unsupported catalog status: Claude Code 2.1.302 SDK initialize returns `models[].resolvedModel` and declared effort metadata. Added the version-gated `claude-sdk/initialize-models` adapter in `internal/providers/discover_claude.go`, using existing bounded RPC/process-group cleanup. It sends one initialize control request and EOF, with hooks/MCP disabled and no user message or session persistence.
- Catalog parsing requires valid resolved IDs, deduplicates consistent alias resolutions, rejects inconsistent/missing resolution and control-response identity, and keeps unspecified effort support unknown. Explicit no-effort metadata excludes configured effort before a model probe. Account/session data is neither returned nor included in the normalized catalog hash; billing/context/entitlement remain unknown.
- Final built CLI read-only discovery with model calls disabled: version 2.1.302, complete/client_catalog, 12 unique resolved models, 11 explicit effort-support entries, one unknown. No inference or model prompts were submitted.
- Adapter fixture/privacy/schema tests and routing effort exclusion test pass. Mutation replacing resolved IDs with aliases failed the catalog test as expected; restored and reran successfully. Account-only fixture changes leave the model catalog hash unchanged.
- Integrated `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test ./... -count=1`: exit 0, 21 packages (cmd/rein 88.974s, routing 57.768s). Final targeted `go test -race ./internal/providers ./internal/routing -run 'TestClaude|TestExplicitNoEffort' -count=1`: exit 0 (15.802s/1.863s). `go vet ./...` and `git diff --check`: exit 0.
- Updated routing design and integration plan. This closes Claude's metadata-discovery gap, not weekly quota, actual call eligibility, native Thai/model compliance, account binding, cross-provider live handoff or remote termination gates. No extra agents or account changes were used; shipped model calls remain disabled.


## Native Codex readiness diagnostic (18 October 2026)

- Added `scripts/codex-readiness.py`: uses only native app-server `initialize`, `hooks/list`, `skills/list` for the selected absolute workspace. No session/turn, model prompt, installation or trust changes. Ownership must be `crux@rein`; matching repository skill names alone do not qualify.
- Required metadata: enabled `crux:crux-mode` and `crux:thai-communication`, plus both enabled `sessionStart`/`userPromptSubmit` hook groups with every handler trusted or managed. Missing, disabled, untrusted, modified, unknown or discovery errors do not pass. READY describes metadata only; live acceptance stays NOT_VERIFIED.
- Optional `harness-acceptance.py --native-codex` combines local adapter checks with this real metadata inspection. Other harnesses remain explicitly unverified live. Exit 2 means metadata not ready; exit 1 means inspection/local failure. Default remains offline.
- Actual installed Codex inspection found 55 enabled Crux skills and both hooks untrusted. This confirms the current blocker without assuming trust approval. The helper neither approves nor bypasses it.
- Python suite: 64 tests passed, including real disposable fake-server process tests of exact RPC methods, responses larger than 64 KiB and owned-process timeout cleanup. Tests do not call provider models. Actual Codex check is separate metadata evidence and does not prove native role execution, Thai compliance or cross-provider workflow completion.

## Combined native metadata diagnostic (18 October 2026)

`python3 -B plugins/crux/scripts/harness-acceptance.py --native-all` now combines offline adapter tests with four separate native metadata checks. No user/model turn is submitted. READY/METADATA_READY only describes metadata; every full live workflow gate remains NOT_VERIFIED. Default execution without a native flag remains offline.

| Runtime | Fresh native observation | Scope and remaining gap |
| --- | --- | --- |
| Codex | 55 enabled Crux skills; both required hooks untrusted | Installed `crux@rein`; NOT_READY, no trust changes |
| Claude Code | 55 required Crux skills and 2 required agents | Temporary `--plugin-dir` source selection; hooks/MCP disabled; native source identity and permanent installation not verified |
| Antigravity | Native initialize selects `crux` | Fresh workspace bundle contains 55 skills/3 agents, but native skill/child inventory not exposed. Uses normal CLI profile; userdata isolation and hooks/MCP disabling not verified. Normal initialization history may persist; checker does not change config/auth/trust |
| OpenCode2 | Exact source adapter active; 10 effective data paths isolated | Private server, no sessions, stdin EOF exit 0/reaped, temporary root removed. User installation, activation and context hook not verified by this check |

The combined command returns exit 2 for the known Codex metadata gap; errors return exit 1. Even if all metadata becomes ready, `--require-live` still returns exit 2 until the full live acceptance scope is implemented and proven. Metadata output must never substitute for quota/account binding, Thai semantic evaluation, guard/delegate/stop or cross-provider handoff proof.

Validation: Python suite 86 tests passed in the contracted performer and integrated main (main 10.301s), including malformed/wrong inventory, private metadata exclusion, fixed no-turn protocol, owned-process timeout cleanup, OpenAPI/source matching and temporary path isolation. No Go code changed in this diagnostic slice. This adds repeatable native discovery evidence, not completion of the integration goal.
