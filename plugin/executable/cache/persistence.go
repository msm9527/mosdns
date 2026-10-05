package cache

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	walMagic   = "mosdns_cache_wal_v1\n"
	walOpSet   = byte(1)
	walOpDel   = byte(2)
	walOpFlush = byte(3)
)

type persistenceManager struct {
	logger       *zap.Logger
	snapshotPath string
	walPath      string
	syncInterval time.Duration

	mu               sync.Mutex
	walFile          *os.File
	walWriter        *bufio.Writer
	lastSync         time.Time
	header           walHeader
	loadedCheckpoint *snapshotCheckpoint
	restoreErr       error
	validWALSize     int64
}

type walStoreRecord struct {
	key       key
	cacheExp  time.Time
	cacheItem *item
}

type walRecord struct {
	op byte
	walStoreRecord
}

// 只保留 dump 所需的不可变字段，不复制 DNS payload，也不持有 L1 或 TTL 索引。
type snapshotEntry struct {
	key             key
	resp            []byte
	domainSet       string
	cacheExpiration int64
	msgExpiration   int64
	msgStored       int64
}

type cacheSnapshot struct {
	entries    []snapshotEntry
	checkpoint *snapshotCheckpoint
}

func (snapshot cacheSnapshot) writeDump(w io.Writer) (int, error) {
	return writeDumpEntries(w, func(yield func(*CachedEntry) error) error {
		for _, entry := range snapshot.entries {
			if err := yield(&CachedEntry{
				Key: []byte(entry.key), Msg: entry.resp, DomainSet: entry.domainSet,
				CacheExpirationTime: entry.cacheExpiration,
				MsgExpirationTime:   entry.msgExpiration, MsgStoredTime: entry.msgStored,
			}); err != nil {
				return err
			}
		}
		return nil
	}, snapshot.checkpoint.extra())
}

func newPersistenceManager(args *Args, logger *zap.Logger) *persistenceManager {
	pm := &persistenceManager{
		logger:       logger,
		snapshotPath: args.DumpFile,
		walPath:      args.WALFile,
		syncInterval: time.Duration(args.WALSyncInterval) * time.Second,
	}
	if pm.syncInterval <= 0 {
		pm.syncInterval = defaultWALSyncIntervalSeconds * time.Second
	}
	return pm
}

func (pm *persistenceManager) restore(c *Cache) error {
	var loadErr error
	if pm.snapshotPath != "" {
		loadErr = c.loadSnapshot()
	}
	if loadErr != nil {
		return loadErr
	}
	if pm.walPath == "" {
		return nil
	}
	if err := c.replayWAL(); err != nil {
		return err
	}
	// 开始服务前原样迁移 v1 records。旧快照与 v2/base=0 仍可完整恢复。
	return pm.migrateLegacyWAL()
}

