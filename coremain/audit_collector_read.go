package coremain

import (
	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

func (c *AuditCollector) GetDiskUsageBytes() int64 {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
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
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
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
	c.clearMu.Lock()
	defer c.clearMu.Unlock()

	c.generation.Add(1)
	for _, queue := range c.queues {
		for {
			select {
			case _, ok := <-queue:
				if !ok {
					queue = nil
				}
			default:
				queue = nil
			}
			if queue == nil {
				break
			}
		}
	}
	c.realtime.Reset()
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	storage := c.getStorage()
	if storage == nil {
		return nil
	}
	return storage.Clear()
}

func (c *AuditCollector) GetOverview(windowSeconds int) AuditOverview {
	overview := c.realtime.Snapshot(windowSeconds)
	overview.Enabled = c.IsCapturing()
	overview.QueueDepth = c.queueDepth()
	overview.Degraded = c.degraded.Load()
	overview.CurrentStorageBytes = c.GetDiskUsageBytes()
	c.fillOverviewTotals(&overview)
	return overview
}

func (c *AuditCollector) GetTimeseries(params AuditTimeseriesQuery) ([]AuditTimeseriesPoint, error) {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	storage := c.getStorage()
	if storage == nil {
		return []AuditTimeseriesPoint{}, nil
	}
	return storage.QueryTimeseries(params)
}

func (c *AuditCollector) GetRank(rankType RankType, params AuditRangeQuery) ([]AuditRankItem, error) {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	storage := c.getStorage()
	if storage == nil {
		return []AuditRankItem{}, nil
	}
	return storage.QueryRank(rankType, params)
}

func (c *AuditCollector) GetSlowLogs(params AuditRangeQuery) ([]AuditLog, error) {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	storage := c.getStorage()
	if storage == nil {
		return []AuditLog{}, nil
	}
	return storage.QuerySlowLogs(params)
}

func (c *AuditCollector) GetLogs(params AuditLogsQuery) (AuditLogsResponse, error) {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
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
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	storage := c.getStorage()
	if storage == nil {
		return
	}
	totals, err := storage.QueryOverviewTotals()
	if err != nil {
		mlog.L().Warn("failed to query audit overview totals", zap.Error(err))
		return
	}
	overview.TotalQueryCount = totals.QueryCount
	overview.TotalAverageDurationMs = totals.AverageDurationMs
	overview.ResolvedTotalQueryCount = totals.ResolvedQueryCount
	overview.ResolvedTotalAverageDurationMs = totals.ResolvedAverageDurationMs
	if len(overview.PeriodSummaries) > 0 {
		overview.PeriodSummaries[0].QueryCount = totals.QueryCount
		overview.PeriodSummaries[0].AverageDurationMs = totals.AverageDurationMs
		overview.PeriodSummaries[0].ResolvedQueryCount = totals.ResolvedQueryCount
		overview.PeriodSummaries[0].ResolvedAverageDurationMs = totals.ResolvedAverageDurationMs
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
