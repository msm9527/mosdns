package coremain

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditCollectorBufferedHistoryBeforeFlush(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 300000)
	// Drain the settings notification emitted by the test helper's initial open.
	time.Sleep(20 * time.Millisecond)
	c.CollectLog(auditWorkerTestLog(17))
	waitAuditWorker(t, func() bool { return c.GetOverview(60).TotalQueryCount == 1 })
	if n := auditWorkerRowCount(t, c); n != 0 {
		t.Fatalf("premature disk rows = %d", n)
	}
	result, err := c.GetLogs(AuditLogsQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil || len(result.Logs) != 1 || result.Summary.MatchedCount != 1 {
		t.Fatalf("buffered history count=%d summary=%+v err=%v", len(result.Logs), result.Summary, err)
	}
	id := result.Logs[0].ID
	if err := c.flushBuffered(); err != nil {
		t.Fatal(err)
	}
	result, err = c.GetLogs(AuditLogsQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil || len(result.Logs) != 1 || result.Logs[0].ID != id {
		t.Fatalf("flush changed buffered identity: %+v %v", result, err)
	}
	if got := c.GetOverview(60); got.TotalQueryCount != 1 || got.QueryCount != 1 {
		t.Fatalf("flush double counted %+v", got)
	}
}

func TestAuditCollectorIngressByteBudgetAndClear(t *testing.T) {
	c := NewAuditCollector(defaultAuditSettings(), t.TempDir())
	log := auditWorkerTestLog(1)
	log.Answers = []AnswerDetail{{Type: "TXT", Data: strings.Repeat("x", 64*1024)}}
	size := estimateAuditLogBytes(log)
	accepted := int(auditIngressMaxBytes / size)
	for i := 0; i < accepted+1; i++ {
		c.CollectLogWithShard(log, uint64(i))
	}
	if c.queueDepth() != accepted || c.ingressBytes.Load() != int64(accepted)*size {
		t.Fatalf("queue=%d bytes=%d", c.queueDepth(), c.ingressBytes.Load())
	}
	if got := c.realtime.Snapshot(60).DroppedEvents; got != 1 {
		t.Fatalf("overload drops=%d", got)
	}
	if err := c.ClearLogs(); err != nil {
		t.Fatal(err)
	}
	if c.ingressBytes.Load() != 0 || c.queueDepth() != 0 {
		t.Fatal("Clear retained ingress reservations")
	}
	c.CollectLog(log)
	if c.queueDepth() != 1 {
		t.Fatal("Clear did not restore ingress capacity")
	}
}

func TestAuditCollectorOwnsQueuedAnswers(t *testing.T) {
	c := NewAuditCollector(defaultAuditSettings(), t.TempDir())
	log := auditWorkerTestLog(1)
	log.Answers = []AnswerDetail{{Type: "TXT", Data: "original"}}
	c.CollectLogWithShard(log, 0)
	log.Answers[0].Data = "changed"
	item := <-c.queues[0]
	c.releaseIngressBytes(item)
	if item.log.Answers[0].Data != "original" {
		t.Fatal("caller changed accepted audit record")
	}
	if c.ingressBytes.Load() != 0 {
		t.Fatal("dequeue retained bytes")
	}
}

func TestAuditCollectorByteTriggerAndOversizedRecord(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 300000)
	time.Sleep(20 * time.Millisecond)
	log := auditWorkerTestLog(1)
	log.Answers = []AnswerDetail{{Type: "TXT", Data: strings.Repeat("x", 100*1024)}}
	c.CollectLog(log)
	waitAuditWorker(t, func() bool { return c.GetOverview(60).TotalQueryCount == 1 })
	c.CollectLog(log)
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == 1 })
	if got := c.GetOverview(60).TotalQueryCount; got != 2 {
		t.Fatalf("byte-trigger history=%d", got)
	}
	large := auditWorkerTestLog(2)
	large.Answers = []AnswerDetail{{Type: "TXT", Data: strings.Repeat("y", 1200*1024)}}
	c.CollectLog(large)
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == 3 })
	if c.ingressBytes.Load() != 0 {
		t.Fatal("writer retained ingress bytes")
	}
	if count, size := c.getStorage().BufferedUsage(); count != 0 || size != 0 {
		t.Fatalf("oversized tail=%d/%d", count, size)
	}
}