func (pm *persistenceManager) appendStore(record walStoreRecord) error {
	if pm.walPath == "" {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if err := pm.ensureWalWriterLocked(); err != nil {
		return err
	}
	if err := writeWALStoreRecord(pm.walWriter, record); err != nil {
		return err
	}
	if time.Since(pm.lastSync) >= pm.syncInterval {
		if err := pm.flushLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (pm *persistenceManager) appendDelete(recordKey key) error {
	if pm.walPath == "" {
		return nil
	}
	return pm.appendDeletes([]key{recordKey}, false)
}

func (pm *persistenceManager) appendDeleteBatch(recordKeys []key) error {
	return pm.appendDeletes(recordKeys, true)
}

func (pm *persistenceManager) appendDeletes(recordKeys []key, syncNow bool) error {
	if pm.walPath == "" || len(recordKeys) == 0 {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if err := pm.ensureWalWriterLocked(); err != nil {
		return err
	}
	for _, recordKey := range recordKeys {
		if err := writeWALDeleteRecord(pm.walWriter, recordKey); err != nil {
			return err
		}
	}
	if syncNow || time.Since(pm.lastSync) >= pm.syncInterval {
		if err := pm.flushLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (pm *persistenceManager) appendFlush() error {
	if pm.walPath == "" {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if err := pm.ensureWalWriterLocked(); err != nil {
		return err
	}
	if err := writeWALFlushRecord(pm.walWriter); err != nil {
		return err
	}
	return pm.flushLocked()
}

func (pm *persistenceManager) checkpoint(c *Cache) (int, error) {
	if pm.restoreErr != nil {
		return 0, pm.restoreErr
	}
	if pm.snapshotPath == "" {
		return 0, nil
	}
	snapshot, cut, err := pm.captureSnapshot(c)
	if err != nil {
		return 0, err
	}
	// gzip 和磁盘 I/O 不持有变更锁，期间的新操作继续追加旧 WAL。
	entries, err := writeSnapshotFileAtomic(pm.snapshotPath, snapshot.writeDump)
	if err != nil {
		return 0, err
	}
	if err := pm.completeCheckpoint(c, cut); err != nil {
		return entries, err
	}
	return entries, nil
}

func (pm *persistenceManager) captureSnapshot(c *Cache) (cacheSnapshot, uint64, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if err := pm.ensureWalWriterLocked(); err != nil {
		return cacheSnapshot{}, 0, err
	}
	// cut 位于已同步的完整记录边界，与内存采集对应同一个状态。
	if err := pm.flushLocked(); err != nil {
		return cacheSnapshot{}, 0, err
	}
	var cut uint64
	if pm.walFile != nil {
		info, err := pm.walFile.Stat()
		if err != nil {
			return cacheSnapshot{}, 0, err
		}
		payloadSize := info.Size() - pm.header.size()
		if payloadSize < 0 || uint64(payloadSize) > ^uint64(0)-pm.header.base {
			return cacheSnapshot{}, 0, errors.New("invalid cache wal size")
		}
		cut = pm.header.base + uint64(payloadSize)
	}
	snapshot := cacheSnapshot{entries: make([]snapshotEntry, 0, c.backend.Len())}
	if pm.walPath != "" {
		snapshot.checkpoint = &snapshotCheckpoint{generation: pm.header.generation, cut: cut}
	}
	now := time.Now()
	err := c.backend.Range(func(k key, v *item, expiration time.Time) error {
		if !expiration.Before(now) {
			snapshot.entries = append(snapshot.entries, snapshotEntry{
				key: k, resp: v.resp, domainSet: v.domainSet,
				cacheExpiration: expiration.Unix(),
				msgExpiration:   unixNanoToTime(v.expireUnixNano).Unix(),
				msgStored:       unixNanoToTime(v.storedUnixNano).Unix(),
			})
		}
		return nil
	})
	return snapshot, cut, err
}

func (pm *persistenceManager) completeCheckpoint(c *Cache, cut uint64) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.loadedCheckpoint = &snapshotCheckpoint{generation: pm.header.generation, cut: cut}
	// 发布快照后仍保留完整旧 WAL，直到所有并发增量都已同步并复制。
	if err := pm.flushLocked(); err != nil {
		return err
	}
	return pm.resetWALLocked(cut)
}

func (pm *persistenceManager) close() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if err := pm.flushLocked(); err != nil {
		return err
	}
	if pm.walFile != nil {
		err := pm.walFile.Close()
		pm.walFile = nil
		pm.walWriter = nil
		return err
	}
	return nil
}

func (pm *persistenceManager) resetWALLocked(cut uint64) error {
	if pm.walPath == "" {
		return nil
	}
	if pm.walFile == nil || pm.header.version != 2 || cut < pm.header.base {
		return errors.New("invalid cache wal checkpoint boundary")
	}
	info, err := pm.walFile.Stat()
	if err != nil {
		return err
	}
	if info.Size() < pm.header.size() || cut-pm.header.base > uint64(info.Size()-pm.header.size()) {
		return errors.New("cache wal checkpoint boundary exceeds file size")
	}
	offset := pm.header.size() + int64(cut-pm.header.base)
	header := pm.header
	header.base = cut
	return pm.replaceWALLocked(pm.walFile, offset, info.Size()-offset, header)
}

func (pm *persistenceManager) replaceWALLocked(source *os.File, offset, length int64, header walHeader) error {
	dir := filepath.Dir(pm.walPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".cache-wal-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if err := writeWALHeader(f, header); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := io.Copy(f, io.NewSectionReader(source, offset, length)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := os.Rename(tmpPath, pm.walPath); err != nil {
		_ = f.Close()
		return err
	}
	// rename 成功后即切换 writer，目录同步失败时也不能再写旧 inode。
	oldFile := pm.walFile
	pm.walFile = f
	pm.walWriter = bufio.NewWriterSize(f, 64*1024)
	pm.header = header
	pm.lastSync = time.Now()
	if oldFile != nil {
		if err := oldFile.Close(); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

func (pm *persistenceManager) migrateLegacyWAL() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	f, err := os.Open(pm.walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	header, err := readWALHeader(f)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if header.version == 2 {
		pm.header = header
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if header.version == 2 && pm.validWALSize == info.Size() {
		return nil
	}
	if header.version == 1 {
		header, err = newWALHeader()
		if err != nil {
			return err
		}
		return pm.replaceWALLocked(f, int64(len(walMagic)), pm.validWALSize-int64(len(walMagic)), header)
	}
	// 忽略的断尾必须在后续 append 前移除，否则新记录会接在半条旧记录之后。
	return pm.replaceWALLocked(f, header.size(), pm.validWALSize-header.size(), header)
}

func (pm *persistenceManager) ensureWalWriterLocked() error {
	if pm.restoreErr != nil {
		return pm.restoreErr
	}
	if pm.walPath == "" {
		return nil
	}
	if pm.walFile != nil && pm.walWriter != nil {
		return nil
	}
	if pm.walFile != nil {
		if pm.walWriter != nil {
			if err := pm.walWriter.Flush(); err != nil {
				return err
			}
		}
		if err := pm.walFile.Close(); err != nil {
			return err
		}
		pm.walFile = nil
		pm.walWriter = nil
	}
	if err := os.MkdirAll(filepath.Dir(pm.walPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(pm.walPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if info.Size() == 0 {
		header, err := newWALHeader()
		if err != nil {
			_ = f.Close()
			return err
		}
		if pm.loadedCheckpoint != nil {
			header.generation = pm.loadedCheckpoint.generation
			header.base = pm.loadedCheckpoint.cut
		}
		if err := writeWALHeader(f, header); err != nil {
			_ = f.Close()
			return err
		}
		pm.header = header
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err := syncDir(filepath.Dir(pm.walPath)); err != nil {
			_ = f.Close()
			return err
		}
	} else {
		header, err := readWALHeader(io.NewSectionReader(f, 0, info.Size()))
		if err != nil || header.version != 2 {
			_ = f.Close()
			return fmt.Errorf("cache wal was not migrated to v2, %v", err)
		}
		pm.header = header
	}
	pm.walFile = f
	pm.walWriter = bufio.NewWriterSize(f, 64*1024)
	pm.lastSync = time.Now()
	return nil
}

func (pm *persistenceManager) flushLocked() error {
	if pm.walWriter == nil || pm.walFile == nil {
		return nil
	}
	if err := pm.walWriter.Flush(); err != nil {
		return err
	}
	if err := pm.walFile.Sync(); err != nil {
		return err
	}
	pm.lastSync = time.Now()
	return nil
}

func (c *Cache) loadSnapshot() error {
	start := time.Now()
	entries := 0
	c.loadTotalCounter.Inc()
	defer func() {
		c.loadDuration.Observe(time.Since(start).Seconds())
	}()

	f, err := os.Open(c.persistence.snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.runtimeState.recordLoad(0, time.Since(start), nil)
			c.logger.Info("cache dump file not found, skipping load", zap.String("file", c.persistence.snapshotPath))
			return nil
		}
		c.loadErrorCounter.Inc()
		c.runtimeState.recordLoad(0, time.Since(start), err)
		return err
	}
	defer f.Close()
	entries, err = c.readDumpState(f, false, true)
	if err != nil {
		c.loadErrorCounter.Inc()
		c.runtimeState.recordLoad(entries, time.Since(start), err)
		return err
	}
	c.runtimeState.recordLoad(entries, time.Since(start), nil)
	c.logger.Info("cache dump loaded", zap.Int("entries", entries))
	return nil
}

func (c *Cache) replayWAL() error {
	start := time.Now()
	entries := 0
	c.walReplayCounter.Inc()
	defer func() {
		c.walReplayDuration.Observe(time.Since(start).Seconds())
	}()

	f, err := os.Open(c.persistence.walPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.runtimeState.recordReplay(0, time.Since(start), nil)
			return nil
		}
		c.walReplayErrorCounter.Inc()
		c.runtimeState.recordReplay(0, time.Since(start), err)
		return err
	}
	defer f.Close()

	header, err := readWALHeader(f)
	if errors.Is(err, io.EOF) && c.persistence.loadedCheckpoint == nil {
		c.runtimeState.recordReplay(0, time.Since(start), nil)
		return nil
	}
	if err != nil {
		c.walReplayErrorCounter.Inc()
		c.runtimeState.recordReplay(0, time.Since(start), err)
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	offset, err := walReplayOffset(header, c.persistence.loadedCheckpoint, info.Size())
	if err == nil {
		err = validateWALCut(bufio.NewReader(io.NewSectionReader(f, header.size(), offset-header.size())), offset-header.size())
	}
	if err != nil {
		c.walReplayErrorCounter.Inc()
		c.runtimeState.recordReplay(0, time.Since(start), err)
		return err
	}
	reader := &walCountingReader{r: bufio.NewReader(io.NewSectionReader(f, offset, info.Size()-offset))}
	validSize := offset

	for {
		record, err := readWALRecord(reader)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if offset+reader.n != validSize {
					c.logger.Warn("ignore truncated wal tail", zap.Error(err))
				}
				break
			}
			c.walReplayErrorCounter.Inc()
			c.runtimeState.recordReplay(entries, time.Since(start), err)
			return err
		}
		validSize = offset + reader.n
		switch record.op {
		case walOpSet:
			if c.containsExcludedWire(record.cacheItem.resp) {
				c.backend.Delete(record.key)
				c.deleteL1Key(record.key)
				entries++
				continue
			}
			c.prepareCacheItemForStore(record.cacheItem)
			c.backend.Store(record.key, record.cacheItem, record.cacheExp)
		case walOpDel:
			c.backend.Delete(record.key)
			c.deleteL1Key(record.key)
		case walOpFlush:
			c.backend.Flush()
			c.resetL1()
		default:
			err := fmt.Errorf("unsupported wal op %d", record.op)
			c.walReplayErrorCounter.Inc()
			c.runtimeState.recordReplay(entries, time.Since(start), err)
			return err
		}
		entries++
	}
	c.persistence.header = header
	c.persistence.validWALSize = validSize
	c.runtimeState.recordReplay(entries, time.Since(start), nil)
	return nil
}

func (c *Cache) writeSnapshotFileAtomic(path string) (int, error) {
	return writeSnapshotFileAtomic(path, c.writeDump)
}

func writeSnapshotFileAtomic(path string, writeDump func(io.Writer) (int, error)) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cache-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	entries, err := writeDump(tmp)
	if err != nil {
		cleanup()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return entries, err
	}
	return entries, nil
}

func writeWALStoreRecord(w io.Writer, record walStoreRecord) error {
	payload := bytes.NewBuffer(make([]byte, 0, len(record.cacheItem.resp)+len(record.key)+96))
	payload.WriteByte(walOpSet)
	writeInt64(payload, record.cacheExp.Unix())
	writeInt64(payload, unixNanoToTime(record.cacheItem.expireUnixNano).Unix())
	writeInt64(payload, unixNanoToTime(record.cacheItem.storedUnixNano).Unix())
	writeBytes(payload, []byte(record.key))
	writeBytes(payload, record.cacheItem.resp)
	writeString(payload, record.cacheItem.domainSet)

	return writeWALPayload(w, payload.Bytes())
}

func writeWALDeleteRecord(w io.Writer, recordKey key) error {
	payload := bytes.NewBuffer(make([]byte, 0, len(recordKey)+8))
	payload.WriteByte(walOpDel)
	writeBytes(payload, []byte(recordKey))
	return writeWALPayload(w, payload.Bytes())
}

func writeWALFlushRecord(w io.Writer) error {
	return writeWALPayload(w, []byte{walOpFlush})
}

func writeWALPayload(w io.Writer, payload []byte) error {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readWALRecord(r io.Reader) (walRecord, error) {
	payload, err := readSizedBytes(r, maxWALRecordPayloadLength, "cache wal record")
	if err != nil {
		return walRecord{}, err
	}
	buf := bytes.NewReader(payload)
	op, err := buf.ReadByte()
	if err != nil {
		return walRecord{}, err
	}
	if op == walOpDel {
		k, err := readBytes(buf)
		if err != nil {
			return walRecord{}, err
		}
		return walRecord{op: op, walStoreRecord: walStoreRecord{key: key(k)}}, nil
	}
	if op == walOpFlush {
		return walRecord{op: op}, nil
	}
	if op != walOpSet {
		return walRecord{}, fmt.Errorf("unsupported wal op %d", op)
	}
	cacheExpUnix, err := readInt64(buf)
	if err != nil {
		return walRecord{}, err
	}
	msgExpUnix, err := readInt64(buf)
	if err != nil {
		return walRecord{}, err
	}
	storedUnix, err := readInt64(buf)
	if err != nil {
		return walRecord{}, err
	}
	k, err := readBytes(buf)
	if err != nil {
		return walRecord{}, err
	}
	msg, err := readBytes(buf)
	if err != nil {
		return walRecord{}, err
	}
	domainSet, err := readString(buf)
	if err != nil {
		return walRecord{}, err
	}
	return walRecord{
		op: walOpSet,
		walStoreRecord: walStoreRecord{
			key:      key(k),
			cacheExp: time.Unix(cacheExpUnix, 0),
			cacheItem: &item{
				resp:           msg,
				storedUnixNano: time.Unix(storedUnix, 0).UnixNano(),
				expireUnixNano: time.Unix(msgExpUnix, 0).UnixNano(),
				domainSet:      domainSet,
			},
		},
	}, nil
}

func writeInt64(w io.Writer, v int64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	_, _ = w.Write(buf[:])
}

func readInt64(r io.Reader) (int64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(buf[:])), nil
}

func writeBytes(w io.Writer, b []byte) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(len(b)))
	_, _ = w.Write(buf[:])
	_, _ = w.Write(b)
}

func readBytes(r io.Reader) ([]byte, error) {
	return readSizedBytes(r, maxWALRecordPayloadLength, "cache payload field")
}

func writeString(w io.Writer, s string) {
	writeBytes(w, []byte(s))
}

func readString(r io.Reader) (string, error) {
	b, err := readBytes(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
