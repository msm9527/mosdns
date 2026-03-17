package coremain

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAuditRealtimeOverviewSnapshot(t *testing.T) {
	store := newAuditRealtimeStore(16)
	now := time.Now().Truncate(time.Second)

	store.Record(AuditLog{
		QueryTime:    now.Add(-2 * time.Second),
		DurationMs:   4,
		ResponseCode: "NOERROR",
		CacheStatus:  AuditCacheHit,
	})
	store.Record(AuditLog{
		QueryTime:    now.Add(-time.Second),
		DurationMs:   8,
		ResponseCode: "SERVFAIL",
		CacheStatus:  AuditCacheMiss,
	})
	store.RecordDrop(now)

	overview := store.Snapshot(5)
	if overview.QueryCount != 2 {
		t.Fatalf("QueryCount = %d, want 2", overview.QueryCount)
	}
	if overview.AverageDurationMs != 6 {
		t.Fatalf("AverageDurationMs = %.2f, want 6", overview.AverageDurationMs)
	}
	if overview.MaxDurationMs != 8 {
		t.Fatalf("MaxDurationMs = %.2f, want 8", overview.MaxDurationMs)
	}
	if overview.ErrorCount != 1 {
		t.Fatalf("ErrorCount = %d, want 1", overview.ErrorCount)
	}
	if overview.CacheHitCount != 1 {
		t.Fatalf("CacheHitCount = %d, want 1", overview.CacheHitCount)
	}
	if overview.DroppedEvents != 1 {
		t.Fatalf("DroppedEvents = %d, want 1", overview.DroppedEvents)
	}
}

func TestSQLiteAuditStorageWritesLogsQueryAndTimeseries(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = storage.Close() }()

	base := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	logs := []AuditLog{
		testAuditLog("one.example", base.Add(10*time.Second), 2, "NOERROR", "domestic", AuditCacheHit),
		testAuditLog("two.example", base.Add(70*time.Second), 5, "SERVFAIL", "foreign", AuditCacheMiss),
		testAuditLog("three.example", base.Add(80*time.Second), 8, "NOERROR", "foreign", AuditCacheLazy),
	}

	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}

	resp, err := storage.QueryLogs(AuditLogsQuery{
		From:  base,
		To:    base.Add(3 * time.Minute),
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("QueryLogs() error = %v", err)
	}
	if resp.Summary.MatchedCount != 3 {
		t.Fatalf("MatchedCount = %d, want 3", resp.Summary.MatchedCount)
	}
	if len(resp.Logs) != 2 {
		t.Fatalf("len(resp.Logs) = %d, want 2", len(resp.Logs))
	}
	if resp.Logs[0].QueryName != "three.example" || resp.Logs[1].QueryName != "two.example" {
		t.Fatalf("unexpected logs order: %+v", resp.Logs)
	}
	if resp.NextCursor == "" {
		t.Fatal("expected next cursor")
	}

	next, err := storage.QueryLogs(AuditLogsQuery{
		From:   base,
		To:     base.Add(3 * time.Minute),
		Limit:  2,
		Cursor: resp.NextCursor,
	})
	if err != nil {
		t.Fatalf("QueryLogs(next) error = %v", err)
	}
	if len(next.Logs) != 1 || next.Logs[0].QueryName != "one.example" {
		t.Fatalf("unexpected next page logs: %+v", next.Logs)
	}

	points, err := storage.QueryTimeseries(AuditTimeseriesQuery{
		From: base.Truncate(time.Minute),
		To:   base.Add(3 * time.Minute),
		Step: "minute",
	})
	if err != nil {
		t.Fatalf("QueryTimeseries() error = %v", err)
	}
	if len(points) < 2 {
		t.Fatalf("expected at least 2 timeseries points, got %d", len(points))
	}

	rank, err := storage.QueryRank(RankByDomainSet, AuditRangeQuery{
		From:  base,
		To:    base.Add(3 * time.Minute),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryRank() error = %v", err)
	}
	if len(rank) == 0 || rank[0].Key == "" {
		t.Fatalf("unexpected rank result: %+v", rank)
	}

	totalQueryCount, totalAverageDurationMs, err := storage.QueryOverviewTotals()
	if err != nil {
		t.Fatalf("QueryOverviewTotals() error = %v", err)
	}
	if totalQueryCount != 3 {
		t.Fatalf("totalQueryCount = %d, want 3", totalQueryCount)
	}
	if totalAverageDurationMs != 5 {
		t.Fatalf("totalAverageDurationMs = %.2f, want 5", totalAverageDurationMs)
	}
}

func testAuditLog(name string, at time.Time, duration float64, rcode, domainSet, cacheStatus string) AuditLog {
	return AuditLog{
		QueryTime:     at,
		ClientIP:      "127.0.0.1",
		QueryType:     "A",
		QueryName:     name,
		QueryClass:    "IN",
		DurationMs:    duration,
		TraceID:       name,
		ResponseCode:  rcode,
		AnswerCount:   1,
		Answers:       []AnswerDetail{{Type: "A", TTL: 60, Data: "1.1.1.1"}},
		DomainSetRaw:  domainSet,
		DomainSetNorm: normalizeAuditDomainSet(domainSet, "A"),
		CacheStatus:   cacheStatus,
	}
}
