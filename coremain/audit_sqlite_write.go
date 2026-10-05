package coremain

import (
	"database/sql"
	"fmt"
	"time"
)

func (s *SQLiteAuditStorage) WriteBatch(logs []AuditLog) error {
	writeErr, _ := s.WriteBatchWithBudget(logs, 0)
	return writeErr
}

// WriteBatchWithBudget retires at most twice the batch size in the write transaction.
// A maintenance error is returned only after the batch has committed. Write errors
// mean the batch was not accepted and take precedence over maintenance failures.
func (s *SQLiteAuditStorage) WriteBatchWithBudget(logs []AuditLog, maxBytes int64) (writeErr, maintenanceErr error) {
	if len(logs) == 0 {
		return nil, nil
	}
	db := s.DB()
	if db == nil {
		return fmt.Errorf("sqlite audit storage is not open"), nil
	}
	// Take capacity ownership before the connection, matching standalone
	// maintenance and Clear. The watermark belongs to committed eviction only.
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	target := s.evictionTargetBytes

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin sqlite audit tx: %w", err), nil
	}
	defer tx.Rollback()
	if err := s.insertAuditLogs(tx, logs); err != nil {
		return err, nil
	}
	if err := s.upsertAggregates(tx, logs); err != nil {
		return err, nil
	}
	if maxBytes > 0 {
		if _, err := tx.Exec(`SAVEPOINT audit_capacity`); err != nil {
			maintenanceErr = fmt.Errorf("begin sqlite audit capacity savepoint: %w", err)
		} else {
			target, maintenanceErr = enforceAuditStorageBudget(tx, maxBytes, len(logs)*2, target)
			if maintenanceErr != nil {
				// Capacity failures must not undo accepted logs or their rollups.
				// Undo all maintenance steps, including earlier successful deletes.
				if _, err := tx.Exec(`ROLLBACK TO audit_capacity`); err != nil {
					return fmt.Errorf("rollback sqlite audit capacity after %v: %w", maintenanceErr, err), nil
				}
				target = s.evictionTargetBytes
			}
			if _, err := tx.Exec(`RELEASE audit_capacity`); err != nil {
				return fmt.Errorf("release sqlite audit capacity savepoint: %w", err), nil
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit tx: %w", err), nil
	}
	s.evictionTargetBytes = target
	return nil, maintenanceErr
}

func (s *SQLiteAuditStorage) insertAuditLogs(tx txPreparer, logs []AuditLog) error {
	// Answers and search text own the public read paths. Keep the legacy IP and
	// CNAME columns for older binaries, but do not duplicate new answers in them.
	stmt, err := tx.Prepare(`
		INSERT INTO audit_log (
			query_time_unix_ms, client_ip, query_type, query_name, query_class, duration_ms,
			trace_id, response_code, response_flags_aa, response_flags_tc, response_flags_ra,
			answers_json, answer_count, answer_search_text,
			domain_set_raw, domain_set_norm, upstream_tag, transport, server_name, url_path,
			cache_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
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
			log.AnswerCount,
			answerSearchText(log.Answers),
			log.DomainSetRaw,
			log.DomainSetNorm,
			log.UpstreamTag,
			log.Transport,
			log.ServerName,
			log.URLPath,
			log.CacheStatus,
		); err != nil {
			return fmt.Errorf("insert sqlite audit row: %w", err)
		}
	}
	return nil
}

func (s *SQLiteAuditStorage) upsertAggregates(tx txPreparer, logs []AuditLog) error {
	minuteRows, hourRows := buildAggregateRows(logs)
	if err := upsertAggregateTable(tx, "audit_minute", minuteRows); err != nil {
		return err
	}
	if err := upsertAggregateTable(tx, "audit_hour", hourRows); err != nil {
		return err
	}
	return nil
}

func buildAggregateRows(logs []AuditLog) (map[int64]auditAggregateRow, map[int64]auditAggregateRow) {
	minuteRows := make(map[int64]auditAggregateRow)
	hourRows := make(map[int64]auditAggregateRow)
	for _, log := range logs {
		updateAggregateRow(minuteRows, log.QueryTime.Truncate(time.Minute).Unix(), log)
		updateAggregateRow(hourRows, log.QueryTime.Truncate(time.Hour).Unix(), log)
	}
	return minuteRows, hourRows
}

func updateAggregateRow(rows map[int64]auditAggregateRow, bucket int64, log AuditLog) {
	row := rows[bucket]
	row.BucketStartUnix = bucket
	row.QueryCount++
	row.DurationSumMs += log.DurationMs
	if log.DurationMs > row.DurationMaxMs {
		row.DurationMaxMs = log.DurationMs
	}
	if isAuditResolvedCode(log.ResponseCode) {
		row.ResolvedQueryCount++
		row.ResolvedDurationSumMs += log.DurationMs
		if log.DurationMs > row.ResolvedDurationMaxMs {
			row.ResolvedDurationMaxMs = log.DurationMs
		}
	}
	if isAuditErrorCode(log.ResponseCode) {
		row.ErrorCount++
	}
	if log.ResponseCode == "NO_RESPONSE" {
		row.NoResponseCount++
	}
	if isAuditCacheHit(log.CacheStatus) {
		row.CacheHitCount++
	}
	rows[bucket] = row
}

func upsertAggregateTable(tx txPreparer, table string, rows map[int64]auditAggregateRow) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`
		INSERT INTO ` + table + ` (
			bucket_start_unix, query_count, duration_sum_ms, duration_max_ms,
			resolved_query_count, resolved_duration_sum_ms, resolved_duration_max_ms,
			error_count, no_response_count, cache_hit_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start_unix) DO UPDATE SET
			query_count = ` + table + `.query_count + excluded.query_count,
			duration_sum_ms = ` + table + `.duration_sum_ms + excluded.duration_sum_ms,
			duration_max_ms = MAX(` + table + `.duration_max_ms, excluded.duration_max_ms),
			resolved_query_count = ` + table + `.resolved_query_count + excluded.resolved_query_count,
			resolved_duration_sum_ms = ` + table + `.resolved_duration_sum_ms + excluded.resolved_duration_sum_ms,
			resolved_duration_max_ms = MAX(` + table + `.resolved_duration_max_ms, excluded.resolved_duration_max_ms),
			error_count = ` + table + `.error_count + excluded.error_count,
			no_response_count = ` + table + `.no_response_count + excluded.no_response_count,
			cache_hit_count = ` + table + `.cache_hit_count + excluded.cache_hit_count
	`)
	if err != nil {
		return fmt.Errorf("prepare sqlite aggregate upsert for %s: %w", table, err)
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.Exec(
			row.BucketStartUnix,
			row.QueryCount,
			row.DurationSumMs,
			row.DurationMaxMs,
			row.ResolvedQueryCount,
			row.ResolvedDurationSumMs,
			row.ResolvedDurationMaxMs,
			row.ErrorCount,
			row.NoResponseCount,
			row.CacheHitCount,
		); err != nil {
			return fmt.Errorf("upsert sqlite aggregate row for %s: %w", table, err)
		}
	}
	return nil
}

type txPreparer interface {
	Prepare(query string) (*sql.Stmt, error)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
