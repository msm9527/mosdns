package cache

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

func persistenceTestArgs(dir string) *Args {
	return &Args{Size: 32768, DumpFile: filepath.Join(dir, "cache.dump"), DumpInterval: 3600,
		WALFile: filepath.Join(dir, "cache.wal"), WALSyncInterval: 1}
}

func storePersistenceAnswer(t *testing.T, c *Cache, name string, ip net.IP) string {
	t.Helper()
	k := cacheKeyForQuery(t, name)
	if _, ok := c.saveRespToCache(k, testQueryContext(t, name, ip)); !ok {
		t.Fatal("response was not cached")
	}
	return k
}

func TestCacheInvalidationDefersSnapshot(t *testing.T) {
	c := NewCache(persistenceTestArgs(t.TempDir()), Opts{})
	defer c.Close()
	storePersistenceAnswer(t, c, "purge.example.", net.IPv4(1, 2, 3, 4))
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}
	before := counterValue(t, c.dumpTotalCounter)
	if count, err := c.PurgeDomainRuntime(context.Background(), "purge.example.", 0); err != nil || count != 1 {
		t.Fatalf("purge = %d, %v", count, err)
	}
	if after := counterValue(t, c.dumpTotalCounter); after != before {
		t.Fatalf("invalidation wrote a full snapshot, before=%v after=%v", before, after)
	}
	if c.updatedKey.Load() == 0 {
		t.Fatal("invalidation was not scheduled for a later snapshot")
	}
}

func TestCacheCheckpointFlushesWALBeforePublishing(t *testing.T) {
	c := NewCache(persistenceTestArgs(t.TempDir()), Opts{})
	defer c.Close()
	storePersistenceAnswer(t, c, "buffered.example.", net.IPv4(1, 2, 3, 4))
	c.persistence.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- c.dumpCache() }()
	// Holding the WAL lock prevents its flush. No snapshot may be published yet.
	timer := time.NewTimer(100 * time.Millisecond)
	<-timer.C
	_, statErr := os.Stat(c.args.DumpFile)
	c.persistence.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !os.IsNotExist(statErr) {
		t.Fatalf("snapshot became visible before the WAL was flushed, stat=%v", statErr)
	}
}

func TestCacheCrashReplayMutations(t *testing.T) {
	if dir := os.Getenv("MOSDNS_CACHE_CRASH_DIR"); dir != "" {
		c := NewCache(persistenceTestArgs(dir), Opts{})
		storePersistenceAnswer(t, c, "deleted.example.", net.IPv4(1, 1, 1, 1))
		storePersistenceAnswer(t, c, "cleared.example.", net.IPv4(2, 2, 2, 2))
		if err := c.dumpCache(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PurgeDomainRuntime(context.Background(), "deleted.example.", 0); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("MOSDNS_CACHE_CRASH_STAGE") == "delete_synced" {
			os.Exit(0)
		}
		// A clear is durable independently of a snapshot. Exercise that WAL-only path.
		// Use the public WAL-only clear path while retaining the previous snapshot.
		c.args.DumpFile = ""
		if err := c.FlushRuntimeCache(context.Background()); err != nil {
			t.Fatal(err)
		}
		storePersistenceAnswer(t, c, "inserted.example.", net.IPv4(3, 3, 3, 3))
		storePersistenceAnswer(t, c, "sync.example.", net.IPv4(4, 4, 4, 4))
		if _, err := c.PurgeDomainRuntime(context.Background(), "sync.example.", 0); err != nil {
			t.Fatal(err)
		}
		switch os.Getenv("MOSDNS_CACHE_CRASH_STAGE") {
		case "snapshot_published":
			// Model termination after snapshot rename but before WAL replacement.
			snapshot, _, err := c.persistence.captureSnapshot(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writeSnapshotFileAtomic(c.persistence.snapshotPath, snapshot.writeDump); err != nil {
				t.Fatal(err)
			}
		case "wal_replaced":
			if err := c.dumpCache(); err != nil {
				t.Fatal(err)
			}
		}
		// Bypass Close and its checkpoint to model abrupt process termination.
		os.Exit(0)
	}
	for _, stage := range []string{"delete_synced", "wal_synced", "snapshot_published", "wal_replaced"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCacheCrashReplayMutations$")
			cmd.Env = append(os.Environ(), "MOSDNS_CACHE_CRASH_DIR="+dir, "MOSDNS_CACHE_CRASH_STAGE="+stage)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash writer failed, %v\n%s", err, out)
			}
			c := NewCache(persistenceTestArgs(dir), Opts{})
			defer c.Close()
			removed := []string{"deleted.example."}
			if stage != "delete_synced" {
				removed = append(removed, "cleared.example.", "sync.example.")
			}
			for _, name := range removed {
				if _, _, ok := c.backend.Get(key(cacheKeyForQuery(t, name))); ok {
					t.Fatalf("removed entry resurrected, %s", name)
				}
			}
			keep := "inserted.example."
			if stage == "delete_synced" {
				keep = "cleared.example."
			}
			if _, _, ok := c.backend.Get(key(cacheKeyForQuery(t, keep))); !ok {
				t.Fatal("durable post-clear insertion was lost")
			}
		})
	}
}

