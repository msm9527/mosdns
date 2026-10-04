package cache

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

func TestCacheLegacyWALMigration(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	source := NewCache(&Args{Size: 64}, Opts{})
	defer source.Close()
	deleted := storePersistenceAnswer(t, source, "legacy-deleted.example.", net.IPv4(1, 1, 1, 1))
	var snapshot bytes.Buffer
	if _, err := source.writeDump(&snapshot); err != nil {
		t.Fatal(err)
	}
	inserted := storePersistenceAnswer(t, source, "legacy-inserted.example.", net.IPv4(2, 2, 2, 2))
	v, expiration, _ := source.backend.Get(key(inserted))
	var wal bytes.Buffer
	wal.WriteString(walMagic)
	if err := writeWALDeleteRecord(&wal, key(deleted)); err != nil {
		t.Fatal(err)
	}
	if err := writeWALFlushRecord(&wal); err != nil {
		t.Fatal(err)
	}
	if err := writeWALStoreRecord(&wal, walStoreRecord{key: key(inserted), cacheExp: expiration, cacheItem: v}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args.DumpFile, snapshot.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args.WALFile, wal.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(args, Opts{})
	defer c.Close()
	if c.persistence.restoreErr != nil {
		t.Fatal(c.persistence.restoreErr)
	}
	if _, _, ok := c.backend.Get(key(inserted)); !ok {
		t.Fatal("legacy insert was not restored")
	}
	if _, _, ok := c.backend.Get(key(deleted)); ok {
		t.Fatal("legacy delete or clear was lost")
	}
	migrated, err := os.ReadFile(args.WALFile)
	if err != nil {
		t.Fatal(err)
	}
	header, err := readWALHeader(bytes.NewReader(migrated))
	if err != nil || header.version != 2 || header.base != 0 {
		t.Fatalf("migration header = %+v, %v", header, err)
	}
	if !bytes.Equal(migrated[header.size():], wal.Bytes()[len(walMagic):]) {
		t.Fatal("migration changed v1 records")
	}
	// The unchanged legacy snapshot must also recover with the newly migrated WAL.
	restored := NewCache(args, Opts{})
	defer restored.Close()
	if restored.persistence.restoreErr != nil {
		t.Fatal(restored.persistence.restoreErr)
	}
	if _, _, ok := restored.backend.Get(key(inserted)); !ok {
		t.Fatal("legacy snapshot plus v2/base=0 did not recover")
	}
}

func TestCacheSnapshotCutDoesNotReplayCapacityEvictions(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	args.Size = 2 * concurrent_map.MapShardSize
	c := NewCache(args, Opts{})
	defer c.Close()
	// Force a full shard so replaying a transient prefix insertion would evict
	// a correct snapshot entry, even though the transient entry was later removed.
	var names []string
	var shard uint64
	for i := 0; len(names) < 4; i++ {
		name := fmt.Sprintf("capacity-%d.example.", i)
		index := key(cacheKeyForQuery(t, name)).Sum() % concurrent_map.MapShardSize
		if len(names) == 0 {
			shard = index
		}
		if index == shard {
			names = append(names, name)
		}
	}
	for _, name := range names[:2] {
		storePersistenceAnswer(t, c, name, net.IPv4(1, 2, 3, 4))
	}
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}
	storePersistenceAnswer(t, c, names[2], net.IPv4(2, 3, 4, 5))
	if _, err := c.PurgeDomainRuntime(context.Background(), names[2], 0); err != nil {
		t.Fatal(err)
	}
	storePersistenceAnswer(t, c, names[3], net.IPv4(3, 4, 5, 6))
	snapshot, _, err := c.persistence.captureSnapshot(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSnapshotFileAtomic(args.DumpFile, snapshot.writeDump); err != nil {
		t.Fatal(err)
	}
	// Do not rotate the WAL. This is the new snapshot plus old WAL crash pair.
	restored := NewCache(args, Opts{})
	defer restored.Close()
	if restored.persistence.restoreErr != nil {
		t.Fatal(restored.persistence.restoreErr)
	}
	for _, name := range names {
		k := key(cacheKeyForQuery(t, name))
		_, _, before := c.backend.Get(k)
		_, _, after := restored.backend.Get(k)
		if before != after {
			t.Fatalf("capacity replay changed %s, live=%v recovered=%v", name, before, after)
		}
	}
	if count := restored.snapshotStats().LastReplay.Entries; count != 0 {
		t.Fatalf("replayed %d records already represented by the snapshot", count)
	}
}

func TestCacheMismatchedCheckpointPreservesFilesAndBypasses(t *testing.T) {
	for _, fault := range []string{"generation", "old_snapshot", "split_record", "legacy_rotated"} {
		t.Run(fault, func(t *testing.T) {
			args := persistenceTestArgs(t.TempDir())
			c := NewCache(args, Opts{})
			defer c.Close()
			storePersistenceAnswer(t, c, "bad-pair.example.", net.IPv4(1, 1, 1, 1))
			if err := c.dumpCache(); err != nil {
				t.Fatal(err)
			}
			oldSnapshot, err := os.ReadFile(args.DumpFile)
			if err != nil {
				t.Fatal(err)
			}
			storePersistenceAnswer(t, c, "new-record.example.", net.IPv4(2, 2, 2, 2))
			snapshot, _, err := c.persistence.captureSnapshot(c)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "generation":
				snapshot.checkpoint.generation[0] ^= 1
				_, err = writeSnapshotFileAtomic(args.DumpFile, snapshot.writeDump)
			case "old_snapshot":
				if err = c.SaveToDisk(context.Background()); err == nil {
					err = os.WriteFile(args.DumpFile, oldSnapshot, 0o600)
				}
			case "split_record":
				storePersistenceAnswer(t, c, "tail-record.example.", net.IPv4(3, 3, 3, 3))
				snapshot.checkpoint.cut++
				if err = c.persistence.close(); err == nil {
					_, err = writeSnapshotFileAtomic(args.DumpFile, snapshot.writeDump)
				}
			case "legacy_rotated":
				if err = c.SaveToDisk(context.Background()); err == nil {
					_, err = c.writeSnapshotFileAtomic(args.DumpFile)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			dumpBefore, _ := os.ReadFile(args.DumpFile)
			walBefore, _ := os.ReadFile(args.WALFile)
			restored := NewCache(args, Opts{})
			if restored.persistence.restoreErr == nil || restored.backend.Len() != 0 {
				t.Fatal("mismatched checkpoint was accepted or partially loaded cache remained")
			}
			if restored.snapshotStats().LastReplay.Status != "error" {
				t.Fatal("recovery failure is not visible in runtime stats")
			}
			if err := restored.SaveToDisk(context.Background()); err == nil {
				t.Fatal("checkpoint overwrote files after failed recovery")
			}
			var importData bytes.Buffer
			if _, err := c.writeDump(&importData); err != nil {
				t.Fatal(err)
			}
			if _, err := restored.readDumpWithWAL(&importData, true); err == nil || restored.backend.Len() != 0 {
				t.Fatal("failed import changed the empty cache after recovery was blocked")
			}
			storePersistenceAnswer(t, restored, "must-not-write.example.", net.IPv4(3, 3, 3, 3))
			upstream := &testResponseExec{ip: net.IPv4(4, 4, 4, 4)}
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": restored, "upstream": upstream})
			seq, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("recovery", m)), []sequence.RuleArgs{
				{Exec: "$cache"}, {Exec: "$upstream"},
			})
			if err != nil {
				t.Fatal(err)
			}
			q := queryThroughSequence(t, seq, "bad-pair.example.")
			if !responseHasA(q.R(), upstream.ip) || upstream.calls.Load() != 1 {
				t.Fatal("failed cache recovery did not pass DNS to the upstream")
			}
			_ = seq.Close()
			_ = restored.Close()
			dumpAfter, _ := os.ReadFile(args.DumpFile)
			walAfter, _ := os.ReadFile(args.WALFile)
			if !bytes.Equal(dumpBefore, dumpAfter) || !bytes.Equal(walBefore, walAfter) {
				t.Fatal("failed recovery changed the source files")
			}
		})
	}
}

