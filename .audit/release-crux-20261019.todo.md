# Todo: ship Crux + close audit follow-ups + release v0.9.0 — 2026-10-19

## Goal

เจ้าของสั่ง: “crux ทำเป็น engine ด้วยไหม และ rein + crux ทำงานเข้าด้วยกันหรือยัง … ทำต่อให้ครบ ทำให้จบ และ released version”

ตีความ: อธิบายขอบเขต engine ให้ตรงจริง, ทำให้ Crux + Rein อยู่ใน release เดียวกัน, ปิด follow-ups ที่ค้าง, แล้วออก release ใหม่

## Exit predicate (checkable)

- [x] Working tree สะอาดและ commit ครบ; ไม่มีไฟล์ WIP ค้าง
- [x] `go test -count=1 ./...` exit 0
- [x] `go test -race -count=1 ./...` exit 0
- [x] `go vet ./...` และ `GOOS=windows go vet ./cmd/... ./internal/...` exit 0
- [x] `gofmt -l .` ว่าง
- [x] `node --test plugins/crux/adapters/codex/hooks/*.test.mjs plugins/crux/tests/*.test.mjs` ผ่าน
- [x] `python3 -B -m unittest discover -s plugins/crux/tests -p 'test_*.py'` ผ่าน
- [x] `python3 -B -m unittest discover -s scripts -p 'test*.py'` ผ่าน
- [x] `node --test test/*.test.mjs` ผ่าน
- [x] `scripts/preflight-push.sh` ผ่านบน commit สุดท้าย
- [x] `scripts/check-version-consistency.sh v0.9.0` ผ่าน
- [x] tag `v0.9.0` ชี้ commit `24a2cc9` ใน `main`; GitHub release มี asset ครบ 8 ไฟล์ (6 binaries + SHA256SUMS + `crux-0.9.0.tgz`)
- [x] Crux tarball ไม่มี `__pycache__`/test-only files และ manifest ใน tarball เป็น 0.9.0
- [x] ดาวน์โหลด `rein-darwin-arm64` + `SHA256SUMS` + `crux-0.9.0.tgz` แล้ว `./rein-darwin-arm64 version` รายงาน `rein 0.9.0`; checksum ตรง; Crux tarball manifest 0.9.0 และ clean
- [x] ไม่มีการเรียกโมเดล/provider เพิ่ม (quota/credit ยังไม่พร้อม)

## Steps

1. [x] Baseline gates บน WIP เดิม: Go full, vet, gofmt, node, python, npm, harness acceptance; race เจอ drift TempDir race จาก global graphify hook แล้วแก้ด้วย hermetic git env ใน test helper
2. [x] อัปเดต `.audit` todo/trail + handover ให้ตรงสถานะ release
3. [x] Crux: เพิ่มคำอธิบายขอบเขต “workflow plugin + shared session engine, ไม่ใช่ execution engine ที่สอง” ใน README/integration-plan
4. [x] Thai communication skill: รวมฉบับ repo + global, เพิ่มส่วนรายงานผล/หลักฐาน, copy `thai-scan.py` เข้า repo, รัน scanner
5. [x] Follow-up F-05 Windows: `scripts/install.ps1` ดาวน์โหลด release + ตรวจ `SHA256SUMS` + รองรับ `file://` สำหรับ offline test
6. [x] Follow-up version: `scripts/check-version-consistency.sh` + wire เข้า CI/release workflow
7. [x] Follow-up docs: ADR 0002 (normalized-body proof, effective-latest verdict, SHA identity), hunter help/CLI_REFERENCE, README status, ROADMAP, historical-doc banners
8. [x] Version bump 0.9.0: `package.json`, `.claude-plugin/plugin.json`, `plugin.json`, `plugins/rein/plugin.json`, และ Crux 3 manifests; ไม่ใส่ `plugins/crux/` ใน npm files (npm ไม่ได้ publish) แต่สร้าง `dist/crux-0.9.0.tgz` เป็น release asset โดยกัน `__pycache__`/test cache
9. [x] `CHANGELOG.md` + release notes (แยก verified vs ยังไม่ยืนยัน)
10. [x] Final gates ซ้ำหลังแก้ทั้งหมด + preflight
11. [x] Commit เป็นชุด (Crux / audit remediation / integration+release / docs), tag `v0.9.0`, push `main` + tag (commit bd01e0d)
12. [x] รอ release workflow (run 39185003910 success), ตรวจ assets 8 ไฟล์, ดาวน์โหลดและรัน `rein version` = 0.9.0, รายงานผลภาษาไทย

## ขอบเขตที่คงไว้ (ไม่แตะ)

- G-01 Orca recovery fence — safety boundary โดยเจตนา
- Live 4-harness / two-provider handoff / remote Orca recovery — ยังไม่มีหลักฐาน live; release notes ต้องบอกว่ายังไม่ยืนยัน
- ไม่ publish npm (ไม่ authenticated); GitHub release เป็นช่องทางที่ workflow รองรับ

## Release verification (2026-10-19)

- GitHub release: https://github.com/voravitl/rein/releases/tag/v0.9.0
- Release workflow run: 39185003910 (success)
- Assets: 8 (`rein-darwin-amd64`, `rein-darwin-arm64`, `rein-linux-amd64`, `rein-linux-arm64`, `rein-windows-amd64.exe`, `rein-windows-arm64.exe`, `crux-0.9.0.tgz`, `SHA256SUMS`)
- Local verification: `rein-darwin-arm64 version` = `rein 0.9.0`; SHA256SUMS matches; Crux tarball manifest 0.9.0 and no `__pycache__`/test-only files
- npm: not published (unauthenticated); package version in repo is 0.9.0
