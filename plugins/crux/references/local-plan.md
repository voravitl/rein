# เตรียมแผนงานในเครื่องด้วย Rein

ใช้ schema 2 ของ Rein ตาม [CLI reference](../../../docs/CLI_REFERENCE.md#checkpoint-report-only-hunter-and-bounded-supervisor) โดยไม่สร้าง scheduler หรือ ledger เพิ่ม ตัวอย่างด้านล่างเป็นแม่แบบ ต้องแทนค่าจาก contract/config จริงก่อนตรวจ ไม่ใช่หลักฐานว่า provider พร้อมทำงาน

## เตรียมข้อมูล

ติดตั้ง guard/hooks ของ task ให้เสร็จก่อนอ่าน hash เพราะการติดตั้งอาจเปลี่ยน contract เก็บแผนที่ผูกเครื่องไว้ใน run directory ไม่ commit path ส่วนตัวหรือข้อมูลบัญชี

| ค่าในแผน | แหล่งข้อมูลจริง |
| --- | --- |
| `task` | ชื่อ contract ที่สร้างให้ worktree นี้ |
| `repo` | repository ของ run ใช้ absolute path ที่ resolve แล้ว |
| `contract_hash` | ผล `rein contract hash TASK` ไม่ใช่ SHA256 ของไฟล์ contract ดิบ |
| `profile` | task profile ที่วิเคราะห์ scope แล้ว ใช้ `contract_hash` เดียวกัน ห้ามระบุ model/provider เพื่อข้าม routing |
| `config_path` | absolute path ของ config ที่ผ่านการตรวจ policy/billing แล้ว |
| `config_digest` | SHA256 ของ config bytes จริง เช่น `shasum -a 256 CONFIG` แล้วใช้เฉพาะ digest |
| `base` | full SHA จาก `git -C WORKTREE rev-parse HEAD` |
| `pools` | canonical quota pools ของ provider ทุกตัวใน `launches` ไม่ใช่ billing pools |
| `launches` | argv ที่อนุญาตไว้สำหรับ provider keys ใน config มี literal prompt เดียว และเลือก model ผ่าน receipt |
| `verify` | argv ทดสอบพฤติกรรมงานจริงใน scope ไม่ใช้เพียงข้อความจาก worker |

อ่านรายละเอียด contract ได้จากไฟล์ที่ `rein contract path TASK` คืนให้ ห้ามคัดลอก hash จากงานหรือ worktree อื่น

## ตรวจงบก่อนเรียกโมเดล

การตรวจแผนด้วย `rein supervisor validate` ไม่เรียกโมเดลและไม่ได้ยืนยันว่ามีเพดานค่าใช้จ่ายพร้อมใช้ ส่วน `rein route auto` อาจเรียก availability probe หลังจองงบ จึงไม่ใช้คำสั่งนี้เป็นการตรวจแบบไม่ใช้โควตา

| สิ่งที่ต้องตรวจ | เกณฑ์สำหรับ Crux ที่คุมค่าใช้จ่าย |
| --- | --- |
| เจ้าของไม่ให้ใช้โควตาเพิ่ม | ตรวจ source/fixture ในเครื่องเท่านั้น ไม่เรียก probe, calibration, worker หรือ agent เพิ่ม |
| budget และ pool caps | ต้องมีเพดานบวกที่เจ้าของกำหนด ครอบคลุม billing pools ของ probe, worker และ review ที่จะใช้; ไม่ใช่เฉพาะ quota pools ในแผน |
| ค่า `0` หรือไม่มีค่า | ใน ordinary budget หมายถึงไม่มีเพดานนั้น ไม่ใช่งบศูนย์ ถ้าไม่มีเพดานอื่นที่ใช้ได้ ให้ถือว่ายังไม่พร้อม dispatch |
| ต้นทุนและโควตา | ตรวจ billing mode/หน่วย/บัญชี/ช่วงเวลาและ probe/funding bound จากข้อมูลจริง ไม่ถือว่า subscription เท่ากับไม่มีต้นทุน และไม่แปลงหน่วยเอง |
| งานหลายตัวหรือ retry | รวมยอดที่ใช้แล้วและ reservation ที่ยังค้างด้วย ไม่ตั้งงบแยกจนใช้ pool เดียวซ้ำ; retry เป็นการตัดสินใจและจองใหม่ |
| Calibration | ต้องมี authorization และ caps เฉพาะของมัน ไม่ถือว่ามีสิทธิ์เพียงเพราะ discover พบ model |

หลักฐานพฤติกรรมอยู่ใน [ตัวตรวจ cap](../../../internal/budget/budget.go), [การจองงบ](../../../internal/budget/reserve.go), [เส้นทางเลือกและ probe](../../../internal/routing/auto.go) และ [ชุดทดสอบงบ](../../../internal/budget/reserve_test.go) การจองงบของ Rein ป้องกันการจองเกินเพดานที่ตั้งไว้ แต่ไม่ใช่ provider-side spending cap และไม่ควบคุมคำขอที่ส่งข้าม Rein ข้อมูล usage ที่ไม่ทราบต้องคงเป็นไม่ทราบ ไม่รายงานเป็นศูนย์

## แม่แบบงานเดียว

แทนข้อความตัวพิมพ์ใหญ่ทุกจุด และแทนทั้ง object `profile` ด้วย task profile จริง ตัวอย่างนี้ใช้ Codex; harness อื่นต้องใช้ argv ของ adapter ที่ตรวจไว้แล้ว ห้ามนำ flags ข้าม harness

```json
{
  "schema": 2,
  "run": "crux-pilot",
  "repo": "ABSOLUTE_REPOSITORY",
  "terminal": "ACTUAL_COORDINATOR_HANDLE",
  "source": "crux",
  "version": "1",
  "max_checks": 120,
  "timeout_ms": 300000,
  "poll_interval_ms": 1000,
  "pools": ["CANONICAL_QUOTA_POOL"],
  "tasks": [{
    "task": "TASK",
    "process_policy": "non_detaching",
    "contract_hash": "CURRENT_CONTRACT_HASH",
    "profile": {"contract_hash": "CURRENT_CONTRACT_HASH", "phase": "worker"},
    "config_path": "ABSOLUTE_CONFIG_PATH",
    "config_digest": "CURRENT_CONFIG_BYTES_SHA256",
    "base": "FULL_BASE_SHA",
    "max_attempts": 1,
    "launches": {"CONFIG_PROVIDER_KEY": ["codex", "exec", "--model", "{model}", "APPROVED_LITERAL_TASK_PROMPT"]},
    "verify": [["ACTUAL_TEST_EXECUTABLE", "ACTUAL_TEST_ARGUMENT"]]
  }]
}
```

`profile` ในแม่แบบยังไม่ครบสำหรับ PrepareAuto ต้องมีชนิดงาน ความสามารถที่ต้องใช้ context และ planned files พร้อมเหตุผลตาม schema routing จริง หากต้องใช้ effort ให้ใส่รูปแบบที่ CLI รุ่นนั้นรองรับและตรงกับ receipt ไม่เติม flag โดยเดา

## ตรวจและส่งต่อ

1. รัน `rein supervisor validate --plan PLAN` ต้องผ่านรูปแบบ ขอบเขต argv และ hash ปัจจุบันของ contract/config/profile การผ่านขั้นนี้ยังไม่ยืนยัน auth, quota, quality evidence หรือสิทธิ์เปิด worker
2. ใช้ `rein run start REPO --run RUN --session SESSION --pid OWNER_PID --supervisor-plan PLAN` ด้วย coordinator ที่ยังมีชีวิตและ identity จริง อนุญาต task ผ่านขั้นตอน `rein run allow` เดิมก่อนรัน ห้ามใช้ PID เก่าหรือแก้ marker เอง
3. รัน `rein supervisor run --plan PLAN` ระบบตรวจ authority ซ้ำก่อน PrepareAuto และตรวจ receipt ก่อน launch ถ้าแก้ plan หลัง bind ต้องเตรียม run ใหม่ตามขั้นตอนเดิม ไม่แก้ hash ใน marker เพื่อให้ผ่าน
4. เมื่อได้ `WAITING_HUMAN` ให้ reviewer ที่แยกจากผู้เขียนตรวจ revision จริงผ่าน workflow review ของ Rein แล้วจึงเข้าขั้นอนุมัติของคน

Graph production รับเฉพาะ `phase: worker` การตั้ง task ชื่อ reviewer ไม่ทำให้งานนั้นเป็น independent review งานแต่ละ task ต้องมี worktree ต่างกัน และ `depends_on` ระบุลำดับเท่านั้น ไม่ได้ส่ง diff ข้าม worktree การส่งงานตรวจต้องระบุ revision/checkpoint และเตรียมขอบเขตของผู้ตรวจผ่าน workflow เดิมอย่างชัดเจน

หลักฐานจบงานต้องแยกแผนผ่าน validator, model เรียกได้, งานผ่าน tests, independent review และ remote lifecycle ออกจากกัน หาก provider ติดโควตาหรือยังพิสูจน์การหยุด worker ไม่ได้ ให้รายงานข้อจำกัดจริงและคง fence ของ Rein
