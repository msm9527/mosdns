package coremain

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newAuditWorkerTestCollector(t *testing.T, intervalMs int) *AuditCollector {
	t.Helper()
	settings := defaultAuditSettings()
	settings.FlushIntervalMs = intervalMs
	c := NewAuditCollector(settings, t.TempDir())
	// Exercise every ingress shard independently of the test host CPU count.
	c.queues = make([]chan auditQueuedLog, auditQueueMaxShards)
	for i := range c.queues {
		c.queues[i] = make(chan auditQueuedLog, auditQueueCapacity(settings)/auditQueueMaxShards)
	}
	if err := c.reopenStorage(c.GetSettings(), c.configBaseDir); err != nil {
		t.Fatal(err)
	}
	go c.runWriter()
	go c.runMaintenance()
	t.Cleanup(c.StopWorker)
	return c
}

func auditWorkerTestLog(n int) AuditLog {
	return testAuditLog(fmt.Sprintf("audit-%d.example", n), time.Now(), 2, "NOERROR", "domestic", AuditCacheHit)
}

func auditWorkerRowCount(t *testing.T, c *AuditCollector) int {
	t.Helper()
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	var count int
	if err := c.getStorage().DB().QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func waitAuditWorker(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("audit worker did not reach expected state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAuditCollectorBatchThresholdAcrossShards(t *testing.T) {
	// The timer is deliberately later than the assertion deadline. A full
	// collector batch must become visible even though no shard holds 256 logs.
	c := newAuditWorkerTestCollector(t, 5000)
	for i := 0; i < auditDefaultFlushBatchSize; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
	}
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == auditDefaultFlushBatchSize })
	if got := c.realtime.Snapshot(60).QueryCount; got != auditDefaultFlushBatchSize {
		t.Fatalf("realtime count = %d, want %d", got, auditDefaultFlushBatchSize)
	}
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	for _, table := range []string{"audit_minute", "audit_hour"} {
		var count int
		if err := c.getStorage().DB().QueryRow("SELECT SUM(query_count) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != auditDefaultFlushBatchSize {
			t.Fatalf("%s count = %d, want %d", table, count, auditDefaultFlushBatchSize)
		}
	}
}

func TestAuditCollectorTimerFlushesPartialSharedBatch(t *testing.T) {
	c := newAuditWorkerTestCollector(t, auditDefaultFlushIntervalMs)
	for i := range c.queues {
		c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
	}
	waitAuditWorker(t, func() bool { return c.realtime.Snapshot(60).QueryCount == uint64(len(c.queues)) })
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == len(c.queues) })
}

func TestAuditCollectorClearDiscardsPendingGeneration(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 5000)
	c.CollectLogWithShard(auditWorkerTestLog(0), 0)
	waitAuditWorker(t, func() bool { return c.realtime.Snapshot(60).QueryCount == 1 })
	if err := c.ClearLogs(); err != nil {
		t.Fatal(err)
	}
	// Model a dequeued old event arriving after Clear, including drop markers.
	old := auditQueuedLog{generation: 0, log: auditWorkerTestLog(-1)}
	if c.recordQueuedLog(&old) {
		t.Fatal("old generation accepted after clear")
	}
	old.dropped, old.at = true, time.Now()
	if c.recordQueuedLog(&old) {
		t.Fatal("old drop marker accepted after clear")
	}
	for i := 1; i <= auditDefaultFlushBatchSize; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
	}
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == auditDefaultFlushBatchSize })
	overview := c.realtime.Snapshot(60)
	if overview.QueryCount != auditDefaultFlushBatchSize || overview.DroppedEvents != 0 {
		t.Fatalf("realtime after clear = %+v", overview)
	}
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	var oldCount int
	if err := c.getStorage().DB().QueryRow("SELECT COUNT(*) FROM audit_log WHERE query_name IN ('audit-0.example', 'audit--1.example')").Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatalf("old history returned after clear = %d", oldCount)
	}
}

func TestAuditCollectorStopDrainsEveryShard(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 5000)
	const count = 803 // Includes a partial final batch and unequal shard depths.
	for i := 0; i < count; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
	}
	c.StopWorker()
	storage, err := openAuditStorage(c.GetSettings(), c.configBaseDir)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	var got int
	if err := storage.DB().QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != count || c.queueDepth() != 0 || c.realtime.Snapshot(60).QueryCount != count {
		t.Fatalf("drain count = %d, queue = %d, realtime = %d", got, c.queueDepth(), c.realtime.Snapshot(60).QueryCount)
	}
}

