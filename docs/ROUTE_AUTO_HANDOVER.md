# Handover — `rein route auto` (automatic task + model selection)

เอกสารส่งต่องานสำหรับคนหรือ agent ที่จะทำต่อ ไม่ซ้ำเนื้อหาที่อยู่ในเอกสารอื่นของ repo (อ้างด้วย path แทน)
สถานะ ณ 2026-10-09 · branch `voravitl/rein-integration-20261009` · base commit `80884a9` · เขียนบน macOS/zsh, Go (go.mod = 1.26)

> เอกสารนี้เป็นภาพ ณ จุดเวลาเดียว: ตัวเลข ผลทดสอบ และสถานะ git อาจเก่ากว่าโค้ดในภายหลัง
> เมื่อข้อ §4 ถูกทำจนครบ หรือโค้ดเปลี่ยนไปมาก ให้แก้ไฟล์นี้ให้ตรงหรือลบทิ้ง — อย่าปล่อยให้เก่า

## 0. สถานะ (TL;DR)

- **เป้าหมาย:** implement `docs/ROUTING_SELECTION_DESIGN.md` เป็น `rein route auto` (+ `route settle|reconcile|calibrate|discover`, `ledger charge|fixture`, `contract hash`)
- **ทำเสร็จแล้ว ทั้งโค้ด, test, เอกสาร.** ผ่าน independent review 1 รอบ (16 findings F1–F16 ไม่มี BLOCKER, 3 HIGH) และ **แก้ครบทุกข้อแล้ว**
- **Commit แล้ว แต่ยังไม่ push และยังไม่มี PR:** `3d21eae` (primitives: ledger/providers/budget/run/contract) → `e23daf6` (guard) → `0b1c2d7` (`rein route auto`: routing + cmd + advise.sh) → commit สุดท้ายเป็นเอกสาร (รวมไฟล์นี้). ดู `git log --oneline 80884a9..HEAD`. แต่ละ commit โค้ดผ่าน build/vet/gofmt/ทดสอบทั้ง module ใน staged tree ของตัวเองก่อน commit (ไล่ bisect ได้)
- **สิ่งที่ยังไม่เคยทำ:** review อิสระรอบที่ 2 **กับโค้ดที่แก้แล้ว** (ที่ผ่านมา verify ด้วย test, mutation และ repro ของ reviewer เท่านั้น) — ดู §4
- ข้อจำกัดที่เจ้าของตั้ง: Go stdlib เท่านั้น (`go.mod`/`go.sum` ต้องไม่เปลี่ยน — ตอนนี้ไม่เปลี่ยน), Linux/macOS CI ต้องเขียว, **Windows CI ถูกยกเว้นโดยเจ้าของ**

## 1. Source of truth (อ่านที่นี่ก่อน อย่า duplicate)

| เรื่อง | ที่อยู่ |
|---|---|
| ข้อกำหนดการออกแบบ + **"Implementation status"** (code map, decisions, **Not delivered / known limits**) | `docs/ROUTING_SELECTION_DESIGN.md` |
| วิธีใช้ CLI, flag, refusal codes, receipt/hold lifecycle, **trust model** | `docs/CLI_REFERENCE.md` §15 |
| ขั้นตอนฝั่ง pipeline (task profile → route auto → launch → ledger → settle, review ด้วย `advise.sh`) | `skills/worktree-pipeline/SKILL.md` (bullet "Automatic selection") |
| ตัวอย่าง config/policy ที่ครบ (มี test บังคับว่าครบ) | `examples/profile.example.json`, `examples/fallback-chain.example.json` |
| กฎของ repo (review report 6 หัวข้อ, ผูก Head SHA, ต้องมี file:line / exit code เป็นหลักฐาน) | `AGENTS.md` |

