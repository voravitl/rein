package providers

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOrcaQuotaWindowsPrivacyAndUnknown(t *testing.T) {
	now := time.UnixMilli(1792302000000)
	raw := fmt.Sprintf(`{"ok":true,"result":{"email":"private@example.test","token":"secret","rateLimits":{"claudeTarget":{"runtime":"host"},"opencodeGoApiKeyConfigured":false,"inactiveClaudeAccounts":[],"claude":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":100}},"codex":{"status":"ok","updatedAt":%d,"session":null,"weekly":{"usedPercent":68}},"antigravity":{"status":"ok","updatedAt":%d,"buckets":[{"name":"Gemini weekly","usedPercent":4},{"name":"Claude weekly","usedPercent":94}]},"opencodeGo":{"status":"ok","updatedAt":%d,"monthly":{"usedPercent":0}}}}}`, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	snapshots, err := ParseOrcaQuota([]byte(raw), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 5 {
		t.Fatal(snapshots)
	}
	if *snapshots[0].Windows[0].Remaining != 0 || *snapshots[1].Windows[0].Remaining != 32 {
		t.Fatal(snapshots)
	}
	if *snapshots[2].Windows[0].Remaining != 96 || *snapshots[2].Windows[1].Remaining != 6 {
		t.Fatal(snapshots[2])
	}
	if snapshots[3].Status != "unknown" || len(snapshots[3].Windows) != 0 {
		t.Fatal("OpenCode Go must not imply free-model limits")
	}
	if strings.Contains(fmt.Sprint(snapshots), "secret") || strings.Contains(fmt.Sprint(snapshots), "private@example") {
		t.Fatal("private account data retained")
	}
}
func TestOrcaQuotaRejectsUntrustworthyRemaining(t *testing.T) {
	now := time.UnixMilli(1792302000000)
	for _, tc := range []struct {
		name, status string
		stamp        int64
		used         string
		want         string
	}{
		{"stale", "ok", now.Add(-6 * time.Minute).UnixMilli(), "50", "stale"},
		{"future", "ok", now.Add(2 * time.Minute).UnixMilli(), "50", "invalid"},
		{"unavailable", "error", now.UnixMilli(), "50", "unavailable"},
		{"missing percent", "ok", now.UnixMilli(), "null", "ok"},
		{"invalid percent", "ok", now.UnixMilli(), "101", "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"ok":true,"result":{"rateLimits":{"claude":{"status":%q,"updatedAt":%d,"weekly":{"usedPercent":%s}}}}}`, tc.status, tc.stamp, tc.used)
			s, e := ParseOrcaQuota([]byte(raw), now, 5*time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			if s[0].Status != tc.want || s[0].Windows[0].Remaining != nil {
				t.Fatal(s[0])
			}
		})
	}
	for _, raw := range []string{`{`, `{"ok":false}`, `{"ok":true,"result":{}}`} {
		if _, e := ParseOrcaQuota([]byte(raw), now, time.Minute); e == nil {
			t.Fatal("accepted bad envelope")
		}
	}
}
