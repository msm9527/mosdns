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