func TestCacheFailedCheckpointRetainsDurableWAL(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	defer c.Close()
	storePersistenceAnswer(t, c, "recover.example.", net.IPv4(1, 2, 3, 4))
	blockingFile := filepath.Join(filepath.Dir(args.DumpFile), "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.persistence.snapshotPath = filepath.Join(blockingFile, "cache.dump")
	if err := c.dumpCache(); err == nil {
		t.Fatal("expected snapshot failure")
	}
	// No Close or explicit WAL flush occurs before reading the recovery files.
	restored := NewCache(args, Opts{})
	defer restored.Close()
	if _, _, ok := restored.backend.Get(key(cacheKeyForQuery(t, "recover.example."))); !ok {
		t.Fatal("failed checkpoint did not retain a durable insertion")
	}
}

func TestCacheImportedSnapshotSurvivesOldWALReplay(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	defer c.Close()
	storePersistenceAnswer(t, c, "before-clear.example.", net.IPv4(1, 1, 1, 1))
	// Retain a clear record until a new snapshot has been published.
	c.args.DumpFile = ""
	if err := c.FlushRuntimeCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := NewCache(&Args{Size: 64}, Opts{})
	defer source.Close()
	storePersistenceAnswer(t, source, "imported.example.", net.IPv4(2, 2, 2, 2))
	var dump bytes.Buffer
	if _, err := source.writeDump(&dump); err != nil {
		t.Fatal(err)
	}
	if _, err := c.readDumpWithWAL(&dump, true); err != nil {
		t.Fatal(err)
	}
	c.persistence.mu.Lock()
	err := c.persistence.flushLocked()
	c.persistence.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := c.persistence.captureSnapshot(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSnapshotFileAtomic(c.persistence.snapshotPath, snapshot.writeDump); err != nil {
		t.Fatal(err)
	}
	// Read the new snapshot with the old WAL, as after interrupted rotation.
	restored := NewCache(persistenceTestArgs(filepath.Dir(c.persistence.snapshotPath)), Opts{})
	defer restored.Close()
	if _, _, ok := restored.backend.Get(key(cacheKeyForQuery(t, "imported.example."))); !ok {
		t.Fatal("old WAL clear erased the imported snapshot entry")
	}
}

func TestCacheConcurrentFlushCheckpointReplay(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	defer c.Close()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			storePersistenceAnswer(t, c, fmt.Sprintf("flush-race-%d.example.", i), net.IPv4(1, 2, 3, 4))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := c.FlushRuntimeCache(context.Background()); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := c.SaveToDisk(context.Background()); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	if err := c.persistence.close(); err != nil {
		t.Fatal(err)
	}
	restored := NewCache(args, Opts{})
	defer restored.Close()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("flush-race-%d.example.", i)
		k := key(cacheKeyForQuery(t, name))
		_, _, live := c.backend.Get(k)
		_, _, recovered := restored.backend.Get(k)
		if recovered != live {
			t.Errorf("clear/store ordering differs after replay for %s, live=%v restored=%v", name, live, recovered)
		}
	}
}

func TestCacheCrashAcrossCheckpointCut(t *testing.T) {
	if dir := os.Getenv("MOSDNS_CACHE_CUT_CRASH_DIR"); dir != "" {
		c := NewCache(persistenceTestArgs(dir), Opts{})
		storePersistenceAnswer(t, c, "cut-deleted.example.", net.IPv4(1, 1, 1, 1))
		storePersistenceAnswer(t, c, "cut-cleared.example.", net.IPv4(2, 2, 2, 2))
		if err := c.dumpCache(); err != nil {
			t.Fatal(err)
		}
		storePersistenceAnswer(t, c, "cut-prefix.example.", net.IPv4(3, 3, 3, 3))
		snapshot, cut, err := c.persistence.captureSnapshot(c)
		if err != nil {
			t.Fatal(err)
		}
		c.args.DumpFile = ""
		if _, err := c.PurgeDomainRuntime(context.Background(), "cut-deleted.example.", 0); err != nil {
			t.Fatal(err)
		}
		if err := c.FlushRuntimeCache(context.Background()); err != nil {
			t.Fatal(err)
		}
		storePersistenceAnswer(t, c, "cut-suffix.example.", net.IPv4(4, 4, 4, 4))
		storePersistenceAnswer(t, c, "cut-sync.example.", net.IPv4(5, 5, 5, 5))
		if _, err := c.PurgeDomainRuntime(context.Background(), "cut-sync.example.", 0); err != nil {
			t.Fatal(err)
		}
		stage := os.Getenv("MOSDNS_CACHE_CUT_CRASH_STAGE")
		if stage != "before_snapshot" {
			if _, err := writeSnapshotFileAtomic(c.persistence.snapshotPath, snapshot.writeDump); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "wal_replaced" {
			if err := c.persistence.completeCheckpoint(c, cut); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "rotation_failed" {
			badPath := filepath.Join(dir, "wal-is-directory")
			if err := os.Mkdir(badPath, 0o755); err != nil {
				t.Fatal(err)
			}
			c.persistence.walPath = badPath
			if err := c.persistence.completeCheckpoint(c, cut); err == nil {
				t.Fatal("expected WAL replacement failure")
			}
		}
		os.Exit(0)
	}
	for _, stage := range []string{"before_snapshot", "snapshot_published", "wal_replaced", "rotation_failed"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCacheCrashAcrossCheckpointCut$")
			cmd.Env = append(os.Environ(), "MOSDNS_CACHE_CUT_CRASH_DIR="+dir, "MOSDNS_CACHE_CUT_CRASH_STAGE="+stage)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash writer failed, %v\n%s", err, out)
			}
			c := NewCache(persistenceTestArgs(dir), Opts{})
			defer c.Close()
			for _, name := range []string{"cut-deleted.example.", "cut-cleared.example.", "cut-prefix.example.", "cut-sync.example."} {
				if _, _, ok := c.backend.Get(key(cacheKeyForQuery(t, name))); ok {
					t.Fatalf("post-cut deletion or clear was lost, %s", name)
				}
			}
			if _, _, ok := c.backend.Get(key(cacheKeyForQuery(t, "cut-suffix.example."))); !ok {
				t.Fatal("post-cut insertion was lost")
			}
		})
	}
}

