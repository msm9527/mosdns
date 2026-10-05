package coremain

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

type auditCheckpointPolicy struct {
	pages       int
	synchronous int
	foreignKeys int
}

func readAuditCheckpointPolicy(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) auditCheckpointPolicy {
	t.Helper()
	var policy auditCheckpointPolicy
	for pragma, dest := range map[string]*int{
		"wal_autocheckpoint": &policy.pages,
		"synchronous":        &policy.synchronous,
		"foreign_keys":       &policy.foreignKeys,
	} {
		if err := db.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	return policy
}

func replaceAuditCheckpointConnection(t *testing.T, s *SQLiteAuditStorage) {
	t.Helper()
	db := s.DB()
	db.SetMaxIdleConns(0)
	db.SetMaxIdleConns(1)
	// These connection-local defaults prove that the configured connection was
	// discarded, so the next audit operation must establish its own policy.
	want := auditCheckpointPolicy{pages: 1000, synchronous: 2, foreignKeys: 0}
	if got := readAuditCheckpointPolicy(t, db); got != want {
		t.Fatalf("replacement policy = %+v, want %+v", got, want)
	}
}

func TestAuditCheckpointPolicyRestoresReplacementAndPersistsTail(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	wantPolicy := auditCheckpointPolicy{pages: 8192, synchronous: 1, foreignKeys: 1}

	for attempt := 0; attempt < 2; attempt++ {
		replaceAuditCheckpointConnection(t, s)
		conn, err := s.borrowConfiguredAuditConn()
		if err != nil {
			t.Fatal(err)
		}
		got := readAuditCheckpointPolicy(t, conn)
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		if got != wantPolicy {
			t.Fatalf("borrowed physical connection policy = %+v, want %+v", got, wantPolicy)
		}
	}

	fixture := auditBufferFixture()
	logs := fixture[:4]
	replaceAuditCheckpointConnection(t, s)
	if err := s.WriteBatch(logs[:2]); err != nil {
		t.Fatal(err)
	}
	if got := readAuditCheckpointPolicy(t, s.DB()); got != wantPolicy {
		t.Fatalf("batch connection policy = %+v, want %+v", got, wantPolicy)
	}
	if err := s.StageBufferedLog(logs[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryLogs(auditBufferAllQuery()); err != nil {
		t.Fatal(err)
	}
	replaceAuditCheckpointConnection(t, s)
	if writeErr, maintenanceErr := s.FlushBufferedWithBudget(128 * 1024 * 1024); writeErr != nil || maintenanceErr != nil {
		t.Fatalf("flush = %v, maintenance = %v", writeErr, maintenanceErr)
	}
	if got := readAuditCheckpointPolicy(t, s.DB()); got != wantPolicy {
		t.Fatalf("flush connection policy = %+v, want %+v", got, wantPolicy)
	}
	if err := s.StageBufferedLog(logs[3]); err != nil {
		t.Fatal(err)
	}
	before := snapshotAuditBuffer(t, s, logs)
	if before.Count != int64(len(logs)) {
		t.Fatalf("visible accepted records = %d, want %d", before.Count, len(logs))
	}
	// Close must reconfigure its replacement connection and persist the ordinary
	// tail before releasing the database, including its aggregate updates.
	replaceAuditCheckpointConnection(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newSQLiteAuditStorage(s.Path())
	if err := reopened.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	after := snapshotAuditBuffer(t, reopened, logs)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("close and reopen changed accepted records, pages, searches or aggregates")
	}
	if err := reopened.WriteBatch(fixture[4:5]); err != nil {
		t.Fatal(err)
	}
	if got := readAuditCheckpointPolicy(t, reopened.DB()); got != wantPolicy {
		t.Fatalf("reopened audit write policy = %+v, want %+v", got, wantPolicy)
	}
	page, err := reopened.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 5 || page.Summary.MatchedCount != 5 {
		t.Fatal("reopened audit write lost or duplicated records")
	}
}

func TestAuditCheckpointPolicyDoesNotChangeControlDatabase(t *testing.T) {
	control, err := runtimesqlite.Open(filepath.Join(t.TempDir(), "control.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	initial := auditCheckpointPolicy{pages: 1000, synchronous: 1, foreignKeys: 1}
	if got := readAuditCheckpointPolicy(t, control.DB()); got != initial {
		t.Fatalf("control runtime policy = %+v, want %+v", got, initial)
	}
	// A control connection owns its own policy, even while audit connections
	// are replaced and restored in the same process.
	if _, err := control.DB().Exec("PRAGMA wal_autocheckpoint = 73; PRAGMA synchronous = FULL; PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	wantControl := auditCheckpointPolicy{pages: 73, synchronous: 2, foreignKeys: 0}
	s := newAuditBufferTestStorage(t)
	replaceAuditCheckpointConnection(t, s)
	if err := s.WriteBatch(auditBufferFixture()[:2]); err != nil {
		t.Fatal(err)
	}
	if got := readAuditCheckpointPolicy(t, s.DB()); got != (auditCheckpointPolicy{pages: 8192, synchronous: 1, foreignKeys: 1}) {
		t.Fatalf("audit write policy = %+v", got)
	}
	if got := readAuditCheckpointPolicy(t, control.DB()); got != wantControl {
		t.Fatalf("audit write changed control policy = %+v, want %+v", got, wantControl)
	}
}
