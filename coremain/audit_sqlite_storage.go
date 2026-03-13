package coremain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

type SQLiteAuditStorage struct {
	path        string
	maxDBSizeMB int
	runtimeDB   *runtimesqlite.RuntimeDB
}

func newSQLiteAuditStorage(path string, maxDBSizeMB int) *SQLiteAuditStorage {
	return &SQLiteAuditStorage{
		path:        path,
		maxDBSizeMB: maxDBSizeMB,
	}
}

func (s *SQLiteAuditStorage) Name() string { return "sqlite" }

func (s *SQLiteAuditStorage) Path() string { return s.path }

func (s *SQLiteAuditStorage) Open() error {
	if s.path == "" {
		return fmt.Errorf("sqlite audit path is required")
	}
	db, err := runtimesqlite.Open(s.path, []runtimesqlite.Migration{
		{
			ID: "0100_audit_log",
			Up: `
				CREATE TABLE IF NOT EXISTS audit_log (
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
					answer_search_text TEXT NOT NULL DEFAULT '',
					answer_ips_text TEXT NOT NULL DEFAULT '',
					answer_cnames_text TEXT NOT NULL DEFAULT '',
					domain_set TEXT NOT NULL DEFAULT ''
				);
				CREATE INDEX IF NOT EXISTS idx_audit_log_time ON audit_log(query_time_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_audit_log_query_name_time ON audit_log(query_name, query_time_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_audit_log_client_ip_time ON audit_log(client_ip, query_time_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_audit_log_domain_set_time ON audit_log(domain_set, query_time_unix_ms DESC);
			`,
		},
		{
			ID: "0101_audit_rollups",
			Up: `
				CREATE TABLE IF NOT EXISTS audit_rollup_hour (
					bucket_start_unix INTEGER NOT NULL,
					metric_name TEXT NOT NULL,
					metric_key TEXT NOT NULL,
					metric_value INTEGER NOT NULL,
					PRIMARY KEY (bucket_start_unix, metric_name, metric_key)
				);
				CREATE TABLE IF NOT EXISTS audit_rollup_day (
					bucket_start_unix INTEGER NOT NULL,
					metric_name TEXT NOT NULL,
					metric_key TEXT NOT NULL,
					metric_value INTEGER NOT NULL,
					PRIMARY KEY (bucket_start_unix, metric_name, metric_key)
				);
			`,
		},
	})
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

func (s *SQLiteAuditStorage) WriteBatch(logs []AuditLog) error {
	if len(logs) == 0 {
		return nil
	}
	if s.runtimeDB == nil {
		return fmt.Errorf("sqlite audit storage is not open")
	}

	tx, err := s.runtimeDB.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin sqlite audit tx: %w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO audit_log (
			query_time_unix_ms, client_ip, query_type, query_name, query_class, duration_ms,
			trace_id, response_code, response_flags_aa, response_flags_tc, response_flags_ra,
			answers_json, answer_search_text, answer_ips_text, answer_cnames_text, domain_set
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare sqlite audit insert: %w", err)
	}
	defer stmt.Close()

	for _, log := range logs {
		if _, err := stmt.Exec(
			log.QueryTime.UnixMilli(),
			log.ClientIP,
			log.QueryType,
			log.QueryName,
			log.QueryClass,
			log.DurationMs,
			log.TraceID,
			log.ResponseCode,
			boolToInt(log.ResponseFlags.AA),
			boolToInt(log.ResponseFlags.TC),
			boolToInt(log.ResponseFlags.RA),
			marshalAnswers(log.Answers),
			answerSearchText(log.Answers),
			answerIPsText(log.Answers),
			answerCNAMEsText(log.Answers),
			log.DomainSet,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert sqlite audit row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit tx: %w", err)
	}
	return nil
}

func (s *SQLiteAuditStorage) LoadRecent(limit int) ([]AuditLog, error) {
	if limit <= 0 || s.runtimeDB == nil {
		return nil, nil
	}
	rows, err := s.runtimeDB.DB().Query(`
		SELECT
			query_time_unix_ms, client_ip, query_type, query_name, query_class, duration_ms,
			trace_id, response_code, response_flags_aa, response_flags_tc, response_flags_ra,
			answers_json, domain_set
		FROM audit_log
		ORDER BY query_time_unix_ms DESC, id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent sqlite audit logs: %w", err)
	}
	defer rows.Close()

	recent := make([]AuditLog, 0, limit)
	for rows.Next() {
		log, err := scanAuditLogRow(rows)
		if err != nil {
			return nil, err
		}
		recent = append(recent, log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent sqlite audit logs: %w", err)
	}

	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	return recent, nil
}

func (s *SQLiteAuditStorage) QueryLogs(params V2GetLogsParams) (V2PaginatedLogsResponse, error) {
	if s.runtimeDB == nil {
		return V2PaginatedLogsResponse{
			Pagination: V2PaginationInfo{CurrentPage: params.Page, ItemsPerPage: params.Limit},
			Logs:       []AuditLog{},
		}, nil
	}
	if params.Page < 1 {
		params.Page = 1
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}

	var where []string
	var args []any

	if params.ClientIP != "" {
		where = append(where, "client_ip = ?")
		args = append(args, params.ClientIP)
	}
	if params.Domain != "" {
		where = append(where, "query_name LIKE ?")
		args = append(args, "%"+params.Domain+"%")
	}
	if params.AnswerIP != "" {
		where = append(where, "answer_ips_text LIKE ?")
		args = append(args, wrapExactPattern(params.AnswerIP))
	}
	if params.AnswerCNAME != "" {
		where = append(where, "answer_cnames_text LIKE ?")
		args = append(args, "%"+params.AnswerCNAME+"%")
	}
	if params.Q != "" {
		if params.Exact {
			where = append(where, `(query_name = ? OR client_ip = ? OR trace_id = ? OR domain_set = ? OR answer_search_text LIKE ?)`)
			args = append(args, params.Q, params.Q, params.Q, params.Q, wrapExactPattern(params.Q))
		} else {
			needle := "%" + strings.ToLower(params.Q) + "%"
			where = append(where, `(LOWER(query_name) LIKE ? OR LOWER(client_ip) LIKE ? OR LOWER(trace_id) LIKE ? OR LOWER(domain_set) LIKE ? OR LOWER(answer_search_text) LIKE ?)`)
			args = append(args, needle, needle, needle, needle, needle)
		}
	}

	baseWhere := ""
	if len(where) > 0 {
		baseWhere = "WHERE " + strings.Join(where, " AND ")
	}

	var totalItems int
	countQuery := "SELECT COUNT(*) FROM audit_log " + baseWhere
	if err := s.runtimeDB.DB().QueryRow(countQuery, args...).Scan(&totalItems); err != nil {
		return V2PaginatedLogsResponse{}, fmt.Errorf("count sqlite audit logs: %w", err)
	}

	offset := (params.Page - 1) * params.Limit
	queryArgs := append(append([]any{}, args...), params.Limit, offset)
	rows, err := s.runtimeDB.DB().Query(`
		SELECT
			query_time_unix_ms, client_ip, query_type, query_name, query_class, duration_ms,
			trace_id, response_code, response_flags_aa, response_flags_tc, response_flags_ra,
			answers_json, domain_set
		FROM audit_log
		`+baseWhere+`
		ORDER BY query_time_unix_ms DESC, id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return V2PaginatedLogsResponse{}, fmt.Errorf("query sqlite audit logs: %w", err)
	}
	defer rows.Close()

	logs := make([]AuditLog, 0, params.Limit)
	for rows.Next() {
		log, err := scanAuditLogRow(rows)
		if err != nil {
			return V2PaginatedLogsResponse{}, err
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		return V2PaginatedLogsResponse{}, fmt.Errorf("iterate sqlite audit logs: %w", err)
	}

	totalPages := int(math.Ceil(float64(totalItems) / float64(params.Limit)))
	return V2PaginatedLogsResponse{
		Pagination: V2PaginationInfo{
			TotalItems:   totalItems,
			TotalPages:   totalPages,
			CurrentPage:  params.Page,
			ItemsPerPage: params.Limit,
		},
		Logs: logs,
	}, nil
}

func (s *SQLiteAuditStorage) EnforceRetention(settings AuditSettings) error {
	if s.runtimeDB == nil {
		return nil
	}
	cutoffDay := time.Now().AddDate(0, 0, -(settings.RetentionDays - 1))
	cutoff := time.Date(cutoffDay.Year(), cutoffDay.Month(), cutoffDay.Day(), 0, 0, 0, 0, cutoffDay.Location()).UnixMilli()

	if _, err := s.runtimeDB.DB().Exec(`DELETE FROM audit_log WHERE query_time_unix_ms < ?`, cutoff); err != nil {
		return fmt.Errorf("trim sqlite audit logs by retention: %w", err)
	}

	maxBytes := int64(settings.MaxDBSizeMB) * 1024 * 1024
	if maxBytes <= 0 {
		return nil
	}
	for {
		sizeBytes, err := s.DiskUsageBytes()
		if err != nil {
			return err
		}
		if sizeBytes <= maxBytes {
			break
		}
		if _, err := s.runtimeDB.DB().Exec(`
			DELETE FROM audit_log
			WHERE id IN (
				SELECT id FROM audit_log
				ORDER BY query_time_unix_ms ASC, id ASC
				LIMIT 5000
			)
		`); err != nil {
			return fmt.Errorf("trim sqlite audit logs by size: %w", err)
		}
		if _, err := s.runtimeDB.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
			return fmt.Errorf("checkpoint sqlite audit wal: %w", err)
		}
	}
	return nil
}

func (s *SQLiteAuditStorage) Clear() error {
	if s.runtimeDB == nil {
		return nil
	}
	if _, err := s.runtimeDB.DB().Exec(`DELETE FROM audit_log`); err != nil {
		return fmt.Errorf("clear sqlite audit logs: %w", err)
	}
	if _, err := s.runtimeDB.DB().Exec(`DELETE FROM audit_rollup_hour`); err != nil {
		return fmt.Errorf("clear sqlite audit hour rollups: %w", err)
	}
	if _, err := s.runtimeDB.DB().Exec(`DELETE FROM audit_rollup_day`); err != nil {
		return fmt.Errorf("clear sqlite audit day rollups: %w", err)
	}
	if _, err := s.runtimeDB.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return fmt.Errorf("checkpoint sqlite audit wal after clear: %w", err)
	}
	return nil
}

func (s *SQLiteAuditStorage) DiskUsageBytes() (int64, error) {
	if s.runtimeDB == nil {
		return 0, nil
	}
	return s.runtimeDB.FileSizeBytes()
}

func scanAuditLogRow(scanner interface {
	Scan(dest ...any) error
}) (AuditLog, error) {
	var (
		queryTimeUnixMs int64
		log             AuditLog
		answersJSON     string
		aa              int
		tc              int
		ra              int
	)
	if err := scanner.Scan(
		&queryTimeUnixMs,
		&log.ClientIP,
		&log.QueryType,
		&log.QueryName,
		&log.QueryClass,
		&log.DurationMs,
		&log.TraceID,
		&log.ResponseCode,
		&aa,
		&tc,
		&ra,
		&answersJSON,
		&log.DomainSet,
	); err != nil {
		return AuditLog{}, fmt.Errorf("scan sqlite audit row: %w", err)
	}
	log.QueryTime = time.UnixMilli(queryTimeUnixMs)
	log.ResponseFlags = ResponseFlags{AA: aa == 1, TC: tc == 1, RA: ra == 1}
	if answersJSON != "" {
		if err := json.Unmarshal([]byte(answersJSON), &log.Answers); err != nil {
			return AuditLog{}, fmt.Errorf("unmarshal sqlite audit answers: %w", err)
		}
	}
	return log, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