func TestAuditCollectorSettingsPreserveBufferedTail(t *testing.T) {
	dir := t.TempDir()
	c := NewAuditCollector(defaultAuditSettings(), dir)
	if err := c.reopenStorage(c.GetSettings(), dir); err != nil {
		t.Fatal(err)
	}
	defer c.closeStorage()
	old := c.getStorage()
	if err := old.StageBufferedLog(auditWorkerTestLog(1)); err != nil {
		t.Fatal(err)
	}
	next := c.GetSettings()
	next.FlushIntervalMs = 1000
	if err := c.SetSettings(next, dir); err != nil {
		t.Fatal(err)
	}
	if c.getStorage() != old {
		t.Fatal("same-path settings replaced buffered owner")
	}
	if n, _ := old.BufferedUsage(); n != 1 {
		t.Fatal("same-path settings lost pending")
	}
	if _, err := old.DB().Exec(`CREATE TRIGGER fail_audit_buffer BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	next.SQLitePath = filepath.Join(dir, "other.db")
	if err := c.SetSettings(next, dir); err == nil {
		t.Fatal("settings switched after failed tail flush")
	}
	if c.getStorage() != old || c.GetSettings().SQLitePath == next.SQLitePath {
		t.Fatal("failed swap changed owner/settings")
	}
	if _, err := old.DB().Exec(`DROP TRIGGER fail_audit_buffer`); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSettings(next, dir); err != nil {
		t.Fatal(err)
	}
	reopened, err := openAuditStorage(defaultAuditSettings(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := reopened.QueryLogs(AuditLogsQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil || len(result.Logs) != 1 {
		t.Fatalf("old path lost flushed tail count=%d err=%v", len(result.Logs), err)
	}
}

func TestAuditCollectorShutdownDiskFailureIsBoundedAndVisible(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 300000)
	time.Sleep(20 * time.Millisecond)
	c.CollectLog(auditWorkerTestLog(1))
	waitAuditWorker(t, func() bool { return c.GetOverview(60).TotalQueryCount == 1 })
	storage := c.getStorage()
	if _, err := storage.DB().Exec(`CREATE TRIGGER fail_audit_stop BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'test disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.StopWorker(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked on permanent write failure")
	}
	if got := c.GetOverview(60); !got.Degraded || got.DroppedEvents != 1 {
		t.Fatalf("shutdown failure invisible %+v", got)
	}
	if n, _ := storage.BufferedUsage(); n != 1 {
		t.Fatal("failed write silently consumed pending")
	}
	// The failure remains retryable; cleanup may close after the disk recovers.
	if _, err := storage.DB().Exec(`DROP TRIGGER fail_audit_stop`); err != nil {
		t.Fatal(err)
	}
}

func TestAuditDefaultUsesByteBudgetBeforeRowLimit(t *testing.T) {
	c := NewAuditCollector(defaultAuditSettings(), t.TempDir())
	if err := c.reopenStorage(c.GetSettings(), c.configBaseDir); err != nil {
		t.Fatal(err)
	}
	defer c.closeStorage()
	written := 0
	for i := 0; i < c.GetSettings().FlushBatchSize; i++ {
		item := auditQueuedLog{generation: c.generation.Load(), log: auditWorkerTestLog(i)}
		if err := c.stageQueuedLog(item); err != nil {
			t.Fatal(err)
		}
		written++
		count, size := c.getStorage().BufferedUsage()
		if size > auditCollectorBufferBytes {
			t.Fatalf("pending budget exceeded: %d", size)
		}
		persisted := auditWorkerRowCount(t, c)
		if written == 257 && (persisted != 0 || count != written) {
			t.Fatalf("legacy row threshold still flushed: persisted=%d pending=%d", persisted, count)
		}
		if persisted > 0 {
			if written <= 257 || written >= c.GetSettings().FlushBatchSize {
				t.Fatalf("unexpected flush threshold: %d", written)
			}
			result, err := c.GetLogs(AuditLogsQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), Limit: 10})
			if err != nil || result.Summary.MatchedCount != written {
				t.Fatalf("history after budget flush: %+v %v", result.Summary, err)
			}
			if err := c.flushBuffered(); err != nil {
				t.Fatal(err)
			}
			if got := auditWorkerRowCount(t, c); got != written {
				t.Fatalf("lost records: %d/%d", got, written)
			}
			return
		}
	}
	t.Fatal("row limit preceded byte budget")
}
