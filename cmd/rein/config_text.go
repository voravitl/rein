package main

import (
	"fmt"
	"strings"
)

func configCheckText(c *setupCheck) string {
	var b strings.Builder
	state := func(v bool) string {
		if v {
			return "เปิด"
		}
		return "ปิด"
	}
	fmt.Fprintln(&b, "ผลตรวจการตั้งค่า Rein + Crux")
	fmt.Fprintln(&b, "สถานะ: ยังไม่ยืนยันความพร้อมส่งงานจริง")
	fmt.Fprintf(&b, "การเรียกโมเดล: %s | ตรวจโควตาก่อนเรียก: %s\n", state(c.ModelCallsEnabled), state(c.QuotaGuardRequired))
	fmt.Fprintln(&b, "การอ่านไฟล์ตั้งค่าได้ไม่ยืนยันบัญชี สิทธิ์ งบ หรือการส่งงานจริง")
	for _, p := range c.Providers {
		cli, probe, model := "ไม่พบ CLI", "ยังไม่ตั้งคำสั่งตรวจโมเดล", p.Model
		if p.Installed {
			cli = "พบ CLI"
		}
		if p.ProbeConfigured {
			probe = "ตั้งคำสั่งตรวจโมเดลแล้ว"
		}
		if model == "" {
			model = "ยังไม่เลือกโมเดล"
		}
		fmt.Fprintf(&b, "- %s: %s | %s | %s\n", p.Harness, cli, model, probe)
	}
	if c.QuotaError != "" {
		fmt.Fprintln(&b, "อ่านโควตาไม่สำเร็จ ตรวจรายละเอียดด้วยผล JSON")
	}
	if len(c.Quotas) == 0 && c.QuotaError == "" {
		fmt.Fprintln(&b, "ยังไม่ได้อ่านโควตา ใช้ --quota orca หรือ --quota native (Codex) เพื่อตรวจโดยไม่เรียกโมเดล")
	}
	for _, q := range c.Quotas {
		status := map[string]string{"ok": "มีข้อมูลล่าสุด", "stale": "ข้อมูลเก่า ใช้ยืนยันโควตาปัจจุบันไม่ได้", "unknown": "ไม่ทราบโควตา", "invalid": "ข้อมูลไม่ถูกต้อง", "unavailable": "ไม่มีข้อมูลที่ใช้ได้", "unsupported": "ยังไม่มีช่องทางอ่านโควตาโดยตรงที่รองรับ"}[q.Status]
		if status == "" {
			status = "ไม่ทราบโควตา"
		}
		nativeCodex := strings.HasPrefix(q.Source, "native Codex")
		if nativeCodex && q.Status == "ok" {
			status = "อ่านจาก CLI สำเร็จ ยังไม่ยืนยันความสดของข้อมูลต้นทาง"
		}
		fmt.Fprintf(&b, "- โควตา %s: %s\n", q.Provider, status)
		if nativeCodex {
			fmt.Fprintln(&b, "  อ่านจาก Codex โดยตรง ไม่ใช่ข้อมูลจาก Orca")
		}
		switch q.AccountEvidence {
		case "credential_metadata_match":
			fmt.Fprintln(&b, "  ข้อมูลบัญชีตรงกัน แต่ยังไม่ยืนยันบัญชีของ worker หรือบัญชีที่ผูกกับโควตา")
		case "credential_metadata_mismatch":
			fmt.Fprintln(&b, "  ข้อมูลบัญชีไม่ตรงกัน ระบบจะหยุดก่อนเรียกโมเดลเมื่อเปิดการตรวจโควตา")
		default:
			fmt.Fprintln(&b, "  ยังไม่ยืนยันบัญชีของ worker")
		}
		if q.Status == "ok" {
			for _, w := range q.Windows {
				if w.Remaining == nil {
					fmt.Fprintf(&b, "  %s: ไม่ทราบ\n", w.Name)
				} else {
					fmt.Fprintf(&b, "  %s: เหลือ %g%%\n", w.Name, *w.Remaining)
				}
			}
		}
	}
	fmt.Fprintln(&b, "งบที่ไม่ระบุหรือเป็นศูนย์ไม่ใช่เพดานค่าใช้จ่าย และโควตาที่เหลือไม่ใช่สิทธิ์ให้ใช้เงิน")
	fmt.Fprintln(&b, "แนะนำตรวจบัญชี โมเดล งบ และสัญญางานก่อนเปิดเรียกโมเดล ข้อดีคือลดการเรียกผิดบัญชี ข้อจำกัดคือต้องพิสูจน์ด้วย worker จริงเพิ่มเติม")
	return b.String()
}
