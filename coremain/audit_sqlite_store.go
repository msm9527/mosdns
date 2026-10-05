package coremain

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

const auditLogsDirname = "audit_logs"

type SQLiteAuditStorage struct {
	path                string
	runtimeDB           *runtimesqlite.RuntimeDB
	capacityMu          sync.Mutex
	bufferMu            sync.Mutex
	pending             []AuditLog
	pendingBytes        int64
	bufferOwner         uint64
	bufferRevision      uint64
	bufferGeneration    uint64
	ids                 *auditIDAllocator
	evictionTargetBytes int64
}

func newSQLiteAuditStorage(path string) *SQLiteAuditStorage {
	// A reopened database may be midway through bounded eviction. Resume toward
	// low water on its first capacity check rather than waiting for new traffic.
	return &SQLiteAuditStorage{path: path, evictionTargetBytes: -1, bufferOwner: auditBufferOwnerSequence.Add(1)}
}

func (s *SQLiteAuditStorage) Open() error {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	if s.runtimeDB != nil {
		return nil
	}
	if s.path == "" {
		return fmt.Errorf("sqlite audit path is required")
	}
	db, err := runtimesqlite.Open(s.path, auditSQLiteMigrations())
	if err != nil {
		return err
	}
	s.runtimeDB = db
	s.ids = auditIDsForPath(db.Path())
	if err := s.ids.syncFrom(db.DB()); err != nil {
		_ = db.Close()
		s.runtimeDB = nil
		return err
	}
	return nil
}

// Close flushes the accepted tail. On a flush failure it remains open so the
// collector can report the failure and retry without silently losing records.
func (s *SQLiteAuditStorage) Close() error {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	if s.runtimeDB == nil {
		return nil
	}
	if writeErr, _ := s.flushBufferedLocked(0); writeErr != nil {
		return writeErr
	}
	err := s.runtimeDB.Close()
	s.runtimeDB = nil
	return err
}

func (s *SQLiteAuditStorage) Path() string {
	return s.path
}

func (s *SQLiteAuditStorage) DB() *sql.DB {
	if s.runtimeDB == nil {
		return nil
	}
	return s.runtimeDB.DB()
}

func (s *SQLiteAuditStorage) DiskUsageBytes() (int64, error) {
	if s.path == "" {
		return 0, nil
	}
	return fileSetSizeBytes(s.path)
}

