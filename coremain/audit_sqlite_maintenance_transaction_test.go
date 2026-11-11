package coremain

import (
	"strings"
	"testing"
	"time"
)

func TestSQLiteAuditPeriodicCapacityRollsBackEarlierDeleteSteps(t *testing.T) {
	s, _ := newAuditBudgetTestStorage(t, 1000)
	if _, err := s.DB().Exec(`CREATE TRIGGER deny_second_capacity_step BEFORE DELETE ON audit_log
		WHEN OLD.id = 300 BEGIN SELECT RAISE(ABORT, 'periodic eviction denied'); END`); err != nil {
		t.Fatal(err)
	}
	before := s.evictionTargetBytes
	if err := s.enforceStorageBudget(100, 513); err == nil || !strings.Contains(err.Error(), "periodic eviction denied") {
		t.Fatalf("capacity error = %v, want second-step deletion failure", err)
	}
	// The first 256-row step must not remain committed after the second fails.
	assertAuditBudgetCounts(t, s, 1000, 1000)
	if s.evictionTargetBytes != before {
		t.Fatalf("failed maintenance changed target = %d, want %d", s.evictionTargetBytes, before)
	}
}

func TestSQLiteAuditRetentionRollsBackRawWhenAggregateDeleteFails(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	old := auditBufferFixture()[0]
	old.QueryTime = time.Now().Add(-48 * time.Hour)
	if err := s.WriteBatch([]AuditLog{old}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER deny_audit_retention_minute BEFORE DELETE ON audit_minute
		BEGIN SELECT RAISE(ABORT, 'retention minute delete denied'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.EnforceRetention(AuditSettings{RawRetentionDays: 1, AggregateRetentionDays: 1, MaxStorageMB: 128})
	if err == nil || !strings.Contains(err.Error(), "trim sqlite audit minute aggregates") {
		t.Fatalf("retention error = %v, want aggregate delete failure", err)
	}
	var raw, minute, hour int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_minute`).Scan(&minute); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_hour`).Scan(&hour); err != nil {
		t.Fatal(err)
	}
	if raw != 1 || minute != 1 || hour != 1 {
		t.Fatalf("retention partially committed raw=%d minute=%d hour=%d", raw, minute, hour)
	}
}

func TestSQLiteAuditPeriodicCapacityCommitFailureRestoresRowsAndWatermark(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 1000)
	if _, err := s.DB().Exec(`CREATE TABLE periodic_capacity_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE periodic_capacity_child (parent_id INTEGER REFERENCES periodic_capacity_parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER deny_capacity_commit AFTER DELETE ON audit_log
		BEGIN INSERT INTO periodic_capacity_child VALUES (1); END`); err != nil {
		t.Fatal(err)
	}
	before := s.evictionTargetBytes
	if err := s.enforceStorageBudget(100, 400); err == nil || !strings.Contains(err.Error(), "commit sqlite audit capacity tx") {
		t.Fatalf("capacity error = %v, want deferred constraint at commit", err)
	}
	assertAuditBudgetCounts(t, s, 1000, 1000)
	if s.evictionTargetBytes != before {
		t.Fatalf("failed commit changed target = %d, want %d", s.evictionTargetBytes, before)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM periodic_capacity_child`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed commit retained %d maintenance trigger rows", count)
	}
	if _, err := s.DB().Exec(`DROP TRIGGER deny_capacity_commit`); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteBatch(logs[:1]); err != nil {
		t.Fatalf("write after failed capacity commit = %v", err)
	}
	assertAuditBudgetCounts(t, s, 1001, 1001)
}

func TestSQLiteAuditPeriodicCapacityCommitsBoundedOldestRowsAndKeepsHistory(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 1000)
	const rowBudget = 513
	if err := s.enforceStorageBudget(100, rowBudget); err != nil {
		t.Fatal(err)
	}
	assertAuditBudgetCounts(t, s, 1000-rowBudget, 1000)
	var oldest string
	if err := s.DB().QueryRow(`SELECT query_name FROM audit_log ORDER BY query_time_unix_ms, id LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != logs[rowBudget].QueryName {
		t.Fatalf("oldest = %s, want %s", oldest, logs[rowBudget].QueryName)
	}
	if s.evictionTargetBytes != 85 {
		t.Fatalf("committed target = %d, want 85", s.evictionTargetBytes)
	}
	// A subsequent batch verifies that the single connection is available and
	// rollups still count accepted history rather than only retained raw rows.
	if err := s.WriteBatch(logs[:2]); err != nil {
		t.Fatalf("write after capacity commit = %v", err)
	}
	assertAuditBudgetCounts(t, s, 1000-rowBudget+2, 1002)
}
