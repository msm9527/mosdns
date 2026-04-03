package coremain

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteAuditStorageEnforceMaxStorageBytesCompactsBeforeDeletingLogs(t *testing.T) {
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
	if after.AllocatedBytes > maxBytes {
		t.Fatalf("after.AllocatedBytes = %d, want <= %d", after.AllocatedBytes, maxBytes)
	}
}
