package coremain

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

const auditLogsDirname = "audit_logs"

type SQLiteAuditStorage struct {
	path      string
	runtimeDB *runtimesqlite.RuntimeDB
}

func newSQLiteAuditStorage(path string) *SQLiteAuditStorage {
	return &SQLiteAuditStorage{path: path}
}

func (s *SQLiteAuditStorage) Open() error {
	if s.path == "" {
		return fmt.Errorf("sqlite audit path is required")
	}
	db, err := runtimesqlite.Open(s.path, auditSQLiteMigrations())
	if err != nil {
		return err
	}
	s.runtimeDB = db
	return nil
}

func (s *SQLiteAuditStorage) Close() error {
	if s.runtimeDB == nil {
		return nil
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
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		total += info.Size()
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
	}
}

func resolveAuditDBDir(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}