โค้ดหลัก (ดู code map ในเอกสารออกแบบ): `internal/routing/{classify,rank,candidates,auto,diff,receipt,lease,settle}.go`, `internal/providers/discover*.go`, `internal/ledger/{evidence,cost,rates}.go`, `internal/budget/reserve.go`, `internal/run/lock.go`, `internal/contract/selection.go`, `internal/guard/coord_bash.go`, `cmd/rein/{route_auto,ledger_strict,route_launch,routing}.go`, `skills/worktree-pipeline/scripts/advise.sh`

## 2. สถานะ verification และวิธีรันซ้ำ

ผลล่าสุด (หลังแก้ finding ครบ; รันซ้ำกับ staged tree ของแต่ละ commit ก่อน commit ด้วย): `gofmt`/`go vet`/`GOOS=windows go vet` สะอาด · `go test ./...` เขียวครบ 18 package **3 รอบติด** (เมื่อปิด git hook ของเครื่อง ดู §5) · mutation red-check 34 จุด แดงครบและคืนไฟล์ byte-identical · discovery กับ CLI จริง (codex 7 โมเดล + 1 limit, kiro 21, agy 18, opencode2 ~500, claude = `unsupported` ตามออกแบบ) · เส้นทาง refusal ของ binary จริงไม่สร้าง receipt/hold/probe · repro ของ reviewer รันกับโค้ดที่แก้แล้วผลเปลี่ยนตามคาด (F1, F2, F3, F4, F6, F8, F9, F11)

```sh
gofmt -l . ; go vet ./... ; GOOS=windows go vet ./... ; git diff --quiet go.mod go.sum && echo unchanged
GIT_CONFIG_GLOBAL=/dev/null go test ./... -count=1          # ใส่ env นี้ด้วย (ดู §5)
python3 -B -m unittest discover -s skills/worktree-pipeline/scripts -p 'test_advise_routing.py'
python3 -B -m unittest discover -s skills/worktree-pipeline/scripts -p 'test*cleanup*.py'
REIN_DISCOVER_LIVE=1 go test ./internal/providers -run TestLiveHarnessesOptIn -v   # opt-in, ต่อ CLI จริง, ไม่ inference
go build -o "$(mktemp -d)/rein" ./cmd/rein    # ห้าม build ลง repo root
```

Mutation red-check (ไม่ได้เก็บสคริปต์ไว้): แก้บรรทัดกฎ 1 จุด → รัน test ที่ตั้งชื่อไว้ → ต้อง **แดง** → คืนไฟล์แล้วเทียบ sha256. ทำซ้ำเมื่อเพิ่ม/แก้กฎใหม่ (ponytail rule ของเจ้าของ: logic ไม่ trivial ต้องมี check ที่ล้มเมื่อ logic พัง)

## 3. Invariants ที่ห้ามทำพัง (รายละเอียดอยู่ในเอกสาร §1)