func TestAuditCollectorConcurrentStopAndIngest(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 5000)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for shard := range c.queues {
		workers.Add(1)
		go func(shard int) {
			defer workers.Done()
			<-start
			for i := 0; i < 200; i++ {
				c.CollectLogWithShard(auditWorkerTestLog(i), uint64(shard))
			}
		}(shard)
	}
	close(start)
	c.StopWorker()
	workers.Wait()
	if c.queueDepth() != 0 {
		t.Fatalf("queue after stop = %d", c.queueDepth())
	}
}

func TestAuditCollectorFullQueueDoesNotWaitForStorage(t *testing.T) {
	c := NewAuditCollector(defaultAuditSettings(), t.TempDir())
	queue := c.queueForShard(0)
	for i := 0; i < cap(queue); i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), 0)
	}
	done := make(chan struct{})
	go func() {
		c.CollectLogWithShard(auditWorkerTestLog(-1), 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full queue blocked producer")
	}
	if !c.degraded.Load() || len(queue) != cap(queue) {
		t.Fatal("overflow must keep bounded queue and report degraded")
	}
}

func newAuditStorageWaitTestCollector(t *testing.T, failOpen bool) (*AuditCollector, func()) {
	t.Helper()
	originalOpen := openAuditStorage
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	openAuditStorage = func(settings AuditSettings, dir string) (*SQLiteAuditStorage, error) {
		<-gate
		if failOpen {
			return nil, fmt.Errorf("test audit storage unavailable")
		}
		return originalOpen(settings, dir)
	}
	c := NewAuditCollector(defaultAuditSettings(), t.TempDir())
	c.StartWorker()
	t.Cleanup(func() {
		c.StopWorker()
		release()
		waitAuditWorker(t, func() bool { return !c.opening.Load() })
		openAuditStorage = originalOpen
	})
	return c, release
}

func assertAuditWorkerHistoryCount(t *testing.T, c *AuditCollector, want int) {
	t.Helper()
	c.storageMu.RLock()
	defer c.storageMu.RUnlock()
	for _, table := range []string{"audit_minute", "audit_hour"} {
		var got int
		if err := c.getStorage().DB().QueryRow("SELECT SUM(query_count) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

func TestAuditCollectorDelayedStoragePreservesAcceptedQueue(t *testing.T) {
	c, release := newAuditStorageWaitTestCollector(t, false)
	const count = auditDefaultFlushBatchSize + 17
	producerDone := make(chan struct{})
	go func() {
		for i := 0; i < count; i++ {
			c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
		}
		close(producerDone)
	}()
	select {
	case <-producerDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("storage wait blocked audit producer")
	}
	// The blocked opener spans both a full batch and the normal flush interval.
	time.Sleep(c.flushInterval() + 50*time.Millisecond)
	if got := c.queueDepth(); got != count {
		t.Fatalf("queue before storage ready = %d, want %d", got, count)
	}
	release()
	waitAuditWorker(t, func() bool { return c.getStorage() != nil })
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == count })
	assertAuditWorkerHistoryCount(t, c, count)
	if got := c.realtime.Snapshot(60); got.QueryCount != count || got.DroppedEvents != 0 {
		t.Fatalf("realtime after storage ready = %+v", got)
	}
}

func TestAuditCollectorClearWhileWaitingForStorage(t *testing.T) {
	c, release := newAuditStorageWaitTestCollector(t, false)
	for i := 0; i < auditDefaultFlushBatchSize; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
	}
	if err := c.ClearLogs(); err != nil {
		t.Fatal(err)
	}
	// A stale event dequeued across Clear must neither persist nor count as loss.
	c.queues[0] <- auditQueuedLog{generation: 0, log: auditWorkerTestLog(-1)}
	const count = 7
	for i := 0; i < count; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(1000+i), uint64(i))
	}
	release()
	waitAuditWorker(t, func() bool { return c.getStorage() != nil })
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == count })
	assertAuditWorkerHistoryCount(t, c, count)
	if got := c.realtime.Snapshot(60); got.QueryCount != count || got.DroppedEvents != 0 {
		t.Fatalf("realtime after Clear and storage ready = %+v", got)
	}
}

