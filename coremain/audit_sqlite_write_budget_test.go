package coremain

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newAuditBudgetTestStorage(t *testing.T, count int) (*SQLiteAuditStorage, []AuditLog) {
	t.Helper()
	s := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base := time.Now().Truncate(time.Hour)
	logs := make([]AuditLog, count)
	for i := range logs {
		logs[i] = testAuditLog(fmt.Sprintf("budget-%05d.example", i), base.Add(time.Duration(i)*time.Millisecond), 1, "NOERROR", "foreign", AuditCacheMiss)
	}
	if err := s.WriteBatch(logs); err != nil {
		t.Fatal(err)
	}
	return s, logs
}

func assertAuditBudgetCounts(t *testing.T, s *SQLiteAuditStorage, raw, total int) {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != raw {
		t.Fatalf("raw count = %d, want %d", count, raw)
	}
	for _, table := range []string{"audit_minute", "audit_hour"} {
		if err := s.DB().QueryRow(`SELECT COALESCE(SUM(query_count), 0) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != total {
			t.Fatalf("%s history count = %d, want %d", table, count, total)
		}
	}
}

func TestSQLiteAuditWriteBatchWithBudgetIsBoundedAndKeepsHistory(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 2000)
	batch := logs[1850:]
	writeErr, maintenanceErr := s.WriteBatchWithBudget(batch, 100)
	if writeErr != nil || maintenanceErr != nil {
		t.Fatalf("write = %v, maintenance = %v", writeErr, maintenanceErr)
	}
	// Crossing a 256-row step still caps the total at twice the incoming batch.
	assertAuditBudgetCounts(t, s, 2000+len(batch)-2*len(batch), 2000+len(batch))
	var oldest string
	if err := s.DB().QueryRow(`SELECT query_name FROM audit_log ORDER BY query_time_unix_ms, id LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != logs[300].QueryName {
		t.Fatalf("oldest = %s, want %s", oldest, logs[300].QueryName)
	}
}

func TestAuditCollectorCapacityFailureKeepsAcceptedBatchAndRollsBackEviction(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 5000)
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_later_eviction BEFORE DELETE ON audit_log
		WHEN OLD.id = 300 BEGIN SELECT RAISE(ABORT, 'eviction denied'); END`); err != nil {
		t.Fatal(err)
	}
	settings := defaultAuditSettings()
	settings.MaxStorageMB = 1
	c := NewAuditCollector(settings, t.TempDir())
	c.storage = s
	targetBefore := s.evictionTargetBytes
	batch := logs[4800:]
	if err := c.writeBatch(c.generation.Load(), batch); err != nil {
		t.Fatalf("accepted batch reported write failure = %v", err)
	}
	if !c.degraded.Load() {
		t.Fatal("maintenance failure must report degraded")
	}
	assertAuditBudgetCounts(t, s, 5000+len(batch), 5000+len(batch))
	if s.evictionTargetBytes != targetBefore {
		t.Fatalf("rolled-back eviction changed target = %d, want %d", s.evictionTargetBytes, targetBefore)
	}
}

func TestSQLiteAuditWriteBatchWithBudgetReportsMaintenanceSeparately(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 500)
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_eviction BEFORE DELETE ON audit_log
		BEGIN SELECT RAISE(ABORT, 'eviction denied'); END`); err != nil {
		t.Fatal(err)
	}
	writeErr, maintenanceErr := s.WriteBatchWithBudget(logs[:2], 100)
	if writeErr != nil || maintenanceErr == nil || !strings.Contains(maintenanceErr.Error(), "eviction denied") {
		t.Fatalf("write = %v, maintenance = %v", writeErr, maintenanceErr)
	}
	assertAuditBudgetCounts(t, s, 502, 502)
}

func TestSQLiteAuditWriteBatchWithBudgetCommitFailureRestoresRowsAndWatermark(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 500)
	if _, err := s.DB().Exec(`CREATE TABLE audit_budget_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE audit_budget_child (parent_id INTEGER REFERENCES audit_budget_parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_batch_commit AFTER INSERT ON audit_log
		BEGIN INSERT INTO audit_budget_child VALUES (1); END`); err != nil {
		t.Fatal(err)
	}
	before := s.evictionTargetBytes
	writeErr, maintenanceErr := s.WriteBatchWithBudget(logs[:2], 100)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "commit sqlite audit tx") || maintenanceErr != nil {
		t.Fatalf("write = %v, maintenance = %v", writeErr, maintenanceErr)
	}
	assertAuditBudgetCounts(t, s, 500, 500)
	if s.evictionTargetBytes != before {
		t.Fatalf("failed commit changed target = %d, want %d", s.evictionTargetBytes, before)
	}
}

func TestSQLiteAuditStorageBudgetClosedStorage(t *testing.T) {
	s := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "closed.db"))
	if err := s.enforceStorageBudget(1024, 10); err != nil {
		t.Fatal(err)
	}
}

func TestAuditCollectorIdleMaintenanceContinuesCapacityEviction(t *testing.T) {
	s, _ := newAuditBudgetTestStorage(t, 5000)
	settings := defaultAuditSettings()
	settings.MaxStorageMB = 1
	settings.MaintenanceIntervalSeconds = 1
	c := NewAuditCollector(settings, t.TempDir())
	c.storage = s
	go c.runMaintenance()
	t.Cleanup(func() {
		c.closed.Store(true)
		<-c.maintDone
	})
	// No writer or ingress runs. The maintenance timer must still drain backlog.
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) < 5000 })
}

func TestSQLiteAuditWriteBatchWithBudgetOuterRollbackIsWriteFailure(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 500)
	if _, err := s.DB().Exec(`CREATE TRIGGER rollback_eviction BEFORE DELETE ON audit_log
		BEGIN SELECT RAISE(ROLLBACK, 'transaction invalidated'); END`); err != nil {
		t.Fatal(err)
	}
	before := s.evictionTargetBytes
	writeErr, maintenanceErr := s.WriteBatchWithBudget(logs[:2], 100)
	// SQLite can invalidate the entire transaction, making savepoint recovery
	// impossible. Such a batch must never be reported as successfully accepted.
	if writeErr == nil || !strings.Contains(writeErr.Error(), "rollback sqlite audit capacity") || maintenanceErr != nil {
		t.Fatalf("write = %v, maintenance = %v", writeErr, maintenanceErr)
	}
	assertAuditBudgetCounts(t, s, 500, 500)
	if s.evictionTargetBytes != before {
		t.Fatalf("invalidated transaction changed target = %d, want %d", s.evictionTargetBytes, before)
	}
}
