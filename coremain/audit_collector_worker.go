package coremain

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

func (c *AuditCollector) runWriter() {
	defer close(c.workerDone)
	timer := time.NewTimer(c.flushInterval())
	defer timer.Stop()

	batch := make([]AuditLog, 0, c.batchSize())
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := c.writeBatch(batch); err != nil {
			c.degraded.Store(true)
			mlog.L().Warn("failed to persist audit batch", zap.Error(err))
		}
		batch = batch[:0]
	}

	for {
		select {
		case log, ok := <-c.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, log)
			if len(batch) >= c.batchSize() {
				flush()
				resetTimer(timer, c.flushInterval())
			}
		case <-timer.C:
			flush()
			resetTimer(timer, c.flushInterval())
		}
	}
}

func (c *AuditCollector) runMaintenance() {
	defer close(c.maintDone)
	ticker := time.NewTicker(c.maintenanceInterval())
	defer ticker.Stop()

	for {
		if c.closed.Load() {
			return
		}
		select {
		case <-ticker.C:
			if err := c.enforceRetention(); err != nil {
				c.degraded.Store(true)
				mlog.L().Warn("failed to enforce audit retention", zap.Error(err))
			}
		case <-time.After(100 * time.Millisecond):
			if c.closed.Load() {
				return
			}
		}
	}
}

func (c *AuditCollector) batchSize() int {
	return c.GetSettings().FlushBatchSize
}

func (c *AuditCollector) flushInterval() time.Duration {
	return time.Duration(c.GetSettings().FlushIntervalMs) * time.Millisecond
}

func (c *AuditCollector) maintenanceInterval() time.Duration {
	return time.Duration(c.GetSettings().MaintenanceIntervalSeconds) * time.Second
}

func (c *AuditCollector) writeBatch(batch []AuditLog) error {
	c.mu.RLock()
	storage := c.storage
	c.mu.RUnlock()
	if storage == nil {
		return nil
	}
	return storage.WriteBatch(append([]AuditLog(nil), batch...))
}

func (c *AuditCollector) enforceRetention() error {
	c.mu.RLock()
	storage := c.storage
	settings := c.settings
	c.mu.RUnlock()
	if storage == nil {
		return nil
	}
	return storage.EnforceRetention(settings)
}

func resetTimer(timer *time.Timer, next time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(next)
}
