# แผนแก้ Orca hardening: F-01–F-12

สถานะ: ตรวจโค้ดและจัดทำแผนแล้ว ยังไม่ได้แก้ implementation

## ขอบเขตและฐานหลักฐาน

- ตรวจ working tree ที่ HEAD `2ccc0a057074d56076a7ab9cf7fe22a32c4da237` รวมไฟล์ Orca ที่ยัง untracked จึงไม่ใช่ผล review ของ commit นี้เพียงอย่างเดียว
- Working tree มีงานอื่นค้างอยู่หลายไฟล์ ต้องรักษางานเดิมและตรวจ diff เฉพาะงานนี้ก่อนส่งต่อ
- แก้ได้เฉพาะ `cmd/rein/**`, `internal/orca/**`, `agents/**`, `docs/**`; ไม่เปลี่ยน `go.mod`, `VERSION`, root files หรือ `.git`
- ใช้ standard library, `Runner`/`SetRunner`, `CleanJSON` และ mock ที่มีอยู่ ไม่เพิ่ม dependency หรือ framework
- Safety tests ต้องบันทึกคำสั่งและ fail เมื่อเจอคำสั่งที่ไม่คาดหมาย: mock ปัจจุบันคืน success สำหรับ unmatched commands (`internal/orca/orca_test.go:23`, `cmd/rein/orca_call_test.go:22`) ซึ่งอาจซ่อน side effects
- รอบนี้เป็นการตรวจและวางแผน ไม่ใช่ PR approval และไม่ลง verdict ให้ MR ที่ไม่ได้ระบุ

## สถานะทีละ finding

เลขบรรทัดต่อไปนี้อ้างอิง source ณ เวลาตรวจ ต้องตรวจใหม่หากมีการแก้ไฟล์

| Finding | สถานะปัจจุบัน | หลักฐาน | งานที่ต้องทำ |
|---|---|---|---|
| F-01 | ยังไม่แก้ — blocker | `cmd/rein/orca.go:405` default terminal เป็น ORCA_COORD; `internal/orca/call.go:53` dispatch โดยไม่มี coordinator guard | default terminal ว่าง; close/send/read ต้องระบุ worker terminal และปฏิเสธ coordinator ก่อน side effect ครอบคลุม flags และ JSON |
| F-02 | ยังไม่แก้ — blocker | `internal/orca/orca.go:482`, `:490`, `:492` ทิ้ง Git errors; `:501` ยอมรับทุกบรรทัดที่ไม่ว่างเป็น ref | fail closed ทุก Git safety command; ยอมรับเฉพาะ `refs/` และ ref อื่นที่ไม่ใช่ branch ตัวเอง; ห้าม release/close/remove หลังตรวจล้มเหลว |
| F-03 | แก้บางส่วนด้วย allowlist เดิม แต่ยังไม่ครบ | `internal/orca/orca.go:104` มี agent allowlist; `:209`, `:221` ใส่ model ลง shell command โดยไม่มี regex validation | ตรวจ agent และ effective model ด้วย `^[A-Za-z0-9._-]+$` ก่อนสร้าง resource; รักษา agent/model policy เดิม |
| F-04 | พฤติกรรมมีอยู่แล้ว แต่ขาด regression ที่ยืนยันการส่งต่อ | `internal/orca/orca.go:97` รับ opts.Model; defaults ใช้เมื่อว่างที่ `:106`, `:113`, `:117`, `:121`; ส่ง model ที่ `:209`, `:221`, `:230` | ไม่เขียน routing ใหม่; เพิ่ม assertions ว่า model ที่ถูกต้องส่งถึง terminal send/worker-start และ LaunchResult ตรงกัน |
| F-05 | ยังไม่แก้ | `internal/orca/orca.go:210`, `:222`, `:226` ทิ้ง error แล้วคืน LaunchResult | ตรวจ output/error จาก terminal send และ worker-start; คืน error ทันทีเมื่อ start ล้มเหลว |
| F-06 | เอกสารยังขาด invariant | `agents/rein-orchestration.md:146` อธิบาย event loop แต่ไม่มี recovery เมื่อ coordinator ตาย | เพิ่ม same-PID resume และ supervisor end/start protocol |
| F-07 | เอกสารยังขาด invariant | `docs/CLI_REFERENCE.md:114` บอกเพียง resume หลัง disconnect | อธิบาย same live owner process; coordinator ตายต้อง end แล้ว start run/session ใหม่ |
| F-08 | ยังไม่แก้ | `internal/orca/orca.go:407`–`:410` break เมื่อ check error และคืน success ที่ `:452` | first check error ต้อง return nil,error; กำหนดผล partial drain เมื่อรอบถัดไปพังให้ชัด ไม่รายงานว่า drained ครบ |
| F-09 | ยังไม่แก้ | `internal/orca/orca.go:342`, `:351`, `:359` ตรวจ substring; `:362` ทิ้ง JSON parse error | parse CleanJSON แล้วตรวจเฉพาะ result.messages[].type และ structured error; เก็บ Raw/Payload ตามสัญญาเดิม |
| F-10 | ยังไม่แก้ | `internal/orca/orca.go:616` ใช้ HasSuffix | เทียบ filepath.Base(wt) กับ CoordWorktree แบบ exact match |
| F-11 | ยังไม่แก้ใน call CLI | `cmd/rein/orca.go:566` คืน 1 ทุก error; JSON early returns ที่ `:393`, `:453`, `:474` เช่นกัน | ใช้ exit 0 success, 2 invalid/protected target, 3 dirty refusal, 4 unique-commit refusal, 1 runtime/API error ให้ครบทุก input mode |
| F-12 | ยังไม่แก้ | `cmd/rein/orca.go:365`–`:378` อ่าน piped stdin อัตโนมัติเมื่อไม่มี args | no args คืน usage/2 ทันทีโดยไม่อ่าน stdin; อ่าน JSON เฉพาะ --stdin ที่ explicit |

