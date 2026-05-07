package coremain

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

type auditQueuedLog struct {
	generation uint64
	dropped    bool
	at         time.Time
	log        AuditLog
}

func (c *AuditCollector) runWriter(queue <-chan auditQueuedLog, done chan<- struct{}) {
	defer close(done)
	timer := time.NewTimer(c.flushInterval())
	defer timer.Stop()

	batchGeneration := c.generation.Load()
	batch := make([]AuditLog, 0, c.batchSize())
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := c.writeBatch(batchGeneration, batch); err != nil {
			c.degraded.Store(true)
			mlog.L().Warn("failed to persist audit batch", zap.Error(err))
		}
		batch = batch[:0]
	}

	for {
		select {
		case item, ok := <-queue:
			if !ok {
				flush()
				return
			}
			if len(batch) > 0 && item.generation != batchGeneration {
				flush()
			}
			batchGeneration = item.generation
			currentGeneration := c.generation.Load()
			if item.generation == currentGeneration && item.dropped {
				c.realtime.RecordDrop(item.at)
				continue
			}
			if item.generation == currentGeneration {
				normalizeAuditLog(&item.log)
				c.realtime.Record(item.log)
			}
			if item.dropped {
				continue
			}
			batch = append(batch, item.log)
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
	workTicker := time.NewTicker(c.maintenanceInterval())
	checkTicker := time.NewTicker(100 * time.Millisecond)
	defer workTicker.Stop()
	defer checkTicker.Stop()

	for {
		select {
		case <-workTicker.C:
			if err := c.enforceRetention(); err != nil {
				c.degraded.Store(true)
				mlog.L().Warn("failed to enforce audit retention", zap.Error(err))
			}
		case <-checkTicker.C:
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

func (c *AuditCollector) writeBatch(generation uint64, batch []AuditLog) error {
	c.clearMu.RLock()
	defer c.clearMu.RUnlock()
	if generation != c.generation.Load() {
		return nil
	}
	for i := range batch {
		normalizeAuditLog(&batch[i])
	}

	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	c.mu.RLock()
	storage := c.storage
	c.mu.RUnlock()
	if storage == nil {
		return nil
	}
	if generation != c.generation.Load() {
		return nil
	}
	return storage.WriteBatch(batch)
}

func (c *AuditCollector) enforceRetention() error {
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
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
