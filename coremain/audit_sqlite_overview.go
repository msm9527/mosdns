package coremain

import (
	"fmt"
	"time"
)

type auditOverviewPeriodSpec struct {
	Key    string
	Label  string
	Window time.Duration
}

var auditOverviewPeriodSpecs = []auditOverviewPeriodSpec{
	{Key: "total", Label: "总计"},
	{Key: "7d", Label: "最近 7 天", Window: 7 * 24 * time.Hour},
	{Key: "3d", Label: "最近 3 天", Window: 3 * 24 * time.Hour},
	{Key: "24h", Label: "24 小时内", Window: 24 * time.Hour},
	{Key: "1h", Label: "1 小时内", Window: time.Hour},
}

func defaultAuditPeriodSummaries() []AuditPeriodSummary {
	summaries := make([]AuditPeriodSummary, 0, len(auditOverviewPeriodSpecs))
	for _, spec := range auditOverviewPeriodSpecs {
		item := AuditPeriodSummary{
			Key:   spec.Key,
			Label: spec.Label,
		}
		if spec.Window > 0 {
			item.WindowSeconds = int(spec.Window / time.Second)
		}
		summaries = append(summaries, item)
	}
	return summaries
}

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

func (s *SQLiteAuditStorage) QueryOverviewWindowSummaries(at time.Time) ([]AuditPeriodSummary, error) {
	if at.IsZero() {
		at = nowTime()
	}

	summaries := make([]AuditPeriodSummary, 0, len(auditOverviewPeriodSpecs)-1)
	for _, spec := range auditOverviewPeriodSpecs[1:] {
		queryCount, avgDurationMs, err := s.queryOverviewWindowSummary(spec.Window, at)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, AuditPeriodSummary{
			Key:               spec.Key,
			Label:             spec.Label,
			WindowSeconds:     int(spec.Window / time.Second),
			QueryCount:        queryCount,
			AverageDurationMs: avgDurationMs,
		})
	}
	return summaries, nil
}

func (s *SQLiteAuditStorage) queryOverviewWindowSummary(window time.Duration, at time.Time) (uint64, float64, error) {
	db := s.DB()
	if db == nil || window <= 0 {
		return 0, 0, nil
	}

	table := "audit_minute"
	from := at.Add(-window).Truncate(time.Minute).Unix()
	to := at.Unix()
	if window > 24*time.Hour {
		table = "audit_hour"
		from = at.Add(-window).Truncate(time.Hour).Unix()
	}

	var queryCount int64
	var durationSumMs float64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(query_count), 0), COALESCE(SUM(duration_sum_ms), 0)
		FROM `+table+`
		WHERE bucket_start_unix BETWEEN ? AND ?
	`, from, to).Scan(&queryCount, &durationSumMs)
	if err != nil {
		return 0, 0, fmt.Errorf("query sqlite audit overview summary for %s: %w", table, err)
	}
	if queryCount <= 0 {
		return 0, 0, nil
	}
	return uint64(queryCount), durationSumMs / float64(queryCount), nil
}