หมายเหตุ F-06/F-07: task เดิมจัดสองข้อนี้ไว้ด้วยกันโดยไม่ได้ให้รายละเอียดแยกเลข จึงติดตามเป็น documentation pair ไม่สร้าง requirement ใหม่ขึ้นเอง

## ข้อที่ต้องจัดการให้ครบก่อนลงมือแก้

1. **JSON bypass:** inline JSON, `--input` และ `--stdin` เรียก CallFromJSON ก่อนถึง flag path (`cmd/rein/orca.go:389`, `:445`, `:459`) ดังนั้นแก้ guard เฉพาะ termFlag ไม่พอ ต้องตรวจ target ที่ shared Call boundary หลัง normalize action และใช้ exit mapping ร่วมกันใน CLI ทุกทางเข้า
2. **Status contract ไม่ตรง implementation:** ปัจจุบัน executeStatus ไม่รับ args และเรียก `orca status --json` ซึ่งรายงาน daemon (`internal/orca/call.go:109`) การใส่ coordinator default ใน map จึงไม่มีผลจริง ก่อน implement ต้องตรวจ CLI/API ที่ติดตั้งว่ารองรับ terminal status หรือไม่ แล้วระบุรูปแบบผลลัพธ์และ regression ให้ตรง requirement เดิม หากไม่รองรับให้บันทึกข้อจำกัดอย่างชัดเจน ห้ามอ้างว่าค่า terminal มีผลแล้ว
3. **Resume เป็น process ownership:** `internal/run/run.go:489`–`:506` บังคับ owner ยังมีชีวิตและ PID เดิมอยู่แล้ว และ `:332`–`:338` ตรวจ process start identity ด้วย จึงไม่ใช่เพียงเลข PID ตรงกัน งานนี้แก้คำอธิบาย ไม่ทำให้ process ใหม่ยึด run เก่าได้ การฟื้นงานของ supervisor ต้องอ้างอิง run identity เดิมและใช้ end/start ที่มีอยู่
4. **Launch failure อาจทิ้ง resource ที่สร้างก่อนหน้า:** คืน error พร้อมชื่อขั้นตอนและข้อมูลช่วยตรวจตามจริง ห้ามเพิ่ม automatic destructive cleanup ใน patch นี้

## ลำดับดำเนินการและ regression ที่ต้องเพิ่ม

### 1. ป้องกัน coordinator และ data loss ก่อน — F-01/F-02/F-11/F-12

ไฟล์: `cmd/rein/orca.go`, `internal/orca/call.go`, `internal/orca/orca.go` และ tests ที่เกี่ยวข้อง

- เพิ่ม regression ที่แดงกับโค้ดเดิมก่อนแก้: close/send/read ไม่มี terminal หรือชี้ ORCA_COORD ต้องถูกปฏิเสธ และ Runner ไม่ถูกเรียก ครอบคลุม positional/--action/inline JSON/--input/--stdin
- ตรวจ invalid input ได้ exit 2 และยังมี JSON error response ที่ใช้งานได้สำหรับ JSON callers
- จำลอง Git status/branch/for-each-ref ล้มเหลวแยกกรณี ทั้ง output ว่างและ output ชวนให้เข้าใจผิด; assert ว่าไม่เรียก worker-release, terminal close หรือ worktree rm
- ทดสอบ ref เป็นข้อความ error, ว่าง, branch ตัวเอง และ ref อื่นที่ถูกต้อง; positive case ให้จบที่ check-only หรือ mock removal เท่านั้น
- ทดสอบ close exit 0/1/3/4 ทุก input mode โดยแยก safety refusal ออกจาก Git/API runtime failure
- ทดสอบ no args กับ pipe ที่ยังเปิด writer อยู่โดยมี bounded subprocess timeout: ต้องคืน 2 และไม่รอ EOF; pipe ที่มี JSON ต้องไม่ถูกอ่านหากไม่มี --stdin; explicit --stdin ยังทำงาน
- ตรวจ wait/drain ยัง fallback ORCA_COORD ได้ตามพฤติกรรมเดิมหลังเปลี่ยน default ของ terminal flag