- **receipt ใช้ครั้งเดียว** — `ConsumeReceipt` (`lease.go`) เรียกจาก `AcquireLease` (worker) และ `route check --phase review`; ledger ถือ attempt ที่ launch >1 ครั้งว่า incomplete (`evidence.go` `judge`)
- **ชนิดของ item ใน hold** (`budget/reserve.go`): `probe`, `funding`, `review_funding`, `review_reserve`, `calibration`. worker กัน `review_reserve` ไว้, review จอง `review_funding` ของตัวเองแล้วรับช่วงด้วย `ReserveRequest.Handover` ใน transaction เดียว. **ห้ามให้ review spend ถูกบันทึกเป็น component `worker`** (นี่คือ F1)
- **settle** (`routing/settle.go`): probe คิดที่ bound เสมอ; funding คิดที่ bound เว้นแต่มี charge ที่ *measured, ไม่ใช่ approx, > 0* ของชนิดเดียวกันใน pool นั้น; attempt ที่ไม่เคยรันปล่อย funding โดยไม่คิดเงิน; worker ที่ยังรันอยู่ = ปฏิเสธ (`EndLease`)
- **review charge** ต้องระบุ `--provider <ชื่อใน fallback-chain.json>` และ `--model`; `EvaluateReviewCost` match ด้วยชื่อ provider (ไม่ใช่ agent)
- **baseline** = `baselineOrder` ใน `auto.go` วิ่งผ่าน `tryOrdered` (เส้นทางเดียวกับ scored). `Prepare` legacy ต้องคง signature เดิม (อย่าเติม param `mode`/`deny` กลับ)
- **classification fail-closed:** ต้องมี `sensitive_paths`, risk flag นอก vocabulary = `classification_required` ทุก kind, `docs` = ไฟล์เอกสารจริง, review ใช้ git diff จริง (`diff.go`, `--base` default `origin/main`) รวมกับ `changed_files` ของ coordinator (เพิ่มได้อย่างเดียว)
- **guard** (`coord_bash.go`): coordinator ห้ามตั้ง/unset `REIN_PROFILE PIPELINE_{LEDGER,PRICES,CONTRACTS,FALLBACK} REIN_RUN_DIR`, ห้าม `env -i`, ห้าม `--config` กับ `rein route`. **`contract new --profile` ตั้งใจไม่ห้าม** (เป็น flow ที่เอกสารกำหนด) → ใช้ `policy_source` ในรายงาน + คำแนะนำให้ export `REIN_PROFILE`
- **Orca worker** ที่ dispatch แล้ว: lease `Dispatched=true` ถือ task ไว้จน `rein route settle --attempt` (exit row ไม่มี minutes)
- **review ต้องรันบน harness ที่ `advise.sh` รันได้** (`reviewLaunchable` = codex, kiro, claude ใน `candidates.go`) และใช้ effort ตาม receipt (`ADVISE_EFFORT`, `route check --effort`)
- `ledger fixture` **ตั้งใจไม่เป็น user-only** (ไม่งั้นพัง standing-authorization calibration flow) — ความเสี่ยงบันทึกไว้ใน trust model

## 4. ยังไม่ได้ทำ / ยังไม่ verify / จุดที่ควรตรวจต่อ

**งานที่ควรทำถัดไป (เรียงตามความสำคัญ)**
1. **review อิสระกับโค้ดหลังแก้** โดยเฉพาะ: `settle.go`, `lease.go`, `auto.go` (`baselineOrder`, `abandonWinner`, `releaseUnused`), `reserve.go` (`Handover`), `coord_bash.go` (กฎ env), `diff.go`, การเปลี่ยนใน `evidence.go`/`cost.go`. วิธีที่เคยได้ผล: เขียน brief เป็นข้อ ๆ (ให้ระบุ file:line) แล้วส่ง reviewer แบบอ่านอย่างเดียว
2. เมื่อเจ้าของสั่ง: push และเปิด PR (steering `git-workflow`: ดูประวัติ commit ทั้งหมดด้วย `git diff <base>...HEAD`, push ด้วย `-u`, สรุป PR + test plan). **อย่า push/เปิด PR ถ้ายังไม่ขอ**
3. ข้อจำกัดที่เอกสารระบุว่ายังไม่ทำ (รายการเต็มใน `docs/ROUTING_SELECTION_DESIGN.md` "Not delivered"): Claude Code catalog adapter, freshness `remote_verified`, agy quota refresh, auto-refresh ของ rates, calibration *runner* (มีแต่ gate), pin policy ของ owner ตอน `rein run start`, forecast ที่ condition ตาม context + uncertainty, กฎ allocate shared overhead, cross-check `review_sha`/reviewer กับ `rein verdict`, review cost แยกตาม tier/kind

