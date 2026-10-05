package e2e_test

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/miekg/dns"
)

func (fx *serviceE2EFixture) testBufferedAuditHistory(t *testing.T, rec *e2eCaseRecorder) {
	var settings coremain.AuditSettings
	fx.getJSON(t, "/api/v3/audit/settings", &settings)
	if settings.FlushIntervalMs != 300000 {
		t.Fatalf("default audit interval=%d", settings.FlushIntervalMs)
	}
	const domain = "audit-tail.example"
	start := time.Now().Add(-time.Second)
	for i := 0; i < 3; i++ {
		requireServiceE2EAnyARecord(t, fx.queryUDP(t, domain, dns.TypeA))
	}
	query := url.Values{"from": {start.Format(time.RFC3339Nano)}, "to": {time.Now().Add(time.Minute).Format(time.RFC3339Nano)}, "domain": {domain}, "limit": {"2"}}
	endpoint := "/api/v3/audit/logs?"
	var first coremain.AuditLogsResponse
	if err := waitServiceE2EEventually(3*time.Second, func() bool {
		fx.getJSON(t, endpoint+query.Encode(), &first)
		return first.Summary.MatchedCount == 3
	}, "buffered DNS history did not become visible"); err != nil {
		t.Fatal(err)
	}
	if len(first.Logs) != 2 || first.NextCursor == "" {
		t.Fatalf("first page count=%d cursor missing=%t", len(first.Logs), first.NextCursor == "")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(fx.configDir, "db", "audit.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	diskCount := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE query_name=?`, domain).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := diskCount(); n != 0 {
		t.Fatalf("history was already persisted before buffer test=%d", n)
	}
	ids := map[int64]bool{}
	for _, log := range first.Logs {
		ids[log.ID] = true
	}
	query.Set("cursor", first.NextCursor)
	var second coremain.AuditLogsResponse
	fx.getJSON(t, endpoint+query.Encode(), &second)
	if len(second.Logs) != 1 || ids[second.Logs[0].ID] {
		t.Fatal("buffered cursor duplicated or lost records")
	}
	ids[second.Logs[0].ID] = true
	settings.FlushIntervalMs = 250
	var updated map[string]any
	fx.putJSON(t, "/api/v3/audit/settings", settings, &updated)
	if err := waitServiceE2EEventually(3*time.Second, func() bool { return diskCount() == 3 }, "settings update did not flush audit tail"); err != nil {
		t.Fatal(err)
	}
	query.Del("cursor")
	query.Set("limit", "10")
	var persisted coremain.AuditLogsResponse
	fx.getJSON(t, endpoint+query.Encode(), &persisted)
	if len(persisted.Logs) != 3 || persisted.Summary.MatchedCount != 3 {
		t.Fatal("flush changed history count")
	}
	for _, log := range persisted.Logs {
		if !ids[log.ID] {
			t.Fatal("flush changed a buffered record ID")
		}
	}
	settings.FlushIntervalMs = 300000
	fx.putJSON(t, "/api/v3/audit/settings", settings, &updated)
	rec.SetDetail("real UDP queries were searchable and pageable before disk flush, then retained identical IDs after settings-triggered persistence")
	rec.AddCheck("buffered and persisted records", "3 complete records, stable IDs, no duplicate pages")
}