func TestCacheConcurrentInvalidationCheckpointReplay(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	c := NewCache(args, Opts{})
	defer c.Close()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			name := fmt.Sprintf("deleted-%d.example.", i)
			storePersistenceAnswer(t, c, name, net.IPv4(1, 2, 3, 4))
			if n, err := c.PurgeDomainRuntime(context.Background(), name, 0); err != nil || n != 1 {
				t.Errorf("purge %s = %d, %v", name, n, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			storePersistenceAnswer(t, c, fmt.Sprintf("keep-%d.example.", i), net.IPv4(4, 3, 2, 1))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			if err := c.dumpCache(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	if err := c.persistence.close(); err != nil {
		t.Fatal(err)
	}
	restored := NewCache(args, Opts{})
	defer restored.Close()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("keep-%d.example.", i)
		if _, _, ok := restored.backend.Get(key(cacheKeyForQuery(t, name))); !ok {
			t.Errorf("completed insertion was lost, %s", name)
		}
	}
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("deleted-%d.example.", i)
		if _, _, ok := restored.backend.Get(key(cacheKeyForQuery(t, name))); ok {
			t.Errorf("completed deletion resurrected, %s", name)
		}
	}
}

// Run the same binary workload before and after the fix to measure process I/O.
func TestCachePersistenceWriteWorkload(t *testing.T) {
	if os.Getenv("MOSDNS_CACHE_WRITE_WORKLOAD") == "" {
		t.Skip("explicit persistence write workload")
	}
	c := NewCache(persistenceTestArgs(t.TempDir()), Opts{})
	defer func() {
		close(c.closeNotify)
		_ = c.persistence.close()
		_ = c.backend.Close()
	}()
	const entries, batches, batchSize = 4096, 64, 32
	for i := 0; i < entries; i++ {
		storePersistenceAnswer(t, c, fmt.Sprintf("workload-%d.example.", i), net.IPv4(byte(i>>16), byte(i>>8), byte(i), 1))
	}
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}
	before := counterValue(t, c.dumpTotalCounter)
	for batch := 0; batch < batches; batch++ {
		domains := make([]string, batchSize)
		for j := range domains {
			domains[j] = fmt.Sprintf("workload-%d.example.", batch*batchSize+j)
		}
		if n, err := c.PurgeDomainsRuntimeCache(context.Background(), domains, nil); err != nil || n != batchSize {
			t.Fatalf("batch %d purge = %d, %v", batch, n, err)
		}
		for j, name := range domains {
			storePersistenceAnswer(t, c, name, net.IPv4(5, byte(batch), byte(j), 2))
		}
	}
	if err := c.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.persistence.close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("entries=%d batches=%d batch_size=%d snapshots_during_refresh=%g", entries, batches, batchSize,
		counterValue(t, c.dumpTotalCounter)-before)
	// Keep the process alive briefly for an external per-process I/O sample.
	time.Sleep(300 * time.Millisecond)
}

