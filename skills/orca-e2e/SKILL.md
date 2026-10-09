---
name: orca-e2e
description: "Automated End-to-End (E2E) browser testing and executive Board-PDF reporting powered by Orca Browser. Use whenever verifying web apps, SPAs, routes, forms, dashboards, or HTTP interfaces end-to-end, or when asked to run UI tests and produce a board-style PDF report with real screenshots like skill /board-pdf. Executes real browser interactions via Orca, captures accessibility snapshots, verifies DOM assertions, and automatically compiles multi-board landscape PDF reports."
---

# Orca Browser E2E Testing & Board-PDF Reporting

## Overview
This skill enables agents and coordinators to verify web applications end-to-end using Orca's native browser engine. Every test execution automatically compiles an executive-ready **Board-style PDF Report** (matching the `/board-pdf` design system: cover with KPIs, step execution flow strip, full-page screenshots with findings, and failure root cause analysis).

---

## 1. Quick Invocations

### Smoke Test a Live URL (1-Liner)
Runs an automated smoke scenario (opens URL, inspects accessibility tree, takes visual screenshot, and builds the Board PDF):
```sh
rein e2e test http://localhost:3000 --out report/e2e
```

### Run a Complete E2E Scenario Spec
Executes a defined test scenario and compiles `report.pdf`:
```sh
rein e2e run tests/e2e/flow.json --out report/e2e
```

### Check Browser Runtime Readiness
```sh
rein e2e check --json
```

---

## 2. Writing an E2E Scenario Spec

Specs can be saved as `.json` or embedded directly inside Markdown files (e.g. `tests/e2e/spec.md`) using ````json ... ```` blocks.

```json
{
  "name": "User Registration and Dashboard Flow",
  "base_url": "http://localhost:3000",
  "timeout_seconds": 30,
  "steps": [
    {
      "action": "goto",
      "url": "/login",
      "description": "เปิดหน้าเข้าสู่ระบบ"
    },
    {
      "action": "assert_title",
      "value": "Login",
      "description": "ตรวจสอบชื่อหัวเรื่องหน้าเว็บ"
    },
    {
      "action": "fill",
      "target": "Username",
      "value": "admin",
      "description": "กรอกชื่อผู้ใช้งาน"
    },
    {
      "action": "fill",
      "target": "Password",
      "value": "secret123",
      "description": "กรอกรหัสผ่าน"
    },
    {
      "action": "click",
      "target": "Sign In",
      "description": "กดปุ่มเข้าสู่ระบบ"
    },
    {
      "action": "wait",
      "value": "Welcome to Dashboard",
      "timeout_ms": 5000,
      "description": "รอหน้าแดชบอร์ดโหลดเสร็จสมบูรณ์"
    },
    {
      "action": "screenshot",
      "filename": "dashboard-verified.png",
      "description": "บันทึกภาพหน้าจอหน้าแดชบอร์ด"
    }
  ]
}
```

### Supported Step Actions:
- `goto`: นำทางไปยัง URL (รองรับทั้ง full URL หรือ relative path ร่วมกับ `base_url`)
- `snapshot`: บันทึก Accessibility Snapshot ของหน้าปัจจุบัน
- `assert_text`: ตรวจสอบว่ามีข้อความที่ระบุอยู่ใน Accessibility Tree หรือไม่
- `assert_title`: ตรวจสอบว่า Title ของหน้าเว็บตรงตามที่ระบุหรือไม่
- `click`: คลิก Element (ระบุเป็น `@e1`, `e1` หรือชื่อข้อความของปุ่ม/ลิงก์ เช่น `"Sign In"`)
- `fill`: กรอกข้อความลงในช่อง input (ระบุ target เป็น `@e1` หรือชื่อฟิลด์ เช่น `"Username"`)
- `keypress`: กดปุ่มคีย์บอร์ด (เช่น `Enter`, `Tab`, `Escape`)
- `eval`: รันคำสั่ง JavaScript ในเบราว์เซอร์ พร้อมตัวเลือก `assert_value`
- `wait`: หน่วงเวลา หรือรอจนกว่าข้อความ `value` จะปรากฏในหน้าเว็บ
- `screenshot`: จับภาพหน้าจอแบบ PNG บันทึกลงในไดเรกทอรี Evidence

---

## 3. Executive Board-PDF Reports (อัตโนมัติ 100%)

ทุกครั้งที่รัน `rein e2e test` หรือ `rein e2e run`:
1. ระบบจะรันคำสั่งบน Orca Browser จริง
2. สรุปผลการทดสอบลง `report.json` และ `report.md`
3. สร้างบอร์ด HTML (`doc.html`) ตามมาตรฐานดีไซน์ของ `/board-pdf`:
   - **Board 1 (Cover)**: สรุปผลการทดสอบ (KPIs, Pass/Fail, เวลารวม, Environment, Checklist)
   - **Board 2 (Flow Timeline)**: ไทม์ไลน์แสดงสถานะการทำงานทีละขั้นตอนแบบ step cards
   - **Board 3+ (Visual Evidence)**: ภาพ Screenshot จริงขนาดเต็ม พร้อมกล่องคำอธิบายและผลการตรวจสอบ
   - **Board 4 (Failure Diagnostics)**: หากพบล้มเหลว จะวิเคราะห์สาเหตุและแสดง Error Snapshot อัตโนมัติ
4. คอมไพล์เป็น `report.pdf` ผ่าน headless Chrome พร้อมสร้างภาพพรีวิว `report-preview/page-N.png`

---

## 4. การเชื่อมต่อกับ Contract & Drift Gate

เมื่อได้รับมอบหมายงานที่ต้องมี E2E:
1. ระบุ scope ใน Contract:
   ```sh
   rein contract new --name TASK --scope e2e:dashboard --report-path report/TASK.md
   ```
2. รัน E2E เพื่อสร้าง Evidence:
   ```sh
   rein e2e run tests/e2e/dashboard.json --out report/e2e
   ```
3. นำภาพจาก `report/e2e/report-preview/` และไฟล์ `report/e2e/report.pdf` ไปอ้างอิงใน `report/TASK.md`
4. รัน `rein drift TASK` เพื่อยืนยันว่าหลักฐานครบถ้วนและผ่านเกณฑ์คุณภาพ
