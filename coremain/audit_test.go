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

	data, err := os.ReadFile(filepath.Join(dir, auditSettingsFilename))
	if err != nil {
		t.Fatalf("read saved settings: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected saved settings file to be non-empty")
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

	files, err := c.listAuditLogFiles()
	if err != nil {
		t.Fatalf("list audit files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected only one retained audit file, got %d", len(files))
	}
	if filepath.Base(files[0].path) != "audit-"+now.Format("2006-01-02")+".ndjson" {
		t.Fatalf("unexpected retained file: %s", filepath.Base(files[0].path))
	}
}