func TestAuditCollectorUnavailableStorageStopReportsDrops(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("open_failed_%t", failed), func(t *testing.T) {
			c, release := newAuditStorageWaitTestCollector(t, failed)
			if failed {
				release()
				waitAuditWorker(t, func() bool { return !c.opening.Load() })
			}
			if err := c.ClearLogs(); err != nil {
				t.Fatal(err)
			}
			at := time.Now()
			c.queues[0] <- auditQueuedLog{generation: 0, log: auditWorkerTestLog(-1)}
			c.queues[0] <- auditQueuedLog{generation: 0, dropped: true, at: at}
			c.queues[0] <- auditQueuedLog{generation: c.generation.Load(), dropped: true, at: at}
			const count = 3
			for i := 0; i < count; i++ {
				c.CollectLogWithShard(auditWorkerTestLog(i), uint64(i))
			}
			time.Sleep(c.flushInterval() + 50*time.Millisecond)
			stopped := make(chan struct{})
			go func() { c.StopWorker(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("StopWorker blocked on unavailable storage")
			}
			got := c.realtime.Snapshot(60)
			// The existing current-generation drop marker is counted once. Old
			// generation events are excluded, including their drop markers.
			if !c.degraded.Load() || c.queueDepth() != 0 || got.QueryCount != 0 || got.DroppedEvents != count+1 {
				t.Fatalf("shutdown degraded=%t queue=%d realtime=%+v", c.degraded.Load(), c.queueDepth(), got)
			}
		})
	}
}

func TestAuditCollectorWriteBatchWithoutStorageReturnsError(t *testing.T) {
	c := NewAuditCollector(defaultAuditSettings(), t.TempDir())
	if err := c.writeBatch(c.generation.Load(), []AuditLog{auditWorkerTestLog(1)}); err == nil {
		t.Fatal("missing storage reported an accepted batch")
	}
}

func TestAuditCollectorDelayedStorageOverflowStaysVisibleAfterDrain(t *testing.T) {
	c, release := newAuditStorageWaitTestCollector(t, false)
	count := cap(c.queueForShard(0))
	for i := 0; i <= count; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), 0)
	}
	release()
	waitAuditWorker(t, func() bool { return c.getStorage() != nil })
	waitAuditWorker(t, func() bool { return c.queueDepth() == 0 && auditWorkerRowCount(t, c) == count })
	assertAuditWorkerHistoryCount(t, c, count)
	if got := c.GetOverview(60); got.DroppedEvents != 1 || !got.Degraded || got.QueryCount != uint64(count) {
		t.Fatalf("overflow after storage readiness and drain = %+v", got)
	}
}

func TestAuditCollectorClearPreservesOverflowAfterIngressReset(t *testing.T) {
	c, release := newAuditStorageWaitTestCollector(t, false)
	count := cap(c.queueForShard(0))
	for i := 0; i <= count; i++ {
		c.CollectLogWithShard(auditWorkerTestLog(i), 0)
	}
	if got := c.realtime.Snapshot(60).DroppedEvents; got != 1 {
		t.Fatalf("first overflow drops = %d, want 1", got)
	}

	// Pause Clear after its ingress reset, at the disk phase. New producers
	// must finish without that lock and their losses must survive Clear's return.
	c.storageMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(c.storageMu.Unlock) }
	clearDone := make(chan error, 1)
	clearFinished := make(chan struct{})
	go func() {
		clearDone <- c.ClearLogs()
		close(clearFinished)
	}()
	t.Cleanup(func() { unlock(); <-clearFinished })
	waitAuditWorker(t, func() bool { return c.generation.Load() == 1 })
	c.ingestMu.RLock()
	c.ingestMu.RUnlock()
	producerDone := make(chan struct{})
	go func() {
		for i := 0; i < count+2; i++ {
			c.CollectLogWithShard(auditWorkerTestLog(10000+i), 0)
		}
		close(producerDone)
	}()
	select {
	case <-producerDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("producer waited for Clear's storage lock")
	}
	unlock()
	if err := <-clearDone; err != nil {
		t.Fatal(err)
	}
	if got := c.GetOverview(60); got.DroppedEvents != 2 || !got.Degraded {
		t.Fatalf("new overflow after Clear ingress reset = %+v", got)
	}
	release()
	waitAuditWorker(t, func() bool { return c.getStorage() != nil })
	waitAuditWorker(t, func() bool { return c.queueDepth() == 0 && auditWorkerRowCount(t, c) == count })
	assertAuditWorkerHistoryCount(t, c, count)
	if got := c.GetOverview(60); got.DroppedEvents != 2 || !got.Degraded {
		t.Fatalf("new overflow after storage readiness = %+v", got)
	}
	if err := c.ClearLogs(); err != nil {
		t.Fatal(err)
	}
	if got := c.GetOverview(60); got.DroppedEvents != 0 || got.Degraded || got.QueueDepth != 0 || got.TotalQueryCount != 0 {
		t.Fatalf("Clear did not reset losses = %+v", got)
	}
}
