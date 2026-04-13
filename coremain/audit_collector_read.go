package coremain

import (
	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

func (c *AuditCollector) GetDiskUsageBytes() int64 {
	storage := c.getStorage()
	if storage == nil {
		return 0
	}
	size, err := storage.DiskUsageBytes()
	if err != nil {
		return 0
	}
	return size
}

func (c *AuditCollector) GetStorageStats() AuditStorageStats {
	storage := c.getStorage()
	if storage == nil {
		return AuditStorageStats{}
	}
	stats, err := storage.QueryStorageStats()
	if err != nil {
		mlog.L().Warn("failed to query audit storage stats", zap.Error(err))
		return AuditStorageStats{}
	}
	return stats
}

func (c *AuditCollector) ClearLogs() error {
	c.realtime.Reset()
	storage := c.getStorage()
	if storage == nil {
		return nil
	}
	return storage.Clear()
}

func (c *AuditCollector) GetOverview(windowSeconds int) AuditOverview {
	overview := c.realtime.Snapshot(windowSeconds)
	overview.Enabled = c.IsCapturing()
	overview.QueueDepth = len(c.queue)
	overview.Degraded = c.degraded.Load()
	overview.CurrentStorageBytes = c.GetDiskUsageBytes()
	c.fillOverviewTotals(&overview)
	return overview
}

func (c *AuditCollector) GetTimeseries(params AuditTimeseriesQuery) ([]AuditTimeseriesPoint, error) {
	storage := c.getStorage()
	if storage == nil {
		return []AuditTimeseriesPoint{}, nil
	}
	return storage.QueryTimeseries(params)
}

func (c *AuditCollector) GetRank(rankType RankType, params AuditRangeQuery) ([]AuditRankItem, error) {
	storage := c.getStorage()
	if storage == nil {
		return []AuditRankItem{}, nil
	}
	return storage.QueryRank(rankType, params)
}

func (c *AuditCollector) GetSlowLogs(params AuditRangeQuery) ([]AuditLog, error) {
	storage := c.getStorage()
	if storage == nil {
		return []AuditLog{}, nil
	}
	return storage.QuerySlowLogs(params)
}

func (c *AuditCollector) GetLogs(params AuditLogsQuery) (AuditLogsResponse, error) {
	storage := c.getStorage()
	if storage == nil {
		return AuditLogsResponse{}, nil
	}
	return storage.QueryLogs(params)
}

func (c *AuditCollector) fillOverviewTotals(overview *AuditOverview) {
	if overview == nil {
		return
	}
	overview.PeriodSummaries = defaultAuditPeriodSummaries()
	storage := c.getStorage()
	if storage == nil {
		return
	}
	totalQueryCount, totalAverageDurationMs, err := storage.QueryOverviewTotals()
	if err != nil {
		mlog.L().Warn("failed to query audit overview totals", zap.Error(err))
		return
	}
	overview.TotalQueryCount = totalQueryCount
	overview.TotalAverageDurationMs = totalAverageDurationMs
	if len(overview.PeriodSummaries) > 0 {
		overview.PeriodSummaries[0].QueryCount = totalQueryCount
		overview.PeriodSummaries[0].AverageDurationMs = totalAverageDurationMs
	}

	windowSummaries, err := storage.QueryOverviewWindowSummaries(nowTime())
	if err != nil {
		mlog.L().Warn("failed to query audit overview windows", zap.Error(err))
		return
	}
	for i, item := range windowSummaries {
		targetIdx := i + 1
		if targetIdx >= len(overview.PeriodSummaries) {
			overview.PeriodSummaries = append(overview.PeriodSummaries, item)
			continue
		}
		overview.PeriodSummaries[targetIdx] = item
	}
}

func (c *AuditCollector) getStorage() *SQLiteAuditStorage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.storage
}