func TestCacheTruncatedWALTailAllowsLaterRestart(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			args := &Args{Size: 256, WALFile: filepath.Join(t.TempDir(), "cache.wal")}
			source := NewCache(&Args{Size: 256}, Opts{})
			defer source.Close()
			k := storePersistenceAnswer(t, source, "tail-before.example.", net.IPv4(1, 2, 3, 4))
			v, exp, _ := source.backend.Get(key(k))
			var wal bytes.Buffer
			if version == 1 {
				wal.WriteString(walMagic)
			} else {
				header, err := newWALHeader()
				if err != nil {
					t.Fatal(err)
				}
				if err := writeWALHeader(&wal, header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeWALStoreRecord(&wal, walStoreRecord{key: key(k), cacheExp: exp, cacheItem: v}); err != nil {
				t.Fatal(err)
			}
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], 100)
			wal.Write(size[:])
			wal.WriteByte(walOpSet)
			if err := os.WriteFile(args.WALFile, wal.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			c := NewCache(args, Opts{})
			if c.persistence.restoreErr != nil {
				t.Fatal(c.persistence.restoreErr)
			}
			after := storePersistenceAnswer(t, c, "tail-after.example.", net.IPv4(2, 3, 4, 5))
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			restored := NewCache(args, Opts{})
			defer restored.Close()
			if restored.persistence.restoreErr != nil {
				t.Fatal(restored.persistence.restoreErr)
			}
			for _, name := range []string{k, after} {
				if _, _, ok := restored.backend.Get(key(name)); !ok {
					t.Fatal("valid insertion was lost after repairing a truncated WAL tail")
				}
			}
		})
	}
}

