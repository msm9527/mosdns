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

func (c *AuditCollector) runWriter() {
	defer close(c.workerDone)
	// Ingress stays sharded, but SQLite has one connection and one batch owner.
	// Independent shard timers would rewrite the same rollup and index pages in
	// separate transactions within a single flush interval.
	var queues [auditQueueMaxShards]<-chan auditQueuedLog
	for i, queue := range c.queues {
		queues[i] = queue
	}
	remaining := len(c.queues)
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

	for remaining > 0 {
		var item auditQueuedLog
		var ok bool
		var shard int
		// Nil channels disable unused and drained shards. A fixed select avoids
		// reflection and forwarding goroutines on the hot audit path.
		select {
		case item, ok = <-queues[0]:
			shard = 0
		case item, ok = <-queues[1]:
			shard = 1
		case item, ok = <-queues[2]:
			shard = 2
		case item, ok = <-queues[3]:
			shard = 3
		case item, ok = <-queues[4]:
			shard = 4
		case item, ok = <-queues[5]:
			shard = 5
		case item, ok = <-queues[6]:
			shard = 6
		case item, ok = <-queues[7]:
			shard = 7
		case <-timer.C:
			flush()
			resetTimer(timer, c.flushInterval())
			continue
		}
		if !ok {
			queues[shard] = nil
			remaining--
			continue
		}
		if !c.recordQueuedLog(&item) {
			continue
		}
		if len(batch) > 0 && item.generation != batchGeneration {
			flush()
		}
		batchGeneration = item.generation
		batch = append(batch, item.log)
		if len(batch) >= c.batchSize() {
			flush()
			resetTimer(timer, c.flushInterval())
		}
	}
	flush()
}

func (c *AuditCollector) recordQueuedLog(item *auditQueuedLog) bool {
	// Clear resets realtime and persistent history as one generation boundary.
	// A dequeued old event must not reappear after that reset.
	c.clearMu.RLock()
	defer c.clearMu.RUnlock()
	if item.generation != c.generation.Load() {
		return false
	}
	if item.dropped {
		c.realtime.RecordDrop(item.at)
		return false
	}
	normalizeAuditLog(&item.log)
	c.realtime.Record(item.log)
	return true
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
	settings := c.settings
	c.mu.RUnlock()
	if storage == nil {
		return nil
	}
	if generation != c.generation.Load() {
		return nil
	}
	writeErr, maintenanceErr := storage.WriteBatchWithBudget(batch, int64(settings.MaxStorageMB)*1024*1024)
	if writeErr != nil {
		return writeErr
	}
	if maintenanceErr != nil {
		c.degraded.Store(true)
		mlog.L().Warn("failed to enforce audit capacity after persisted batch", zap.Error(maintenanceErr))
	}
	return nil
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
