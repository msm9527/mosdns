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
	walMagic = "mosdns_cache_wal_v1\n"
	walOpSet = byte(1)
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
	if err := writeWALRecord(pm.walWriter, record); err != nil {
		return err
	}
	if time.Since(pm.lastSync) >= pm.syncInterval {
		if err := pm.flushLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (pm *persistenceManager) checkpoint(c *Cache) (int, error) {
	if pm.snapshotPath == "" {
		return 0, pm.resetWAL()
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
		c.backend.Store(record.key, record.cacheItem, record.cacheExp)
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

func writeWALRecord(w io.Writer, record walStoreRecord) error {
	payload := bytes.NewBuffer(make([]byte, 0, len(record.cacheItem.resp)+len(record.key)+96))
	payload.WriteByte(walOpSet)
	writeInt64(payload, record.cacheExp.Unix())
	writeInt64(payload, record.cacheItem.expirationTime.Unix())
	writeInt64(payload, record.cacheItem.storedTime.Unix())
	writeBytes(payload, []byte(record.key))
	writeBytes(payload, record.cacheItem.resp)
	writeString(payload, record.cacheItem.domainSet)

	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(payload.Len()))
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	_, err := w.Write(payload.Bytes())
	return err
}

func readWALRecord(r io.Reader) (walStoreRecord, error) {
	payload, err := readSizedBytes(r, maxWALRecordPayloadLength, "cache wal record")
	if err != nil {
		return walStoreRecord{}, err
	}
	buf := bytes.NewReader(payload)
	op, err := buf.ReadByte()
	if err != nil {
		return walStoreRecord{}, err
	}
	if op != walOpSet {
		return walStoreRecord{}, fmt.Errorf("unsupported wal op %d", op)
	}
	cacheExpUnix, err := readInt64(buf)
	if err != nil {
		return walStoreRecord{}, err
	}
	msgExpUnix, err := readInt64(buf)
	if err != nil {
		return walStoreRecord{}, err
	}
	storedUnix, err := readInt64(buf)
	if err != nil {
		return walStoreRecord{}, err
	}
	k, err := readBytes(buf)
	if err != nil {
		return walStoreRecord{}, err
	}
	msg, err := readBytes(buf)
	if err != nil {
		return walStoreRecord{}, err
	}
	domainSet, err := readString(buf)
	if err != nil {
		return walStoreRecord{}, err
	}
	return walStoreRecord{
		key:      key(k),
		cacheExp: time.Unix(cacheExpUnix, 0),
		cacheItem: &item{
			resp:           msg,
			storedTime:     time.Unix(storedUnix, 0),
			expirationTime: time.Unix(msgExpUnix, 0),
			domainSet:      domainSet,
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
