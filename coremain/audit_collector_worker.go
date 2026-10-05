package coremain

import (
	"errors"
	"fmt"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

type auditQueuedLog struct {
	generation     uint64
	estimatedBytes int64
	dropped        bool
	at             time.Time
	log            AuditLog
}

func (c *AuditCollector) runWriter() {
	defer close(c.workerDone)
	// Retain startup traffic in the bounded ingress until storage is installed.
	// DNS producers remain nonblocking even if opening fails or takes a while.
	if !c.waitForWriterStorage() {
		c.dropQueuedLogsWithoutStorage()
		return
	}
	// Settings applied before startup are already reflected in the timer and
	// limits below. Do not treat that stale notification as a partial flush.
	select {
	case <-c.settingsChanged:
	default:
	}
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
		case <-c.settingsChanged:
			if !c.retryAuditWrite(c.flushBuffered) {
				c.reportBufferedShutdownFailure()
				c.dropQueuedLogsWithoutStorage()
				return
			}
			resetTimer(timer, c.flushInterval())
			continue
		case <-timer.C:
			if !c.retryAuditWrite(c.flushBuffered) {
				c.reportBufferedShutdownFailure()
				c.dropQueuedLogsWithoutStorage()
				return
			}
			resetTimer(timer, c.flushInterval())
			continue
		}
		if !ok {
			queues[shard] = nil
			remaining--
			continue
		}
		c.releaseIngressBytes(item)
		if !c.recordQueuedLog(&item) {
			continue
		}
		if !c.retryAuditWrite(func() error { return c.stageQueuedLog(item) }) {
			c.recordPersistenceDrop(item)
			c.reportBufferedShutdownFailure()
			c.dropQueuedLogsWithoutStorage()
			return
		}
		if c.bufferNeedsFlush() {
			if !c.retryAuditWrite(c.flushBuffered) {
				c.reportBufferedShutdownFailure()
				c.dropQueuedLogsWithoutStorage()
				return
			}
			resetTimer(timer, c.flushInterval())
		}
	}
	if !c.retryAuditWrite(c.flushBuffered) {
		c.reportBufferedShutdownFailure()
	}
}

func (c *AuditCollector) waitForWriterStorage() bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Check readiness after the shutdown snapshot so storage installed just
		// before Stop can still receive the final closed-queue drain.
		stopping := c.closed.Load()
		if c.getStorage() != nil {
			return true
		}
		if stopping {
			return false
		}
		<-ticker.C
	}
}

func (c *AuditCollector) dropQueuedLogsWithoutStorage() {
	dropped := 0
	// Stop closes every ingress queue. Only current-generation raw events are
	// additional persistence losses; Clear-invalidated events do not count.
	for _, queue := range c.queues {
		for item := range queue {
			c.releaseIngressBytes(item)
			c.clearMu.RLock()
			if item.generation == c.generation.Load() {
				at := item.at
				if !item.dropped {
					at = item.log.QueryTime
					dropped++
				}
				if at.IsZero() {
					at = nowTime()
				}
				c.realtime.RecordDrop(at)
			}
			c.clearMu.RUnlock()
		}
	}
	c.degraded.Store(true)
	mlog.L().Warn("audit storage unavailable at writer shutdown", zap.Int("dropped_events", dropped))
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
		return fmt.Errorf("audit storage is not ready")
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

// The byte trigger limits retained records independently of their answer count.
// It is not a process RSS limit; SQLite and ingress have separate allocations.
const auditCollectorBufferBytes int64 = 1024 * 1024

func (c *AuditCollector) stageQueuedLog(item auditQueuedLog) error {
	c.clearMu.RLock()
	defer c.clearMu.RUnlock()
	if item.generation != c.generation.Load() {
		return nil
	}
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	storage := c.getStorage()
	if storage == nil {
		return fmt.Errorf("audit storage is not ready")
	}
	settings := c.GetSettings()
	count, size := storage.BufferedUsage()
	incoming := estimateAuditBufferBytes(item.log)
	// Flush before crossing the byte budget, rather than retaining one oversized
	// record in the ordinary tail. Failed writes leave that tail intact for retry.
	if count > 0 && (count >= settings.FlushBatchSize || incoming > auditCollectorBufferBytes-size) {
		if err := c.flushStorageBuffered(storage, settings); err != nil {
			return err
		}
	}
	if incoming > auditCollectorBufferBytes {
		writeErr, maintenanceErr := storage.WriteBatchWithBudget([]AuditLog{item.log}, int64(settings.MaxStorageMB)*1024*1024)
		c.reportAuditMaintenanceError(maintenanceErr)
		return writeErr
	}
	if err := storage.StageBufferedLog(item.log); err != nil {
		if !errors.Is(err, ErrAuditBufferFull) {
			return err
		}
		if err := c.flushStorageBuffered(storage, settings); err != nil {
			return err
		}
		return storage.StageBufferedLog(item.log)
	}
	return nil
}

func (c *AuditCollector) bufferNeedsFlush() bool {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	storage := c.getStorage()
	if storage == nil {
		return false
	}
	count, size := storage.BufferedUsage()
	return count >= c.batchSize() || size >= auditCollectorBufferBytes
}

func (c *AuditCollector) flushBuffered() error {
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	storage := c.getStorage()
	if storage == nil {
		return fmt.Errorf("audit storage is not ready")
	}
	return c.flushStorageBuffered(storage, c.GetSettings())
}

func (c *AuditCollector) flushStorageBuffered(storage *SQLiteAuditStorage, settings AuditSettings) error {
	writeErr, maintenanceErr := storage.FlushBufferedWithBudget(int64(settings.MaxStorageMB) * 1024 * 1024)
	c.reportAuditMaintenanceError(maintenanceErr)
	return writeErr
}

func (c *AuditCollector) reportAuditMaintenanceError(err error) {
	if err != nil {
		c.degraded.Store(true)
		mlog.L().Warn("failed to enforce audit capacity after persisted batch", zap.Error(err))
	}
}

// Keep a failed batch in memory while running. Shutdown gets a bounded number
// of retries so a permanently failed disk cannot block service shutdown forever.
func (c *AuditCollector) retryAuditWrite(write func() error) bool {
	shutdownFailures := 0
	reported := false
	for {
		err := write()
		if err == nil {
			return true
		}
		c.degraded.Store(true)
		if !reported {
			mlog.L().Warn("failed to persist audit buffer; retaining records for retry", zap.Error(err))
			reported = true
		}
		delay := time.Second
		if c.closed.Load() {
			shutdownFailures++
			if shutdownFailures >= 3 {
				return false
			}
			delay = 10 * time.Millisecond
		}
		time.Sleep(delay)
	}
}

func (c *AuditCollector) recordPersistenceDrop(item auditQueuedLog) {
	c.clearMu.RLock()
	defer c.clearMu.RUnlock()
	if item.generation != c.generation.Load() {
		return
	}
	c.degraded.Store(true)
	c.ingressDegraded.Store(true)
	c.realtime.RecordDrop(item.log.QueryTime)
}

func (c *AuditCollector) reportBufferedShutdownFailure() {
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	storage := c.getStorage()
	count := 0
	if storage != nil {
		count, _ = storage.BufferedUsage()
	}
	c.degraded.Store(true)
	c.ingressDegraded.Store(true)
	mlog.L().Error("audit shutdown could not persist buffered records", zap.Int("unpersisted_events", count))
}
