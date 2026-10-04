package coremain

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteAuditCapacityEvictionIsBoundedAndReusesPages(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	base := time.Now().Add(-time.Hour)
	logs := make([]AuditLog, 3000)
	for i := range logs {
		logs[i] = testAuditLog(fmt.Sprintf("%05d.example", i), base.Add(time.Duration(i)*time.Second), 1, "NOERROR", "foreign", AuditCacheMiss)
	}
	if err := storage.WriteBatch(logs); err != nil {
		t.Fatal(err)
	}
	pages, free, size, err := storage.queryPageStats()
	if err != nil {
		t.Fatal(err)
	}
	budget := (pages - free) * size * 3 / 4
	if err := storage.enforceStorageBudget(budget, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := storage.DB().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(logs)-100 {
		t.Fatalf("bounded step retained %d rows, want %d", count, len(logs)-100)
	}
	reopened := false
	for i := 0; i < 40; i++ {
		if err := storage.enforceStorageBudget(budget, 100); err != nil {
			t.Fatal(err)
		}
		p, f, z, err := storage.queryPageStats()
		if err != nil {
			t.Fatal(err)
		}
		if (p-f)*z <= budget*85/100 {
			break
		}
		if !reopened && (p-f)*z < budget*90/100 {
			path := storage.Path()
			if err := storage.Close(); err != nil {
				t.Fatal(err)
			}
			storage = newSQLiteAuditStorage(path)
			if err := storage.Open(); err != nil {
				t.Fatal(err)
			}
			reopened = true
		}
		if i == 39 {
			t.Fatal("eviction did not converge to low water")
		}
	}
	if !reopened {
		t.Fatal("fixture did not exercise restart between watermarks")
	}
	var oldest string
	if err := storage.DB().QueryRow(`SELECT query_name FROM audit_log ORDER BY query_time_unix_ms,id LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest == logs[0].QueryName {
		t.Fatal("oldest record was not evicted")
	}
	p, f, _, err := storage.queryPageStats()
	if err != nil {
		t.Fatal(err)
	}
	if p != pages || f <= free {
		t.Fatalf("expected reusable free pages, before=%d/%d after=%d/%d", pages, free, p, f)
	}
	if err := storage.WriteBatch(logs[:50]); err != nil {
		t.Fatal(err)
	}
	after, _, _, err := storage.queryPageStats()
	if err != nil {
		t.Fatal(err)
	}
	if after != pages {
		t.Fatalf("small refill grew database instead of reusing pages: %d -> %d", pages, after)
	}
}

func TestSQLiteAuditCapacityIgnoresWALAndTerminatesBelowSchemaFloor(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, err := storage.DB().Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	log := testAuditLog("one.example", time.Now(), 1, "NOERROR", "foreign", AuditCacheMiss)
	if err := storage.WriteBatch([]AuditLog{log}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := storage.DB().Exec(`UPDATE audit_log SET duration_ms=?`, i); err != nil {
			t.Fatal(err)
		}
	}
	pages, free, size, err := storage.queryPageStats()
	if err != nil {
		t.Fatal(err)
	}
	budget := (pages - free) * size * 2
	wal, err := fileSizeBytes(storage.path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if wal <= budget {
		t.Fatalf("fixture WAL %d must exceed live budget %d", wal, budget)
	}
	if err := storage.enforceStorageBudget(budget, 10); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := storage.DB().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("WAL size evicted live history")
	}
	// The fixed schema cannot fit in one byte. Exhausting all tables must stop
	// with an explicit error instead of a checkpoint/VACUUM loop.
	if err := storage.enforceStorageBudget(1, 20); err == nil {
		t.Fatal("expected non-reclaimable budget error")
	}
}

func TestSQLiteAuditCapacityTrimsAggregateOnlyBacklog(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	tx, err := storage.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20000; i++ {
		if _, err := tx.Exec(`INSERT INTO audit_minute (bucket_start_unix) VALUES (?)`, i*60); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pages, free, size, err := storage.queryPageStats()
	if err != nil {
		t.Fatal(err)
	}
	budget := (pages - free) * size * 3 / 4
	if err := storage.enforceStorageBudget(budget, 123); err != nil {
		t.Fatal(err)
	}
	var count, oldest int
	if err := storage.DB().QueryRow(`SELECT COUNT(*),MIN(bucket_start_unix) FROM audit_minute`).Scan(&count, &oldest); err != nil {
		t.Fatal(err)
	}
	if count != 20000-123 || oldest != 123*60 {
		t.Fatalf("aggregate budget or oldest-first ordering violated: count=%d oldest=%d", count, oldest)
	}
}

func TestSQLiteAuditStorageCapacityReusesFreePagesWithoutDeletingLogs(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = storage.Close()
	})

	base := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	logs := []AuditLog{
		testAuditLog("alpha.example", base, 1, "NOERROR", "domestic", AuditCacheHit),
		testAuditLog("beta.example", base.Add(time.Minute), 2, "NOERROR", "foreign", AuditCacheMiss),
		testAuditLog("gamma.example", base.Add(2*time.Minute), 3, "SERVFAIL", "foreign", AuditCacheMiss),
	}
	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("storage.WriteBatch() error = %v", err)
	}

	if _, err := storage.DB().Exec(`CREATE TABLE filler (payload TEXT NOT NULL);`); err != nil {
		t.Fatalf("create filler table error = %v", err)
	}
	payload := strings.Repeat("x", 32*1024)
	for i := 0; i < 256; i++ {
		if _, err := storage.DB().Exec(`INSERT INTO filler (payload) VALUES (?)`, payload); err != nil {
			t.Fatalf("insert filler row %d error = %v", i, err)
		}
	}
	if _, err := storage.DB().Exec(`DELETE FROM filler`); err != nil {
		t.Fatalf("delete filler rows error = %v", err)
	}
	if err := storage.checkpointWAL(); err != nil {
		t.Fatalf("checkpointWAL() error = %v", err)
	}

	before, err := storage.QueryStorageStats()
	if err != nil {
		t.Fatalf("QueryStorageStats() error = %v", err)
	}
	if before.RawLogCount != int64(len(logs)) {
		t.Fatalf("before.RawLogCount = %d, want %d", before.RawLogCount, len(logs))
	}
	if before.ReclaimableBytes == 0 {
		t.Fatalf("before.ReclaimableBytes = 0, want > 0; stats = %+v", before)
	}

	maxBytes := before.LiveBytes + (before.ReclaimableBytes / 2)
	if maxBytes >= before.AllocatedBytes {
		t.Fatalf("test setup invalid: maxBytes = %d, allocated = %d", maxBytes, before.AllocatedBytes)
	}

	if err := storage.enforceMaxStorageBytes(maxBytes); err != nil {
		t.Fatalf("enforceMaxStorageBytes() error = %v", err)
	}

	after, err := storage.QueryStorageStats()
	if err != nil {
		t.Fatalf("QueryStorageStats() after error = %v", err)
	}
	if after.RawLogCount != int64(len(logs)) {
		t.Fatalf("after.RawLogCount = %d, want %d", after.RawLogCount, len(logs))
	}
	if after.AllocatedBytes != before.AllocatedBytes {
		t.Fatalf("capacity maintenance rewrote or shrank reusable storage: before=%d after=%d", before.AllocatedBytes, after.AllocatedBytes)
	}
}

func TestSQLiteAuditStorageClearCompactsDatabase(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = storage.Close()
	})

	base := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	logs := make([]AuditLog, 0, 500)
	for i := 0; i < 500; i++ {
		logs = append(logs, testAuditLog("clear.example", base.Add(time.Duration(i)*time.Second), 1, "NOERROR", "domestic", AuditCacheHit))
	}
	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("storage.WriteBatch() error = %v", err)
	}

	if _, err := storage.DB().Exec(`CREATE TABLE clear_filler (payload TEXT NOT NULL);`); err != nil {
		t.Fatalf("create clear_filler table error = %v", err)
	}
	payload := strings.Repeat("x", 32*1024)
	for i := 0; i < 256; i++ {
		if _, err := storage.DB().Exec(`INSERT INTO clear_filler (payload) VALUES (?)`, payload); err != nil {
			t.Fatalf("insert clear_filler row %d error = %v", i, err)
		}
	}
	if _, err := storage.DB().Exec(`DROP TABLE clear_filler`); err != nil {
		t.Fatalf("drop clear_filler table error = %v", err)
	}
	if err := storage.checkpointWAL(); err != nil {
		t.Fatalf("checkpointWAL() error = %v", err)
	}

	before, err := storage.QueryStorageStats()
	if err != nil {
		t.Fatalf("QueryStorageStats() error = %v", err)
	}
	if before.AllocatedBytes < 4*1024*1024 {
		t.Fatalf("test setup did not grow database enough: before = %+v", before)
	}

	if err := storage.Clear(); err != nil {
		t.Fatalf("storage.Clear() error = %v", err)
	}

	after, err := storage.QueryStorageStats()
	if err != nil {
		t.Fatalf("QueryStorageStats() after error = %v", err)
	}
	if after.RawLogCount != 0 {
		t.Fatalf("after.RawLogCount = %d, want 0", after.RawLogCount)
	}
	if after.AllocatedBytes >= before.AllocatedBytes/2 {
		t.Fatalf("after.AllocatedBytes = %d, want less than half of before %d", after.AllocatedBytes, before.AllocatedBytes)
	}
	if after.ReclaimableBytes > 1024*1024 {
		t.Fatalf("after.ReclaimableBytes = %d, want <= 1MiB; stats = %+v", after.ReclaimableBytes, after)
	}
}