func TestCacheSnapshotImportIgnoresForeignCheckpoint(t *testing.T) {
	source := NewCache(persistenceTestArgs(t.TempDir()), Opts{})
	defer source.Close()
	storePersistenceAnswer(t, source, "import-api.example.", net.IPv4(1, 2, 3, 4))
	if err := source.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source.args.DumpFile)
	if err != nil {
		t.Fatal(err)
	}
	args := persistenceTestArgs(t.TempDir())
	destination := NewCache(args, Opts{})
	defer destination.Close()
	recorder := httptest.NewRecorder()
	destination.Api().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/load_dump", bytes.NewReader(data)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("snapshot import returned %d, %s", recorder.Code, recorder.Body.String())
	}
	restored := NewCache(args, Opts{})
	defer restored.Close()
	if restored.persistence.restoreErr != nil {
		t.Fatal(restored.persistence.restoreErr)
	}
	if _, _, ok := restored.backend.Get(key(cacheKeyForQuery(t, "import-api.example."))); !ok {
		t.Fatal("imported response was not recovered")
	}
}

func TestCachePeriodicCheckpointFailureRetainsDirtyUpdates(t *testing.T) {
	args := persistenceTestArgs(t.TempDir())
	args.DumpInterval = 1
	c := NewCache(args, Opts{})
	defer c.Close()
	blockingFile := filepath.Join(filepath.Dir(args.DumpFile), "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.persistence.snapshotPath = filepath.Join(blockingFile, "cache.dump")
	c.persistence.mu.Lock()
	c.updatedKey.Store(minimumChangesToDump)
	deadline := time.Now().Add(3 * time.Second)
	for c.updatedKey.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.updatedKey.Load() != 0 {
		c.persistence.mu.Unlock()
		t.Fatal("periodic checkpoint did not consume pending updates")
	}
	const concurrentUpdates = uint64(7)
	c.updatedKey.Add(concurrentUpdates)
	c.persistence.mu.Unlock()
	for c.snapshotStats().LastDump.Status != "error" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.snapshotStats().LastDump.Status != "error" {
		t.Fatal("periodic checkpoint did not report failure")
	}
	for c.updatedKey.Load() < minimumChangesToDump+concurrentUpdates && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dirty := c.updatedKey.Load(); dirty < minimumChangesToDump+concurrentUpdates {
		t.Fatalf("failed checkpoint lost dirty updates, got %d", dirty)
	}
	// No new writes are needed to trigger a retry on the following tick.
	first := counterValue(t, c.dumpErrorCounter)
	deadline = time.Now().Add(2 * time.Second)
	for counterValue(t, c.dumpErrorCounter) == first && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if counterValue(t, c.dumpErrorCounter) <= first {
		t.Fatal("checkpoint was not retried after a failure")
	}
}
