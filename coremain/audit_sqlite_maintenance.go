package coremain

import (
	"database/sql"
	"fmt"
)

func (s *SQLiteAuditStorage) EnforceRetention(settings AuditSettings) error {
	db := s.DB()
	if db == nil {
		return nil
	}
	rawCutoff := nowTime().AddDate(0, 0, -settings.RawRetentionDays).UnixMilli()
	aggregateCutoff := nowTime().AddDate(0, 0, -settings.AggregateRetentionDays).Unix()

	if _, err := db.Exec(`DELETE FROM audit_log WHERE query_time_unix_ms < ?`, rawCutoff); err != nil {
		return fmt.Errorf("trim sqlite audit logs: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_minute WHERE bucket_start_unix < ?`, aggregateCutoff); err != nil {
		return fmt.Errorf("trim sqlite audit minute aggregates: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_hour WHERE bucket_start_unix < ?`, aggregateCutoff); err != nil {
		return fmt.Errorf("trim sqlite audit hour aggregates: %w", err)
	}
	if err := s.enforceMaxStorageBytes(int64(settings.MaxStorageMB) * 1024 * 1024); err != nil {
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
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	db := s.DB()
	if db == nil {
		return nil
	}
	target, err := enforceAuditStorageBudget(db, maxBytes, rowBudget, s.evictionTargetBytes)
	s.evictionTargetBytes = target
	return err
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
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	s.evictionTargetBytes = 0
	db := s.DB()
	if db == nil {
		return nil
	}
	if _, err := db.Exec(`DELETE FROM audit_log`); err != nil {
		return fmt.Errorf("clear sqlite audit logs: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_minute`); err != nil {
		return fmt.Errorf("clear sqlite audit minute aggregates: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_hour`); err != nil {
		return fmt.Errorf("clear sqlite audit hour aggregates: %w", err)
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
	if _, err := s.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return fmt.Errorf("checkpoint sqlite audit wal: %w", err)
	}
	return nil
}

func (s *SQLiteAuditStorage) compactDatabase() error {
	if _, err := s.DB().Exec(`VACUUM;`); err != nil {
		return fmt.Errorf("vacuum sqlite audit db: %w", err)
	}
	return nil
}