func TestCacheCheckpointLoadLatency(t *testing.T) {
	if os.Getenv("MOSDNS_CACHE_WRITE_WORKLOAD") == "" {
		t.Skip("explicit checkpoint latency workload")
	}
	args := persistenceTestArgs(t.TempDir())
	args.Size = 262144
	entries := 100000
	if os.Getenv("MOSDNS_CACHE_LATENCY_400K") != "" {
		entries = 400000
		args.Size = 400000
	}
	if os.Getenv("MOSDNS_CACHE_LATENCY_L2") != "" {
		args.L1Enabled = boolPtr(false)
	}
	c := NewCache(args, Opts{})
	defer c.Close()
	for i := 0; i < entries; i++ {
		storePersistenceAnswer(t, c, fmt.Sprintf("latency-%d.example.", i), net.IPv4(1, byte(i>>8), byte(i), 1))
	}
	// Warm the actual Exec L1 hit path before starting a checkpoint.
	q := testQueryContext(t, "latency-0.example.", net.IPv4(9, 9, 9, 9))
	q.SetResponse(nil)
	if err := c.Exec(context.Background(), q, sequence.ChainWalker{}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	var readTimes, writeTimes []time.Duration
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for {
			q := testQueryContext(t, "latency-0.example.", net.IPv4(9, 9, 9, 9))
			q.SetResponse(nil)
			before := time.Now()
			if err := c.Exec(context.Background(), q, sequence.ChainWalker{}); err != nil || q.R() == nil {
				t.Errorf("concurrent DNS cache hit failed, %v", err)
				return
			}
			readTimes = append(readTimes, time.Since(before))
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for {
			before := time.Now()
			storePersistenceAnswer(t, c, "latency-writer.example.", net.IPv4(2, 3, 4, 5))
			writeTimes = append(writeTimes, time.Since(before))
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	close(start)
	before := time.Now()
	if err := c.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(before)
	close(done)
	wg.Wait()
	dnsPath := "dns_l1"
	if !c.l1Enabled {
		dnsPath = "dns_l2"
	}
	for name, samples := range map[string][]time.Duration{dnsPath: readTimes, "store": writeTimes} {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		if len(samples) == 0 {
			t.Fatal("no concurrent latency samples")
		}
		t.Logf("entries=%d checkpoint=%s %s_count=%d p50=%s p99=%s max=%s", entries, elapsed,
			name, len(samples), samples[len(samples)/2], samples[(len(samples)-1)*99/100], samples[len(samples)-1])
	}
	// Measure only the bounded metadata capture, independently of gzip allocations.
	runtime.GC()
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	captureStart := time.Now()
	snapshot, _, err := c.persistence.captureSnapshot(c)
	if err != nil {
		t.Fatal(err)
	}
	captureElapsed := time.Since(captureStart)
	runtime.ReadMemStats(&memoryAfter)
	t.Logf("capture_entries=%d metadata_bytes=%d capture_alloc_bytes=%d capture_time=%s", len(snapshot.entries),
		uintptr(cap(snapshot.entries))*unsafe.Sizeof(snapshotEntry{}), memoryAfter.TotalAlloc-memoryBefore.TotalAlloc, captureElapsed)
	runtime.KeepAlive(snapshot)
}
