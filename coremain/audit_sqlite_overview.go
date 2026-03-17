package coremain

import "fmt"

func (s *SQLiteAuditStorage) QueryOverviewTotals() (uint64, float64, error) {
	db := s.DB()
	if db == nil {
		return 0, 0, nil
	}

	var totalQueryCount int64
	var totalDurationMs float64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(query_count), 0), COALESCE(SUM(duration_sum_ms), 0)
		FROM audit_hour
	`).Scan(&totalQueryCount, &totalDurationMs)
	if err != nil {
		return 0, 0, fmt.Errorf("query sqlite audit overview totals: %w", err)
	}
	if totalQueryCount <= 0 {
		return 0, 0, nil
	}
	return uint64(totalQueryCount), totalDurationMs / float64(totalQueryCount), nil
}
