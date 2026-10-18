package main

import (
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/providers"
)

func TestConfigThaiReportDoesNotTurnMetadataOrStaleQuotaIntoReadiness(t *testing.T) {
	remaining := 99.0
	report := configCheckText(&setupCheck{Valid: true, ModelCallsEnabled: false, QuotaGuardRequired: true,
		Providers: []setupProvider{{Harness: "codex", Installed: true}},
		Quotas:    []providers.QuotaSnapshot{{Provider: "codex", Status: "stale", AccountEvidence: "credential_metadata_match", Windows: []providers.QuotaWindow{{Name: "weekly", Remaining: &remaining}}}},
	})
	for _, want := range []string{"ยังไม่ยืนยันความพร้อมส่งงานจริง", "ปิด", "ข้อมูลเก่า", "ข้อมูลบัญชีตรงกัน", "ยังไม่ยืนยันบัญชีของ worker", "ไม่ใช่เพดานค่าใช้จ่าย"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q in %s", want, report)
		}
	}
	if strings.Contains(report, "99%") {
		t.Fatal("stale quota presented as usable allowance")
	}
}

func TestConfigThaiReportPreservesQuotaWindowsAndUnknowns(t *testing.T) {
	remaining := 0.0
	report := configCheckText(&setupCheck{ModelCallsEnabled: true, Quotas: []providers.QuotaSnapshot{
		{Provider: "claude", Status: "ok", Windows: []providers.QuotaWindow{{Name: "weekly", Remaining: &remaining}, {Name: "session"}}},
		{Provider: "opencode2", Status: "unknown"},
	}, QuotaError: "private diagnostic"})
	for _, want := range []string{"เปิด", "weekly: เหลือ 0%", "session: ไม่ทราบ", "opencode2", "อ่านโควตาไม่สำเร็จ", "ยังไม่ยืนยันความพร้อมส่งงานจริง"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q in %s", want, report)
		}
	}
	if strings.Contains(report, "private diagnostic") {
		t.Fatal("raw diagnostic leaked into human summary")
	}
}

func TestConfigThaiNativeQuotaKeepsSourceAndAccountLimits(t *testing.T) {
	remaining := 3.0
	report := configCheckText(&setupCheck{QuotaGuardRequired: true, Quotas: []providers.QuotaSnapshot{
		{Provider: "codex", Source: "native Codex app-server/account/rateLimits/read", Status: "ok", AccountEvidence: "unverified", Windows: []providers.QuotaWindow{{Name: "codex/primary", Remaining: &remaining}}},
		{Provider: "claude", Status: "unsupported"},
	}})
	for _, want := range []string{"codex/primary: เหลือ 3%", "อ่านจาก Codex โดยตรง", "ไม่ใช่ข้อมูลจาก Orca", "ไม่ยืนยันบัญชีของ worker", "ยังไม่ยืนยันความสดของข้อมูลต้นทาง", "ยังไม่มีช่องทางอ่านโควตาโดยตรงที่รองรับ", "ยังไม่ยืนยันความพร้อมส่งงานจริง"} {
		if !strings.Contains(report, want) {
			t.Fatalf("missing %q in %s", want, report)
		}
	}
}
