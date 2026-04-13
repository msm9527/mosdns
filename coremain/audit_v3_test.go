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

func TestSQLiteAuditStorageOverviewWindowSummaries(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = storage.Close() }()

	now := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	logs := []AuditLog{
		testAuditLog("one-hour.example", now.Add(-30*time.Minute), 2, "NOERROR", "domestic", AuditCacheHit),
		testAuditLog("one-day.example", now.Add(-23*time.Hour), 6, "NOERROR", "domestic", AuditCacheMiss),
		testAuditLog("three-day.example", now.Add(-48*time.Hour), 8, "SERVFAIL", "foreign", AuditCacheMiss),
		testAuditLog("seven-day.example", now.Add(-6*24*time.Hour), 10, "NOERROR", "foreign", AuditCacheLazy),
		testAuditLog("older.example", now.Add(-8*24*time.Hour), 12, "NOERROR", "foreign", AuditCacheMiss),
	}
	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}

	totalQueryCount, totalAverageDurationMs, err := storage.QueryOverviewTotals()
	if err != nil {
		t.Fatalf("QueryOverviewTotals() error = %v", err)
	}
	if totalQueryCount != 5 {
		t.Fatalf("totalQueryCount = %d, want 5", totalQueryCount)
	}
	if totalAverageDurationMs != 7.6 {
		t.Fatalf("totalAverageDurationMs = %.2f, want 7.60", totalAverageDurationMs)
	}

	summaries, err := storage.QueryOverviewWindowSummaries(now)
	if err != nil {
		t.Fatalf("QueryOverviewWindowSummaries() error = %v", err)
	}
	got := make(map[string]AuditPeriodSummary, len(summaries))
	for _, item := range summaries {
		got[item.Key] = item
	}

	assertAuditSummary(t, got["1h"], 1, 2)
	assertAuditSummary(t, got["24h"], 2, 4)
	assertAuditSummary(t, got["3d"], 3, 16.0/3.0)
	assertAuditSummary(t, got["7d"], 4, 6.5)
}

func assertAuditSummary(t *testing.T, item AuditPeriodSummary, wantCount uint64, wantAvg float64) {
	t.Helper()
	if item.QueryCount != wantCount {
		t.Fatalf("%s query_count = %d, want %d", item.Key, item.QueryCount, wantCount)
	}
	if diff := item.AverageDurationMs - wantAvg; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("%s average_duration_ms = %.6f, want %.6f", item.Key, item.AverageDurationMs, wantAvg)
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