### 2. ทำ launch ให้เชื่อถือผลได้ — F-03/F-04/F-05

ไฟล์: `internal/orca/orca.go`, `internal/orca/orca_test.go`

- ใช้ table tests สำหรับ model/agent ที่มี whitespace, semicolon, quote, newline, shell substitution และ unsupported agent; assert ปฏิเสธก่อน worktree create
- รักษา aliases และ default model เดิม; regex validation ไม่ใช้แทน policy ว่า agent อนุญาต model ใด
- assert valid custom model ถึงคำสั่งของ AGY/Claude/Codex และ Kiro models ที่ policy ยอมรับ; agent ที่รองรับทั้ง alias ต้องคงพฤติกรรมเดิม
- จำลอง terminal send failure สำหรับ Kiro/AGY และ worker-start failure สำหรับ Claude/Codex; ต้องไม่คืน success LaunchResult

### 3. ทำ event loop และ tidy ให้ตรงข้อมูล — F-08/F-09/F-10

ไฟล์: `internal/orca/orca.go`, `internal/orca/orca_test.go`

- Drain first-call error ต้องคืน error; แยกจาก empty successful delivery; ตรวจ ack/no-ack และผล partial delivery เมื่อรอบหลังล้มเหลว
- Wait รับ message type จริงทั้งสามชนิดแม้ JSON มี tabs/newlines หรือ process log นำหน้า; JSON ผิดรูปต้องไม่เป็น signal
- ใส่ metadata/log ที่มี type=worker_done และข้อความ waiter_exists แต่ result.messages มีเพียง heartbeat: ต้องไม่ถูกนับเป็น completion/error control
- รักษา retry/cancellation behavior ด้วย structured error ตาม envelope จริง ไม่เดา field ใหม่
- Tidy แยก coord กับ worker-coord; exact coordinator ต้องไม่ถูกปิด ส่วนชื่อที่เพียงลงท้ายเหมือนกันต้องจัดประเภทตามจริง; dry-run ต้องไม่ปิด terminal

### 4. ปรับเอกสารและตรวจรวม — F-06/F-07

ไฟล์: `agents/rein-orchestration.md`, `docs/CLI_REFERENCE.md`

- ระบุ resume ใช้ได้เมื่อ owner process เดิมยังอยู่และ PID เดิม; ตายแล้วให้ supervisor ตรวจ run ที่ถูกต้อง, end และ start run/session ใหม่
- อธิบาย explicit worker terminal, protected coordinator, --stdin และ close exit codes ให้ตรง implementation
- ไม่แก้ internal/run หรือ ownership invariant; ใช้ tests เดิมเป็น regression evidence

## Validation และเกณฑ์ปิดงาน

หลักฐานปัจจุบัน:

- รอบก่อนหน้า `go test ./...` ผ่าน 20 packages, exit 0; บาง packages ใช้ cache จึงไม่ถือเป็นหลักฐานว่า findings ถูกแก้
- รอบตรวจนี้ `go test -count=1 ./cmd/rein ./internal/orca` ผ่าน, exit 0 (`cmd/rein` 15.457s, `internal/orca` 0.537s)
- existing tests ยังไม่ครอบคลุม failure/bypass cases ข้างต้น ไม่มี red-check ใหม่หรือ live Orca test ในรอบวางแผน

หลัง implement ต้องทำตามลำดับ:

1. เก็บ red/green evidence ของ regression ใหม่โดยไม่ mutate งานเดิมที่ค้างอยู่ใน working tree
2. `gofmt -l` เฉพาะไฟล์ Go ที่แก้ต้องไม่เหลือรายการ; `git diff --check` ต้องไม่มี whitespace error จาก patch นี้
3. `go test -count=1 ./cmd/rein ./internal/orca ./internal/run`
4. `go vet ./...`
5. `go test -count=1 ./...` ต้องผ่านทุก package
6. ทดสอบ runtime ใน disposable worker worktree/terminal ที่ระบุชัดและมี safety checks; ห้ามใช้ coordinator หรือ worktree จริงที่มีงานค้างเป็นเป้าทดสอบ close/tidy หากไม่ได้ทดสอบให้รายงานว่า live runtime ยังไม่ยืนยัน
7. ตรวจ allowlist, ไม่เพิ่ม dependency และตรวจ diff ของไฟล์ที่ tracked และ untracked เทียบ snapshot ก่อนแก้
8. สร้าง `/tmp/runs/run1/reports/fix-orca-hardening.md` พร้อม `## S-hardening` และ `## S-testing` ใส่ผลทีละ finding, คำสั่ง/exit code และช่องว่างจริง; ห้ามใส่ Status: done ก่อนผ่านเกณฑ์

หยุดงานเมื่อ F-01–F-12 มี implementation หรือหลักฐานว่ามีอยู่แล้วพร้อม regression, เอกสารตรง invariant, gates ผ่าน และรายงานแยก local proof ออกจาก runtime proof ชัดเจน