func fileSetSizeBytes(dbPath string) (int64, error) {
	paths := []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
	var total int64
	for _, path := range paths {
		size, err := fileSizeBytes(path)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

func auditSQLiteMigrations() []runtimesqlite.Migration {
	return []runtimesqlite.Migration{
		{
			ID: "0200_audit_v3_reset",
			Up: `
				DROP TABLE IF EXISTS audit_log;
				DROP TABLE IF EXISTS audit_rollup_hour;
				DROP TABLE IF EXISTS audit_rollup_day;
				DROP TABLE IF EXISTS audit_minute;
				DROP TABLE IF EXISTS audit_hour;

				CREATE TABLE audit_log (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					query_time_unix_ms INTEGER NOT NULL,
					client_ip TEXT NOT NULL DEFAULT '',
					query_type TEXT NOT NULL DEFAULT '',
					query_name TEXT NOT NULL DEFAULT '',
					query_class TEXT NOT NULL DEFAULT '',
					duration_ms REAL NOT NULL DEFAULT 0,
					trace_id TEXT NOT NULL DEFAULT '',
					response_code TEXT NOT NULL DEFAULT '',
					response_flags_aa INTEGER NOT NULL DEFAULT 0,
					response_flags_tc INTEGER NOT NULL DEFAULT 0,
					response_flags_ra INTEGER NOT NULL DEFAULT 0,
					answers_json TEXT NOT NULL DEFAULT '[]',
					answer_count INTEGER NOT NULL DEFAULT 0,
					answer_search_text TEXT NOT NULL DEFAULT '',
					answer_ips_text TEXT NOT NULL DEFAULT '',
					answer_cnames_text TEXT NOT NULL DEFAULT '',
					domain_set_raw TEXT NOT NULL DEFAULT '',
					domain_set_norm TEXT NOT NULL DEFAULT '',
					upstream_tag TEXT NOT NULL DEFAULT '',
					transport TEXT NOT NULL DEFAULT '',
					server_name TEXT NOT NULL DEFAULT '',
					url_path TEXT NOT NULL DEFAULT '',
					cache_status TEXT NOT NULL DEFAULT ''
				);

				CREATE TABLE audit_minute (
					bucket_start_unix INTEGER PRIMARY KEY,
					query_count INTEGER NOT NULL DEFAULT 0,
					duration_sum_ms REAL NOT NULL DEFAULT 0,
					duration_max_ms REAL NOT NULL DEFAULT 0,
					error_count INTEGER NOT NULL DEFAULT 0,
					no_response_count INTEGER NOT NULL DEFAULT 0,
					cache_hit_count INTEGER NOT NULL DEFAULT 0
				);

				CREATE TABLE audit_hour (
					bucket_start_unix INTEGER PRIMARY KEY,
					query_count INTEGER NOT NULL DEFAULT 0,
					duration_sum_ms REAL NOT NULL DEFAULT 0,
					duration_max_ms REAL NOT NULL DEFAULT 0,
					error_count INTEGER NOT NULL DEFAULT 0,
					no_response_count INTEGER NOT NULL DEFAULT 0,
					cache_hit_count INTEGER NOT NULL DEFAULT 0
				);
			`,
		},
		{
			ID: "0201_audit_v3_indexes",
			Up: `
				CREATE INDEX idx_audit_log_time_id ON audit_log(query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_domain_time ON audit_log(query_name, query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_client_time ON audit_log(client_ip, query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_domain_set_time ON audit_log(domain_set_norm, query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_rcode_time ON audit_log(response_code, query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_cache_time ON audit_log(cache_status, query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_upstream_time ON audit_log(upstream_tag, query_time_unix_ms DESC, id DESC);
				CREATE INDEX idx_audit_log_duration_time ON audit_log(duration_ms DESC, query_time_unix_ms DESC, id DESC);
			`,
		},
		{
			ID: "0202_audit_resolved_aggregates",
			Up: `
				ALTER TABLE audit_minute ADD COLUMN resolved_query_count INTEGER NOT NULL DEFAULT 0;
				ALTER TABLE audit_minute ADD COLUMN resolved_duration_sum_ms REAL NOT NULL DEFAULT 0;
				ALTER TABLE audit_minute ADD COLUMN resolved_duration_max_ms REAL NOT NULL DEFAULT 0;
				ALTER TABLE audit_hour ADD COLUMN resolved_query_count INTEGER NOT NULL DEFAULT 0;
				ALTER TABLE audit_hour ADD COLUMN resolved_duration_sum_ms REAL NOT NULL DEFAULT 0;
				ALTER TABLE audit_hour ADD COLUMN resolved_duration_max_ms REAL NOT NULL DEFAULT 0;

				UPDATE audit_minute
				SET
					resolved_query_count = COALESCE((
						SELECT COUNT(*)
						FROM audit_log
						WHERE ((query_time_unix_ms / 60000) * 60) = audit_minute.bucket_start_unix
							AND response_code IN ('NOERROR', 'NXDOMAIN')
					), 0),
					resolved_duration_sum_ms = COALESCE((
						SELECT SUM(duration_ms)
						FROM audit_log
						WHERE ((query_time_unix_ms / 60000) * 60) = audit_minute.bucket_start_unix
							AND response_code IN ('NOERROR', 'NXDOMAIN')
					), 0),
					resolved_duration_max_ms = COALESCE((
						SELECT MAX(duration_ms)
						FROM audit_log
						WHERE ((query_time_unix_ms / 60000) * 60) = audit_minute.bucket_start_unix
							AND response_code IN ('NOERROR', 'NXDOMAIN')
					), 0);

				UPDATE audit_hour
				SET
					resolved_query_count = COALESCE((
						SELECT COUNT(*)
						FROM audit_log
						WHERE ((query_time_unix_ms / 3600000) * 3600) = audit_hour.bucket_start_unix
							AND response_code IN ('NOERROR', 'NXDOMAIN')
					), 0),
					resolved_duration_sum_ms = COALESCE((
						SELECT SUM(duration_ms)
						FROM audit_log
						WHERE ((query_time_unix_ms / 3600000) * 3600) = audit_hour.bucket_start_unix
							AND response_code IN ('NOERROR', 'NXDOMAIN')
					), 0),
					resolved_duration_max_ms = COALESCE((
						SELECT MAX(duration_ms)
						FROM audit_log
						WHERE ((query_time_unix_ms / 3600000) * 3600) = audit_hour.bucket_start_unix
							AND response_code IN ('NOERROR', 'NXDOMAIN')
					), 0);
			`,
		},
	}
}

func resolveAuditDBDir(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

// The borrower restores the runtime transaction contract on every physical
// connection. Callers own Close and must return it before another DB helper.
func (s *SQLiteAuditStorage) borrowConfiguredAuditConn() (*sql.Conn, error) {
	db := s.DB()
	if db == nil {
		return nil, fmt.Errorf("sqlite audit storage is not open")
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return nil, fmt.Errorf("borrow sqlite audit connection: %w", err)
	}
	// RuntimeDB can replace its physical connection. Restore these settings
	// before any write rather than accepting SQLite's FULL default.
	for _, pragma := range []string{"PRAGMA synchronous = NORMAL", "PRAGMA foreign_keys = ON"} {
		if _, err := conn.ExecContext(context.Background(), pragma); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("configure sqlite audit connection: %w", err)
		}
	}
	return conn, nil
}