**ยังไม่ verify (ทดสอบด้วย fake เท่านั้น หรือไม่ได้ทดสอบ)**
- flow บวกแบบ end-to-end กับ harness จริง (route auto → probe จริง → launch → settle) — ที่ทดสอบกับของจริงคือ discovery และเส้นทาง refusal; flow บวกใช้ CLI ปลอม
- `advise.sh` กับ codex/claude/kiro จริงเมื่อใช้ `ADVISE_EFFORT` (ตรวจแค่ fake CLI; flag `claude --effort` ยืนยันจาก `claude --help` เท่านั้น)
- ไม่เคยรันบน Linux/Windows จริง (cross-compile + vet เท่านั้น)

**พฤติกรรมที่ควรรู้ (ไม่ใช่ bug ที่ทราบ แต่เป็น trade-off)**
- charge เป็น 0 หรือ approx ไม่ถือเป็นหลักฐานว่าใช้จริง → pool ที่ฟรีจริงจะถูกคิดที่ bound ตอน settle จนกว่าจะบันทึก charge จริง
- T3 (review 2 maker): review แรกรับช่วง `review_reserve` ทั้งก้อน → ความจุของ reviewer คนที่สองไม่ถูกกันไว้ระหว่างรอ
- `releaseUnused` กวาดเฉพาะ receipt ของ *task เดียวกัน*; `routes/used/` ไม่เคย prune (มี ponytail comment)
- `ledger charge` ตรวจ unit กับ billing เฉพาะเมื่อโหลด config ได้ (`--config` หรือ path default)
- guard เป็น seatbelt ของ coordinator ที่เข้าใจผิด ไม่ใช่ sandbox กัน coordinator ที่ตั้งใจโกง; ledger เขียนได้โดย coordinator (trust model ใน `docs/CLI_REFERENCE.md` §15)

## 5. Gotchas ของสภาพแวดล้อมและ test

- **git hook ทั่วเครื่อง** (`core.hooksPath` ใน global git config ของเครื่องที่พัฒนา) อาจเขียนไฟล์ลง repo ชั่วคราวหลัง `git commit` (เห็นเป็น `graphify-out/.rebuild.lock`) → ทำให้ `internal/drift` (และ `internal/verdict` บางครั้ง) **flaky เป็นพัก ๆ** — เป็นของเดิม ไม่เกี่ยวกับงานนี้ (งานนี้ไม่ได้แก้ package นั้น). ใช้ `GIT_CONFIG_GLOBAL=/dev/null` ตอนรัน test; test ที่ใช้ git ของงานนี้ isolate เองด้วย `-c core.hooksPath=/dev/null` + `GIT_CONFIG_GLOBAL=/dev/null` (ทำแบบเดียวกันถ้าเขียน test ใหม่ที่ commit)
- `go test -race` : มี test เดิม **8 ตัว** ล้มที่ HEAD เดิมเหมือนกัน (probe timeout 1 วินาที): `cmd/rein` → `TestRouteLaunchExecutesOnlyPreparedWorker`, `TestRouteCheckPhaseAndWorktree`; `internal/routing` → `TestSequentialQuotaAndValidation`, `TestReviewAndFailureInvalidate`, `TestUnknownConfigAndCooldownState`, `TestStaleReceiptAndKnownReset`, `TestProviderModelNamesAndReservedTask`, `TestMissingWorkerVendorSkipped`. CI ไม่ใช้ `-race` — อย่าไล่แก้. test ใหม่ทั้งหมด race-clean
- helper ใน `internal/routing/auto_test.go`: `newWorld` (ต้องมี `Profile.SensitivePaths`; reviewer ต้องอยู่บน agent codex/kiro/claude; `seedReviewer` ใส่ charge ด้วย *ชื่อ provider*), `seedWorker`, `startRun`, `budgetOf(pools...)`, `costRows`, `invHook`, `diff`/`diffErr`, `profile`, `probes`. ใน `cmd/rein/route_auto_test.go`: `newE2E(t, "shell"|"orca")` + CLI ปลอม (`TestRouteDiscoverProcess` ตอบทั้ง agy และ kiro)
- ข้อสังเกตเฉพาะ agent harness ที่ใช้เขียนงานนี้ (Kiro CLI — อาจเปลี่ยนได้): `orchestrate_subagent` แบบหลาย stage ล้มเสมอ ("No output"); single-stage `general-task-execution` ใช้ได้กับงานอ่าน/review; role `code-reviewer` ไม่มีเครื่องมืออ่านไฟล์ (BLOCKED); subagent เคยเขียนทับ `todo_list` ของ session หลัก
- อย่าใช้ `pkill -f` กว้าง ๆ; temp ที่สร้างเองให้ใช้ `mktemp -d` แล้วลบเอง
- `__pycache__` ใน `skills/worktree-pipeline/scripts` ถูก gitignore แต่ให้ลบหลังรัน python test (ใช้ `-B`)
- `graphify-out/GRAPH_REPORT.md` สร้างก่อนมีไฟล์ใหม่ → graph เก่ากว่าโค้ดปัจจุบัน

