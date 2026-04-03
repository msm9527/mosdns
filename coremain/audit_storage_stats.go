package coremain

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

func (s *SQLiteAuditStorage) QueryStorageStats() (AuditStorageStats, error) {
	db := s.DB()
	if db == nil {
		return AuditStorageStats{}, nil
	}

	allocatedBytes, err := s.DiskUsageBytes()
	if err != nil {
		return AuditStorageStats{}, fmt.Errorf("query sqlite audit allocated size: %w", err)
	}

	pageCount, freelistCount, pageSize, err := s.queryPageStats()
	if err != nil {
		return AuditStorageStats{}, err
	}
	if freelistCount > pageCount {
		freelistCount = pageCount
	}

	walBytes, err := fileSizeBytes(s.path + "-wal")
	if err != nil {
		return AuditStorageStats{}, fmt.Errorf("stat sqlite audit wal size: %w", err)
	}
	shmBytes, err := fileSizeBytes(s.path + "-shm")
	if err != nil {
		return AuditStorageStats{}, fmt.Errorf("stat sqlite audit shm size: %w", err)
	}

	liveDBBytes := int64(pageCount-freelistCount) * int64(pageSize)
	if liveDBBytes < 0 {
		liveDBBytes = 0
	}
	liveBytes := liveDBBytes + walBytes + shmBytes
	if liveBytes > allocatedBytes {
		liveBytes = allocatedBytes
	}

	stats := AuditStorageStats{
		AllocatedBytes: allocatedBytes,
		LiveBytes:      liveBytes,
	}
	if stats.AllocatedBytes > stats.LiveBytes {
		stats.ReclaimableBytes = stats.AllocatedBytes - stats.LiveBytes
	}

	var (
		oldestUnixMs sql.NullInt64
		newestUnixMs sql.NullInt64
	)
	if err := db.QueryRow(`
		SELECT COUNT(*), MIN(query_time_unix_ms), MAX(query_time_unix_ms)
		FROM audit_log
	`).Scan(&stats.RawLogCount, &oldestUnixMs, &newestUnixMs); err != nil {
		return AuditStorageStats{}, fmt.Errorf("query sqlite audit range: %w", err)
	}
	if oldestUnixMs.Valid {
		oldest := time.UnixMilli(oldestUnixMs.Int64)
		stats.OldestLogTime = &oldest
	}
	if newestUnixMs.Valid {
		newest := time.UnixMilli(newestUnixMs.Int64)
		stats.NewestLogTime = &newest
	}
	return stats, nil
}

func (s *SQLiteAuditStorage) queryPageStats() (pageCount, freelistCount, pageSize int64, err error) {
	db := s.DB()
	if db == nil {
		return 0, 0, 0, nil
	}
	if err := db.QueryRow(`PRAGMA page_count;`).Scan(&pageCount); err != nil {
		return 0, 0, 0, fmt.Errorf("query sqlite audit page_count: %w", err)
	}
	if err := db.QueryRow(`PRAGMA freelist_count;`).Scan(&freelistCount); err != nil {
		return 0, 0, 0, fmt.Errorf("query sqlite audit freelist_count: %w", err)
	}
	if err := db.QueryRow(`PRAGMA page_size;`).Scan(&pageSize); err != nil {
		return 0, 0, 0, fmt.Errorf("query sqlite audit page_size: %w", err)
	}
	return pageCount, freelistCount, pageSize, nil
}

func fileSizeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}
