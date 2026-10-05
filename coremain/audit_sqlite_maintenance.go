package coremain

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *SQLiteAuditStorage) EnforceRetention(settings AuditSettings) error {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	db := s.DB()
	if db == nil {
		return nil
	}
	conn, err := s.borrowConfiguredAuditConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	rawCutoff := nowTime().AddDate(0, 0, -settings.RawRetentionDays).UnixMilli()
	aggregateCutoff := nowTime().AddDate(0, 0, -settings.AggregateRetentionDays).Unix()

	if _, err := conn.ExecContext(context.Background(), `DELETE FROM audit_log WHERE query_time_unix_ms < ?`, rawCutoff); err != nil {
		return fmt.Errorf("trim sqlite audit logs: %w", err)
	}
	if _, err := conn.ExecContext(context.Background(), `DELETE FROM audit_minute WHERE bucket_start_unix < ?`, aggregateCutoff); err != nil {
		return fmt.Errorf("trim sqlite audit minute aggregates: %w", err)
	}
	if _, err := conn.ExecContext(context.Background(), `DELETE FROM audit_hour WHERE bucket_start_unix < ?`, aggregateCutoff); err != nil {
		return fmt.Errorf("trim sqlite audit hour aggregates: %w", err)
	}
	// Release the retention connection before capacity borrows the sole slot.
	if err := conn.Close(); err != nil {
		return fmt.Errorf("return sqlite audit retention connection: %w", err)
	}
	if err := s.enforceStorageBudgetLocked(int64(settings.MaxStorageMB)*1024*1024, 2048); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteAuditStorage) enforceMaxStorageBytes(maxBytes int64) error {
	return s.enforceStorageBudget(maxBytes, 2048)
}

// enforceStorageBudget reuses free pages instead of rewriting the database.
// The budget limits live pages, not the retained high-water file size or WAL.
// A bounded step lets readers and new batches proceed while an old backlog drains.
func (s *SQLiteAuditStorage) enforceStorageBudget(maxBytes int64, rowBudget int) error {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	return s.enforceStorageBudgetLocked(maxBytes, rowBudget)
}

func (s *SQLiteAuditStorage) enforceStorageBudgetLocked(maxBytes int64, rowBudget int) error {
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	db := s.DB()
	if db == nil {
		return nil
	}
	conn, err := s.borrowConfiguredAuditConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin sqlite audit capacity tx: %w", err)
	}
	defer tx.Rollback()
	// All bounded steps share one commit. Failed maintenance must restore both
	// the deleted history and the watermark used by the next capacity check.
	target, err := enforceAuditStorageBudget(tx, maxBytes, rowBudget, s.evictionTargetBytes)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit capacity tx: %w", err)
	}
	s.evictionTargetBytes = target
	return nil
}

type auditBudgetExecutor interface {
	auditPageQuerier
	Exec(query string, args ...any) (sql.Result, error)
}

// The executor owns every page check and delete so a batch can reclaim pages
// inside its existing transaction without acquiring the single connection again.
func enforceAuditStorageBudget(db auditBudgetExecutor, maxBytes int64, rowBudget int, target int64) (int64, error) {
	if maxBytes <= 0 {
		return target, nil
	}
	high := maxBytes * 90 / 100
	low := maxBytes * 85 / 100
	for rowBudget > 0 {
		pages, free, size, err := queryAuditPageStats(db)
		if err != nil {
			return target, err
		}
		live := (pages - min(pages, free)) * size
		if live <= low {
			return 0, nil
		}
		if target != low {
			if live < high && target >= 0 {
				return target, nil
			}
			target = low
		}
		limit := min(rowBudget, 256)
		rowsAffected, err := deleteOldestAuditRows(db, limit)
		if err != nil {
			return target, err
		}
		if rowsAffected == 0 {
			rowsAffected, err = deleteOldestAggregateRows(db, limit)
			if err != nil {
				return target, err
			}
			if rowsAffected == 0 {
				return target, fmt.Errorf("sqlite audit budget %d bytes is below non-reclaimable live pages %d", maxBytes, live)
			}
		}
		rowBudget -= int(rowsAffected)
	}
	return target, nil
}

// Aggregates outlive raw logs. If they alone exceed the budget, expire the
// oldest minute buckets before hour buckets, retaining the longer-term view.
func deleteOldestAggregateRows(db auditBudgetExecutor, limit int) (int64, error) {
	for _, table := range []string{"audit_minute", "audit_hour"} {
		result, err := db.Exec(`DELETE FROM `+table+` WHERE bucket_start_unix IN (
			SELECT bucket_start_unix FROM `+table+` ORDER BY bucket_start_unix LIMIT ?
		)`, limit)
		if err != nil {
			return 0, fmt.Errorf("trim sqlite audit aggregates: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil || n > 0 {
			return n, err
		}
	}
	return 0, nil
}

func deleteOldestAuditRows(db auditBudgetExecutor, limit int) (int64, error) {
	result, err := db.Exec(`
		DELETE FROM audit_log
		WHERE id IN (
			SELECT id FROM audit_log
			ORDER BY query_time_unix_ms ASC, id ASC
			LIMIT ?
		)
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("trim sqlite audit log rows: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read trimmed sqlite audit rows: %w", err)
	}
	return rowsAffected, nil
}

func (s *SQLiteAuditStorage) Clear() error {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	db := s.DB()
	if db == nil {
		return nil
	}
	conn, err := s.borrowConfiguredAuditConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin sqlite audit clear tx: %w", err)
	}
	defer tx.Rollback()
	for _, table := range []string{"audit_log", "audit_minute", "audit_hour"} {
		if _, err := tx.Exec(`DELETE FROM main.` + table); err != nil {
			return fmt.Errorf("clear sqlite audit %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit clear tx: %w", err)
	}
	s.evictionTargetBytes = 0
	s.clearBufferedLocked()
	// Mirror cleanup and checkpoint helpers borrow the single connection again.
	if err := conn.Close(); err != nil {
		return fmt.Errorf("return sqlite audit clear connection: %w", err)
	}
	if err := s.releaseBufferedMirror(); err != nil {
		return err
	}
	if err := s.checkpointWAL(); err != nil {
		return err
	}
	if err := s.compactDatabase(); err != nil {
		return err
	}
	if err := s.checkpointWAL(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteAuditStorage) checkpointWAL() error {
	conn, err := s.borrowConfiguredAuditConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return fmt.Errorf("checkpoint sqlite audit wal: %w", err)
	}
	return nil
}

func (s *SQLiteAuditStorage) compactDatabase() error {
	conn, err := s.borrowConfiguredAuditConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `VACUUM;`); err != nil {
		return fmt.Errorf("vacuum sqlite audit db: %w", err)
	}
	return nil
}
