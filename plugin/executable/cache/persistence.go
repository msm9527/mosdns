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

	mu        sync.Mutex
	walFile   *os.File
	walWriter *bufio.Writer
	lastSync  time.Time
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

func newPersistenceManager(args *Args, logger *zap.Logger) *persistenceManager {
	pm := &persistenceManager{
		logger:       logger,
		snapshotPath: args.DumpFile,
		walPath:      args.WALFile,
		syncInterval: time.Duration(args.WALSyncInterval) * time.Second,
	}
	if pm.syncInterval <= 0 {
		pm.syncInterval = time.Second
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
	return c.replayWAL()
}

func (pm *persistenceManager) appendStore(record walStoreRecord) error {
	if pm.walPath == "" {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if err := pm.ensureWalWriterLocked(false); err != nil {
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

	if err := pm.ensureWalWriterLocked(false); err != nil {
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

	if err := pm.ensureWalWriterLocked(false); err != nil {
		return err
	}
	if err := writeWALFlushRecord(pm.walWriter); err != nil {
		return err
	}
	return pm.flushLocked()
}

func (pm *persistenceManager) checkpoint(c *Cache) (int, error) {
	if pm.snapshotPath == "" {
		return 0, nil
	}
	entries, err := c.writeSnapshotFileAtomic(pm.snapshotPath)
	if err != nil {
		return 0, err
	}
	if err := pm.resetWAL(); err != nil {
		return entries, err
	}
	return entries, nil
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

func (pm *persistenceManager) resetWAL() error {
	if pm.walPath == "" {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.walFile != nil {
		if err := pm.walFile.Close(); err != nil {
			return err
		}
		pm.walFile = nil
		pm.walWriter = nil
	}
	return pm.ensureWalWriterLocked(true)
}

func (pm *persistenceManager) ensureWalWriterLocked(truncate bool) error {
	if pm.walPath == "" {
		return nil
	}
	if !truncate && pm.walFile != nil && pm.walWriter != nil {
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
	flag := os.O_CREATE | os.O_RDWR
	if truncate {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}
	f, err := os.OpenFile(pm.walPath, flag, 0o644)
	if err != nil {
		return err
	}
	if truncate {
		if _, err := f.WriteString(walMagic); err != nil {
			_ = f.Close()
			return err
		}
	} else {
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		if info.Size() == 0 {
			if _, err := f.WriteString(walMagic); err != nil {
				_ = f.Close()
				return err
			}
		}
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
	entries, err = c.readDump(f)
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

	magic := make([]byte, len(walMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		if errors.Is(err, io.EOF) {
			c.runtimeState.recordReplay(0, time.Since(start), nil)
			return nil
		}
		c.walReplayErrorCounter.Inc()
		c.runtimeState.recordReplay(0, time.Since(start), err)
		return err
	}
	if string(magic) != walMagic {
		err := fmt.Errorf("invalid wal header")
		c.walReplayErrorCounter.Inc()
		c.runtimeState.recordReplay(0, time.Since(start), err)
		return err
	}

	for {
		record, err := readWALRecord(f)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				c.logger.Warn("ignore truncated wal tail", zap.Error(err))
				break
			}
			c.walReplayErrorCounter.Inc()
			c.runtimeState.recordReplay(entries, time.Since(start), err)
			return err
		}
		switch record.op {
		case walOpSet:
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
	c.runtimeState.recordReplay(entries, time.Since(start), nil)
	return nil
}

func (c *Cache) writeSnapshotFileAtomic(path string) (int, error) {
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

	entries, err := c.writeDump(tmp)
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
