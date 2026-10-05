package cache

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func storeDirtyCheckpointAnswer(t *testing.T, c *Cache, name string, ip byte) {
	t.Helper()
	storePersistenceAnswer(t, c, name, net.IPv4(192, 0, 2, ip))
	// 生产调用点在成功保存响应后记录更新，保留相同的计数时序。
	c.updatedKey.Add(1)
}

func waitDirtyCheckpointCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("checkpoint condition was not reached")
}

func closeDirtyCheckpointResources(t *testing.T, c *Cache) {
	t.Helper()
	// 绕过关闭时的新快照，恢复检查只能依赖已发布快照及其 WAL suffix。
	c.closeOnce.Do(func() { close(c.closeNotify) })
	if err := c.persistence.close(); err != nil {
		t.Fatal(err)
	}
	if err := c.backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheExplicitSaveSettlesDirtyBeforeClose(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	defer c.Close()
	storeDirtyCheckpointAnswer(t, c, "save-close.example.", 1)
	if err := c.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dirty := c.updatedKey.Load(); dirty != 0 {
		t.Fatalf("successful save left %d covered updates pending", dirty)
	}
	before := counterValue(t, c.dumpTotalCounter)
	fileBefore, err := os.Stat(args.DumpFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	fileAfter, err := os.Stat(args.DumpFile)
	if err != nil {
		t.Fatal(err)
	}
	if after := counterValue(t, c.dumpTotalCounter); after != before {
		t.Fatalf("close rewrote covered updates, dumps %v -> %v", before, after)
	}
	if !os.SameFile(fileBefore, fileAfter) {
		t.Fatal("close replaced the already current snapshot")
	}
}

func TestCacheExplicitSaveSettlesDirtyBeforeTicker(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	args.DumpInterval = 1
	c := NewCache(args, Opts{})
	defer c.Close()
	for i := 0; i < minimumChangesToDump; i++ {
		storeDirtyCheckpointAnswer(t, c, fmt.Sprintf("save-ticker-%02d.example.", i%32), byte(i%32+1))
	}
	if err := c.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dirty := c.updatedKey.Load(); dirty != 0 {
		t.Fatalf("successful save left %d covered updates pending", dirty)
	}
	before := counterValue(t, c.dumpTotalCounter)
	fileBefore, err := os.Stat(args.DumpFile)
	if err != nil {
		t.Fatal(err)
	}
	// 覆盖原生定时器的两个检查周期，不手动调用条件保存替代 ticker。
	time.Sleep(2200 * time.Millisecond)
	fileAfter, err := os.Stat(args.DumpFile)
	if err != nil {
		t.Fatal(err)
	}
	if after := counterValue(t, c.dumpTotalCounter); after != before {
		t.Fatalf("ticker rewrote covered updates, dumps %v -> %v", before, after)
	}
	if !os.SameFile(fileBefore, fileAfter) {
		t.Fatal("ticker replaced the already current snapshot")
	}
}

func TestCacheExplicitSaveFailurePreservesDirtyUpdates(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	defer c.Close()
	storeDirtyCheckpointAnswer(t, c, "save-failure.example.", 2)
	if err := c.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	storeDirtyCheckpointAnswer(t, c, "save-failure-new.example.", 3)
	dirtyBefore := c.updatedKey.Load()
	if dirtyBefore != 1 {
		t.Fatalf("pending updates = %d, want 1", dirtyBefore)
	}
	blockingPath := filepath.Join(filepath.Dir(args.DumpFile), "blocking-file")
	if err := os.WriteFile(blockingPath, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.persistence.snapshotPath = filepath.Join(blockingPath, "cache.dump")
	if err := c.SaveToDisk(context.Background()); err == nil {
		t.Fatal("expected snapshot publication failure")
	}
	if dirty := c.updatedKey.Load(); dirty != dirtyBefore {
		t.Fatalf("failed save consumed updates, got %d want %d", dirty, dirtyBefore)
	}
	c.persistence.snapshotPath = args.DumpFile
	if err := c.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dirty := c.updatedKey.Load(); dirty != 0 {
		t.Fatalf("successful retry left %d covered updates pending", dirty)
	}
}

func TestCacheExplicitSaveRetainsLateDirtyCount(t *testing.T) {
	c := NewCache(persistenceTestArgs(t.TempDir()), Opts{})
	defer c.Close()
	storeDirtyCheckpointAnswer(t, c, "late-count.example.", 4)
	c.persistence.mu.Lock()
	locked := true
	defer func() {
		if locked {
			c.persistence.mu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- c.SaveToDisk(context.Background()) }()
	waitDirtyCheckpointCondition(t, func() bool { return c.updatedKey.Load() == 0 })
	// 锁外的成功计数可能晚于本轮消费；成功结算不能清掉这类新到计数。
	const lateUpdates = uint64(7)
	c.updatedKey.Add(lateUpdates)
	c.persistence.mu.Unlock()
	locked = false
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dirty := c.updatedKey.Load(); dirty != lateUpdates {
		t.Fatalf("save lost late updates, got %d want %d", dirty, lateUpdates)
	}
}

func TestCacheExplicitSaveRetainsPostCutUpdateForReplay(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	resourcesClosed := false
	defer func() {
		if !resourcesClosed {
			closeDirtyCheckpointResources(t, c)
		}
	}()
	storeDirtyCheckpointAnswer(t, c, "prefix-dirty.example.", 5)
	c.persistence.mu.Lock()
	locked := true
	defer func() {
		if locked {
			c.persistence.mu.Unlock()
		}
	}()
	saveDone := make(chan error, 1)
	go func() { saveDone <- c.SaveToDisk(context.Background()) }()
	waitDirtyCheckpointCondition(t, func() bool {
		if c.updatedKey.Load() != 0 {
			return false
		}
		if c.mutationMu.TryLock() {
			c.mutationMu.Unlock()
			return false
		}
		return true
	})
	// capture 持有 mutationMu 并等待 WAL 锁，producer 只能在此次 cut 后变更。
	name := "suffix-dirty.example."
	k := cacheKeyForQuery(t, name)
	qCtx := testQueryContext(t, name, net.IPv4(192, 0, 2, 6))
	producerDone := make(chan bool, 1)
	go func() {
		_, ok := c.saveRespToCache(k, qCtx)
		if ok {
			c.updatedKey.Add(1)
		}
		producerDone <- ok
	}()
	c.persistence.mu.Unlock()
	locked = false
	if err := <-saveDone; err != nil {
		t.Fatal(err)
	}
	if !<-producerDone {
		t.Fatal("post-cut response was not cached")
	}
	if dirty := c.updatedKey.Load(); dirty != 1 {
		t.Fatalf("post-cut update was marked covered, pending = %d", dirty)
	}
	closeDirtyCheckpointResources(t, c)
	resourcesClosed = true
	restored := NewCache(args, Opts{})
	defer restored.Close()
	for _, name := range []string{"prefix-dirty.example.", "suffix-dirty.example."} {
		if _, _, ok := restored.backend.Get(key(cacheKeyForQuery(t, name))); !ok {
			t.Fatalf("snapshot/WAL restore lost %s", name)
		}
	}
}
