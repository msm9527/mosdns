package coremain

import "fmt"

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
	if maxBytes <= 0 {
		return nil
	}
	if err := s.checkpointWAL(); err != nil {
		return err
	}
	for {
		stats, err := s.QueryStorageStats()
		if err != nil {
			return err
		}
		if stats.AllocatedBytes <= maxBytes {
			return nil
		}
		if stats.LiveBytes <= maxBytes {
			if err := s.compactDatabase(); err != nil {
				return err
			}
			if err := s.checkpointWAL(); err != nil {
				return err
			}
			continue
		}
		rowsAffected, err := s.deleteOldestAuditRows(5000)
		if err != nil {
			return err
		}
		if err := s.checkpointWAL(); err != nil {
			return err
		}
		if rowsAffected == 0 {
			if stats.ReclaimableBytes == 0 {
				return nil
			}
			if err := s.compactDatabase(); err != nil {
				return err
			}
			if err := s.checkpointWAL(); err != nil {
				return err
			}
		}
	}
}

func (s *SQLiteAuditStorage) deleteOldestAuditRows(limit int) (int64, error) {
	result, err := s.DB().Exec(`
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