## 6. ข้อตกลงกับเจ้าของงาน

- **ตอบเป็นภาษาไทย สั้น กระชับ** ระบุว่า verify อะไรแล้ว/ยังไม่ได้ verify อะไร; ไม่อ้างว่าเสร็จโดยไม่มีหลักฐาน
- ponytail: โค้ดน้อยที่สุดที่ถูกต้อง, ทางลัดต้องมี comment `ponytail:` บอกเพดาน/ทางอัปเกรด, logic ไม่ trivial ต้องมี check ที่รันได้ 1 อัน
- commit/push/PR เฉพาะเมื่อถูกขอ · ไม่เพิ่ม dependency · ไม่ทำเกินคำขอ
- ถ้าทำ review MR/PR ต้องใช้ schema รายงาน 6 หัวข้อและ `rein verdict record` ตาม `AGENTS.md`

## 7. Suggested skills (สำหรับ agent ที่มี skill เหล่านี้)

- `verification-loop` — รัน gate ชุดเต็มซ้ำก่อนอ้างว่าเสร็จ
- `review` — review การเปลี่ยนแปลงตั้งแต่จุดอ้างอิง (Standards + Spec ตาม AGENTS.md) สำหรับรอบ review รอบที่ 2
- `security-review` — trust boundary ของ coordinator, guard env rules, lease/receipt
- `golang-testing` / `golang-patterns` — เมื่อเพิ่ม/แก้ test และโค้ด Go
- `diagnosing-bugs` — ถ้า test flaky/ล้มแบบอธิบายไม่ได้ (เริ่มจาก §5 ก่อน)
- `tdd` — ถ้าเพิ่มความสามารถใหม่ (เช่น Claude adapter, policy pinning)
- `graphify` — นำทาง codebase (รีเฟรช graph ก่อนถ้าจะพึ่ง `GRAPH_REPORT.md`)
- steering `git-workflow` — ตอนเจ้าของสั่งให้ commit/push/PR เท่านั้น (รูปแบบ commit: `<type>: <description>`)

## 8. ขั้นตอนแรกที่แนะนำ

1. `git status --short` ควรสะอาด และ `git log --oneline 80884a9..HEAD` ควรเห็น 4 commit ข้างบน (ถ้าไม่ตรง แปลว่ามีคนแก้ต่อแล้ว — อ่าน diff ก่อน); `git diff --quiet 80884a9 -- go.mod go.sum` ต้องผ่าน
2. รัน gate ใน §2 (ใส่ `GIT_CONFIG_GLOBAL=/dev/null`) เพื่อยืนยันว่ายังเขียว
3. ถามเจ้าของงานว่าจะ: (ก) ให้ review อิสระรอบที่ 2, (ข) commit/PR, หรือ (ค) ทำข้อจำกัดใน "Not delivered" ข้อใดต่อ — แล้วค่อยลงมือ
