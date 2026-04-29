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

type auditOverviewTotals struct {
	QueryCount                uint64
	AverageDurationMs         float64
	ResolvedQueryCount        uint64
	ResolvedAverageDurationMs float64
}

func (s *SQLiteAuditStorage) QueryOverviewTotals() (auditOverviewTotals, error) {
	db := s.DB()
	if db == nil {
		return auditOverviewTotals{}, nil
	}

	var totalQueryCount int64
	var totalDurationMs float64
	var resolvedQueryCount int64
	var resolvedDurationMs float64
	err := db.QueryRow(`
		SELECT
			COALESCE(SUM(query_count), 0),
			COALESCE(SUM(duration_sum_ms), 0),
			COALESCE(SUM(resolved_query_count), 0),
			COALESCE(SUM(resolved_duration_sum_ms), 0)
		FROM audit_hour
	`).Scan(&totalQueryCount, &totalDurationMs, &resolvedQueryCount, &resolvedDurationMs)
	if err != nil {
		return auditOverviewTotals{}, fmt.Errorf("query sqlite audit overview totals: %w", err)
	}
	totals := auditOverviewTotals{
		QueryCount:         uint64(nonNegativeInt64(totalQueryCount)),
		ResolvedQueryCount: uint64(nonNegativeInt64(resolvedQueryCount)),
	}
	if totalQueryCount > 0 {
		totals.AverageDurationMs = totalDurationMs / float64(totalQueryCount)
	}
	if resolvedQueryCount > 0 {
		totals.ResolvedAverageDurationMs = resolvedDurationMs / float64(resolvedQueryCount)
	}
	return totals, nil
}

func (s *SQLiteAuditStorage) QueryOverviewWindowSummaries(at time.Time) ([]AuditPeriodSummary, error) {
	if at.IsZero() {
		at = nowTime()
	}

	summaries := make([]AuditPeriodSummary, 0, len(auditOverviewPeriodSpecs)-1)
	for _, spec := range auditOverviewPeriodSpecs[1:] {
		summary, err := s.queryOverviewWindowSummary(spec.Window, at)
		if err != nil {
			return nil, err
		}
		summary.Key = spec.Key
		summary.Label = spec.Label
		summary.WindowSeconds = int(spec.Window / time.Second)
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *SQLiteAuditStorage) queryOverviewWindowSummary(window time.Duration, at time.Time) (AuditPeriodSummary, error) {
	db := s.DB()
	if db == nil || window <= 0 {
		return AuditPeriodSummary{}, nil
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
	var resolvedQueryCount int64
	var resolvedDurationSumMs float64
	err := db.QueryRow(`
		SELECT
			COALESCE(SUM(query_count), 0),
			COALESCE(SUM(duration_sum_ms), 0),
			COALESCE(SUM(resolved_query_count), 0),
			COALESCE(SUM(resolved_duration_sum_ms), 0)
		FROM `+table+`
		WHERE bucket_start_unix BETWEEN ? AND ?
	`, from, to).Scan(&queryCount, &durationSumMs, &resolvedQueryCount, &resolvedDurationSumMs)
	if err != nil {
		return AuditPeriodSummary{}, fmt.Errorf("query sqlite audit overview summary for %s: %w", table, err)
	}
	if queryCount <= 0 && resolvedQueryCount <= 0 {
		return AuditPeriodSummary{}, nil
	}
	summary := AuditPeriodSummary{
		QueryCount:         uint64(nonNegativeInt64(queryCount)),
		ResolvedQueryCount: uint64(nonNegativeInt64(resolvedQueryCount)),
	}
	if queryCount > 0 {
		summary.AverageDurationMs = durationSumMs / float64(queryCount)
	}
	if resolvedQueryCount > 0 {
		summary.ResolvedAverageDurationMs = resolvedDurationSumMs / float64(resolvedQueryCount)
	}
	return summary, nil
}

func nonNegativeInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
