package coremain

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *SQLiteAuditStorage) QueryTimeseries(params AuditTimeseriesQuery) ([]AuditTimeseriesPoint, error) {
	db := s.DB()
	if db == nil {
		return []AuditTimeseriesPoint{}, nil
	}
	table := "audit_minute"
	if params.Step == "hour" {
		table = "audit_hour"
	}
	rows, err := db.Query(`
		SELECT bucket_start_unix, query_count, duration_sum_ms, duration_max_ms, error_count, cache_hit_count
		FROM `+table+`
		WHERE bucket_start_unix BETWEEN ? AND ?
		ORDER BY bucket_start_unix ASC
	`, params.From.Unix(), params.To.Unix())
	if err != nil {
		return nil, fmt.Errorf("query sqlite audit timeseries: %w", err)
	}
	defer rows.Close()
	points := make([]AuditTimeseriesPoint, 0, 256)
	for rows.Next() {
		var row auditAggregateRow
		if err := rows.Scan(
			&row.BucketStartUnix,
			&row.QueryCount,
			&row.DurationSumMs,
			&row.DurationMaxMs,
			&row.ErrorCount,
			&row.CacheHitCount,
		); err != nil {
			return nil, fmt.Errorf("scan sqlite audit timeseries: %w", err)
		}
		points = append(points, AuditTimeseriesPoint{
			BucketStart:       time.Unix(row.BucketStartUnix, 0),
			QueryCount:        row.QueryCount,
			AverageDurationMs: row.avgDurationMs(),
			MaxDurationMs:     row.DurationMaxMs,
			ErrorCount:        row.ErrorCount,
			CacheHitCount:     row.CacheHitCount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite audit timeseries: %w", err)
	}
	return points, nil
}

func (s *SQLiteAuditStorage) QueryRank(rankType RankType, params AuditRangeQuery) ([]AuditRankItem, error) {
	db := s.DB()
	if db == nil || params.Limit <= 0 {
		return []AuditRankItem{}, nil
	}
	column, err := auditRankColumn(rankType)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT `+column+`, COUNT(*)
		FROM audit_log
		WHERE query_time_unix_ms BETWEEN ? AND ?
		GROUP BY `+column+`
		ORDER BY COUNT(*) DESC, `+column+` ASC
		LIMIT ?
	`, params.From.UnixMilli(), params.To.UnixMilli(), params.Limit)
	if err != nil {
		return nil, fmt.Errorf("query sqlite audit rank: %w", err)
	}
	defer rows.Close()
	items := make([]AuditRankItem, 0, params.Limit)
	for rows.Next() {
		var item AuditRankItem
		if err := rows.Scan(&item.Key, &item.Count); err != nil {
			return nil, fmt.Errorf("scan sqlite audit rank row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite audit rank rows: %w", err)
	}
	return items, nil
}

func (s *SQLiteAuditStorage) QuerySlowLogs(params AuditRangeQuery) ([]AuditLog, error) {
	db := s.DB()
	if db == nil || params.Limit <= 0 {
		return []AuditLog{}, nil
	}
	rows, err := db.Query(`
		SELECT
			id, query_time_unix_ms, client_ip, query_type, query_name, query_class, duration_ms,
			trace_id, response_code, response_flags_aa, response_flags_tc, response_flags_ra,
			answers_json, answer_count, domain_set_raw, domain_set_norm, upstream_tag,
			transport, server_name, url_path, cache_status
		FROM audit_log
		WHERE query_time_unix_ms BETWEEN ? AND ?
		ORDER BY duration_ms DESC, query_time_unix_ms DESC, id DESC
		LIMIT ?
	`, params.From.UnixMilli(), params.To.UnixMilli(), params.Limit)
	if err != nil {
		return nil, fmt.Errorf("query sqlite slow audit logs: %w", err)
	}
	defer rows.Close()
	return scanAuditLogs(rows)
}

func (s *SQLiteAuditStorage) QueryLogs(params AuditLogsQuery) (AuditLogsResponse, error) {
	db := s.DB()
	if db == nil {
		return AuditLogsResponse{}, nil
	}
	where, args := buildAuditLogWhere(params)
	summary, err := s.queryLogsSummary(where, args)
	if err != nil {
		return AuditLogsResponse{}, err
	}
	cursorWhere, cursorArgs, err := buildAuditCursorWhere(params.Cursor)
	if err != nil {
		return AuditLogsResponse{}, err
	}
	if cursorWhere != "" {
		where = append(where, cursorWhere)
		args = append(args, cursorArgs...)
	}
	limit := clampAuditLimit(params.Limit)
	baseWhere, baseArgs := joinAuditWhere(where, args)
	rows, err := db.Query(`
		SELECT
			id, query_time_unix_ms, client_ip, query_type, query_name, query_class, duration_ms,
			trace_id, response_code, response_flags_aa, response_flags_tc, response_flags_ra,
			answers_json, answer_count, domain_set_raw, domain_set_norm, upstream_tag,
			transport, server_name, url_path, cache_status
		FROM audit_log
		`+baseWhere+`
		ORDER BY query_time_unix_ms DESC, id DESC
		LIMIT ?
	`, append(baseArgs, limit+1)...)
	if err != nil {
		return AuditLogsResponse{}, fmt.Errorf("query sqlite audit logs: %w", err)
	}
	defer rows.Close()
	logs, err := scanAuditLogs(rows)
	if err != nil {
		return AuditLogsResponse{}, err
	}
	nextCursor := ""
	if len(logs) > limit {
		nextCursor = encodeAuditCursor(logs[limit-1])
		logs = logs[:limit]
	}
	return AuditLogsResponse{
		Summary:    summary,
		Logs:       logs,
		NextCursor: nextCursor,
	}, nil
}

func (s *SQLiteAuditStorage) queryLogsSummary(where []string, args []any) (AuditLogsSummary, error) {
	baseWhere, baseArgs := joinAuditWhere(where, args)
	var summary AuditLogsSummary
	err := s.DB().QueryRow(`
		SELECT COUNT(*), COALESCE(AVG(duration_ms), 0), COALESCE(MAX(duration_ms), 0)
		FROM audit_log
		`+baseWhere, baseArgs...).Scan(
		&summary.MatchedCount,
		&summary.AverageDurationMs,
		&summary.MaxDurationMs,
	)
	if err != nil {
		return AuditLogsSummary{}, fmt.Errorf("query sqlite audit summary: %w", err)
	}
	return summary, nil
}

func scanAuditLogs(rows *sql.Rows) ([]AuditLog, error) {
	logs := make([]AuditLog, 0, 128)
	for rows.Next() {
		log, err := scanAuditLogRow(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite audit rows: %w", err)
	}
	return logs, nil
}

func scanAuditLogRow(scanner scanner) (AuditLog, error) {
	var (
		log             AuditLog
		queryTimeUnixMs int64
		answersJSON     string
		aa              int
		tc              int
		ra              int
	)
	if err := scanner.Scan(
		&log.ID,
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
		&log.AnswerCount,
		&log.DomainSetRaw,
		&log.DomainSetNorm,
		&log.UpstreamTag,
		&log.Transport,
		&log.ServerName,
		&log.URLPath,
		&log.CacheStatus,
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

func buildAuditLogWhere(params AuditLogsQuery) ([]string, []any) {
	where := []string{"query_time_unix_ms BETWEEN ? AND ?"}
	args := []any{params.From.UnixMilli(), params.To.UnixMilli()}
	appendFilter := func(clause string, value any) {
		where = append(where, clause)
		args = append(args, value)
	}
	if params.ClientIP != "" {
		appendFilter("client_ip = ?", params.ClientIP)
	}
	if params.Domain != "" {
		appendFilter("query_name LIKE ?", "%"+params.Domain+"%")
	}
	if params.ResponseCode != "" {
		appendFilter("response_code = ?", strings.ToUpper(params.ResponseCode))
	}
	if params.DomainSet != "" {
		appendFilter("domain_set_norm = ?", params.DomainSet)
	}
	if params.CacheStatus != "" {
		appendFilter("cache_status = ?", params.CacheStatus)
	}
	if params.UpstreamTag != "" {
		appendFilter("upstream_tag = ?", params.UpstreamTag)
	}
	if params.Transport != "" {
		appendFilter("transport = ?", params.Transport)
	}
	if params.Answer != "" {
		appendFilter("answer_search_text LIKE ?", buildAuditSearchPattern(params.Answer, params.Exact))
	}
	if params.Query != "" {
		appendAuditTextQuery(&where, &args, params.Query, params.Exact)
	}
	return where, args
}

func appendAuditTextQuery(where *[]string, args *[]any, query string, exact bool) {
	if exact {
		*where = append(*where, `(query_name = ? OR client_ip = ? OR trace_id = ? OR domain_set_norm = ? OR answer_search_text LIKE ?)`)
		*args = append(*args, query, query, query, query, wrapExactPattern(query))
		return
	}
	needle := "%" + strings.ToLower(query) + "%"
	*where = append(*where, `(LOWER(query_name) LIKE ? OR LOWER(client_ip) LIKE ? OR LOWER(trace_id) LIKE ? OR LOWER(domain_set_norm) LIKE ? OR LOWER(answer_search_text) LIKE ?)`)
	*args = append(*args, needle, needle, needle, needle, needle)
}

func buildAuditSearchPattern(value string, exact bool) string {
	if exact {
		return wrapExactPattern(value)
	}
	return "%" + value + "%"
}

func joinAuditWhere(where []string, args []any) (string, []any) {
	if len(where) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(where, " AND "), args
}

func clampAuditLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func encodeAuditCursor(log AuditLog) string {
	raw := strconv.FormatInt(log.QueryTime.UnixMilli(), 10) + ":" + strconv.FormatInt(log.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func buildAuditCursorWhere(cursor string) (string, []any, error) {
	if strings.TrimSpace(cursor) == "" {
		return "", nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", nil, fmt.Errorf("decode audit cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("invalid audit cursor")
	}
	timeMs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("parse audit cursor time: %w", err)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("parse audit cursor id: %w", err)
	}
	return "(query_time_unix_ms < ? OR (query_time_unix_ms = ? AND id < ?))", []any{timeMs, timeMs, id}, nil
}

func auditRankColumn(rankType RankType) (string, error) {
	switch rankType {
	case RankByDomain:
		return "query_name", nil
	case RankByClient:
		return "client_ip", nil
	case RankByDomainSet:
		return "domain_set_norm", nil
	default:
		return "", fmt.Errorf("unsupported audit rank type: %s", rankType)
	}
}

type RankType string

const (
	RankByDomain    RankType = "domain"
	RankByClient    RankType = "client"
	RankByDomainSet RankType = "domain_set"
)

type scanner interface {
	Scan(dest ...any) error
}
