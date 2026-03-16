package coremain

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAuditLog(name string, ts time.Time) AuditLog {
	return AuditLog{
		ClientIP:     "127.0.0.1",
		QueryType:    "A",
		QueryName:    name,
		QueryClass:   "IN",
		QueryTime:    ts,
		DurationMs:   1.5,
		TraceID:      name,
		ResponseCode: "NOERROR",
		DomainSet:    "测试",
	}
}

func TestAuditCollectorSetSettingsPreservesRecentLogs(t *testing.T) {
	dir := t.TempDir()
	c := NewAuditCollector(AuditSettings{
		MemoryEntries: 3,
		RetentionDays: 7,
		MaxDiskSizeMB: 32,
	}, dir)

	now := time.Now()
	c.mu.Lock()
	c.appendLogLocked(testAuditLog("a.example", now.Add(-3*time.Minute)))
	c.appendLogLocked(testAuditLog("b.example", now.Add(-2*time.Minute)))
	c.appendLogLocked(testAuditLog("c.example", now.Add(-time.Minute)))
	c.mu.Unlock()

	if err := c.SetSettings(AuditSettings{
		MemoryEntries: 2,
		RetentionDays: 14,
		MaxDiskSizeMB: 64,
	}, dir); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	got := c.GetLogs()
	if len(got) != 2 {
		t.Fatalf("expected 2 logs after resize, got %d", len(got))
	}
	if got[0].QueryName != "b.example" || got[1].QueryName != "c.example" {
		t.Fatalf("unexpected remaining logs: %#v", got)
	}

	settings, ok, err := loadAuditSettingsFromRuntimeStore(dir)
	if err != nil {
		t.Fatalf("loadAuditSettingsFromRuntimeStore: %v", err)
	}
	if !ok || settings.MemoryEntries != 2 || settings.RetentionDays != 14 {
		t.Fatalf("unexpected saved settings: %+v ok=%v", settings, ok)
	}
}

func TestAuditCollectorRestoreFromDisk(t *testing.T) {
	dir := t.TempDir()
	settings := AuditSettings{
		MemoryEntries: 5,
		RetentionDays: 7,
		MaxDiskSizeMB: 32,
	}
	c := NewAuditCollector(settings, dir)
	now := time.Now()
	logs := []AuditLog{
		testAuditLog("one.example", now.Add(-2*time.Minute)),
		testAuditLog("two.example", now.Add(-time.Minute)),
	}
	if err := c.appendBatchToDisk(logs); err != nil {
		t.Fatalf("append batch to disk: %v", err)
	}

	restored := NewAuditCollector(settings, dir)
	if err := restored.restoreFromDisk(); err != nil {
		t.Fatalf("restore from disk: %v", err)
	}
	got := restored.GetLogs()
	if len(got) != 2 {
		t.Fatalf("expected 2 restored logs, got %d", len(got))
	}
	if got[0].QueryName != "one.example" || got[1].QueryName != "two.example" {
		t.Fatalf("unexpected restored logs: %#v", got)
	}
}

func TestAuditCollectorEnforceDiskRetentionByDays(t *testing.T) {
	dir := t.TempDir()
	settings := AuditSettings{
		MemoryEntries: 10,
		RetentionDays: 2,
		MaxDiskSizeMB: 32,
		StorageEngine: "sqlite",
	}
	c := NewAuditCollector(settings, dir)
	now := time.Now()
	if err := c.appendBatchToDisk([]AuditLog{testAuditLog("old.example", now.AddDate(0, 0, -5))}); err != nil {
		t.Fatalf("append old batch: %v", err)
	}
	if err := c.appendBatchToDisk([]AuditLog{testAuditLog("new.example", now)}); err != nil {
		t.Fatalf("append new batch: %v", err)
	}

	if err := c.enforceDiskRetention(); err != nil {
		t.Fatalf("enforce retention: %v", err)
	}

	resp, err := c.sqliteStorage.QueryLogs(V2GetLogsParams{Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("query sqlite logs: %v", err)
	}
	if len(resp.Logs) != 1 {
		t.Fatalf("expected one retained audit log, got %d", len(resp.Logs))
	}
	if resp.Logs[0].QueryName != "new.example" {
		t.Fatalf("unexpected retained log: %+v", resp.Logs[0])
	}
	if _, err := os.Stat(defaultAuditSQLitePath(dir)); err != nil {
		t.Fatalf("expected sqlite audit db to exist: %v", err)
	}
}

func TestAuditCollectorUsesSQLiteForStatsAndFirstPageLogs(t *testing.T) {
	dir := t.TempDir()
	c := NewAuditCollector(AuditSettings{
		MemoryEntries: 2,
		RetentionDays: 7,
		MaxDiskSizeMB: 32,
		StorageEngine: "sqlite",
	}, dir)

	now := time.Now()
	logs := []AuditLog{
		testAuditLog("one.example", now.Add(-2*time.Minute)),
		testAuditLog("two.example", now.Add(-time.Minute)),
	}
	logs[0].DurationMs = 2
	logs[1].DurationMs = 4
	if err := c.appendBatchToDisk(logs); err != nil {
		t.Fatalf("append batch to disk: %v", err)
	}

	stats := c.CalculateV2Stats()
	if stats.TotalQueries != 2 || stats.AverageDurationMs != 3 {
		t.Fatalf("unexpected stats: %#v", stats)
	}

	resp := c.GetV2Logs(V2GetLogsParams{Page: 1, Limit: 10})
	if resp.Pagination.TotalItems != 2 || len(resp.Logs) != 2 {
		t.Fatalf("unexpected logs response: %#v", resp)
	}
	if resp.Logs[0].QueryName != "two.example" || resp.Logs[1].QueryName != "one.example" {
		t.Fatalf("unexpected logs order: %#v", resp.Logs)
	}
}

func TestAuditCollectorResolvesRelativeSQLitePathAgainstConfigBaseDir(t *testing.T) {
	dir := t.TempDir()
	relativePath := filepath.Join("db", "audit.db")
	c := NewAuditCollector(AuditSettings{
		MemoryEntries: 2,
		RetentionDays: 7,
		MaxDiskSizeMB: 32,
		StorageEngine: "sqlite",
		SQLitePath:    relativePath,
	}, dir)

	if c.sqliteStorage == nil {
		t.Fatal("expected sqlite storage to be configured")
	}

	wantPath := filepath.Join(dir, relativePath)
	if got := c.sqliteStorage.Path(); got != wantPath {
		t.Fatalf("unexpected sqlite path: got %q want %q", got, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected sqlite audit db to exist under config dir: %v", err)
	}
}
