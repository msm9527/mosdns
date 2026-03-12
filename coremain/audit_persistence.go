package coremain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

const auditDiskCleanupInterval = time.Minute

type auditLogFileInfo struct {
	path string
	date time.Time
	size int64
}

func normalizeAuditSettings(settings AuditSettings) AuditSettings {
	if settings.MemoryEntries <= 0 {
		if settings.Capacity > 0 {
			settings.MemoryEntries = settings.Capacity
		} else {
			settings.MemoryEntries = defaultAuditMemoryEntries
		}
	}
	if settings.MemoryEntries > maxAuditMemoryEntries {
		settings.MemoryEntries = maxAuditMemoryEntries
	}
	if settings.RetentionDays <= 0 {
		settings.RetentionDays = defaultAuditRetentionDays
	}
	if settings.RetentionDays > maxAuditRetentionDays {
		settings.RetentionDays = maxAuditRetentionDays
	}
	if settings.MaxDiskSizeMB <= 0 {
		settings.MaxDiskSizeMB = defaultAuditMaxDiskSizeMB
	}
	if settings.MaxDiskSizeMB > maxAuditMaxDiskSizeMB {
		settings.MaxDiskSizeMB = maxAuditMaxDiskSizeMB
	}
	settings.Capacity = settings.MemoryEntries
	return settings
}

func loadAuditSettings(configBaseDir string) AuditSettings {
	settings := AuditSettings{
		MemoryEntries: defaultAuditMemoryEntries,
		RetentionDays: defaultAuditRetentionDays,
		MaxDiskSizeMB: defaultAuditMaxDiskSizeMB,
	}
	settingsPath := filepath.Join(configBaseDir, auditSettingsFilename)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return settings
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		mlog.L().Warn("failed to parse audit settings, using defaults", zap.String("path", settingsPath), zap.Error(err))
		return normalizeAuditSettings(settings)
	}
	settings = normalizeAuditSettings(settings)
	mlog.L().Info("loaded audit log settings",
		zap.Int("memory_entries", settings.MemoryEntries),
		zap.Int("retention_days", settings.RetentionDays),
		zap.Int("max_disk_size_mb", settings.MaxDiskSizeMB))
	return settings
}

func saveAuditSettings(configBaseDir string, settings AuditSettings) error {
	settings = normalizeAuditSettings(settings)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(configBaseDir, auditSettingsFilename)
	return os.WriteFile(settingsPath, data, 0o644)
}

func (c *AuditCollector) appendBatchToDisk(logs []AuditLog) error {
	if len(logs) == 0 || c.logDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.logDir, 0o755); err != nil {
		return err
	}

	type payload struct {
		path string
		data []byte
	}
	grouped := make(map[string]*bytes.Buffer)
	for _, log := range logs {
		day := log.QueryTime.Format("2006-01-02")
		buf := grouped[day]
		if buf == nil {
			buf = &bytes.Buffer{}
			grouped[day] = buf
		}
		line, err := json.Marshal(log)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	for day, buf := range grouped {
		path := filepath.Join(c.logDir, fmt.Sprintf("audit-%s.ndjson", day))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(buf.Bytes()); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (c *AuditCollector) restoreFromDisk() error {
	if c.logDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.logDir, 0o755); err != nil {
		return err
	}
	if err := c.enforceDiskRetention(); err != nil {
		return err
	}

	files, err := c.listAuditLogFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 || c.capacity == 0 {
		return nil
	}

	recent := make([]AuditLog, 0, c.capacity)
	for i := len(files) - 1; i >= 0 && len(recent) < c.capacity; i-- {
		logs, err := readAuditLogFile(files[i].path)
		if err != nil {
			mlog.L().Warn("failed to read persisted audit log file", zap.String("path", files[i].path), zap.Error(err))
			continue
		}
		for j := len(logs) - 1; j >= 0 && len(recent) < c.capacity; j-- {
			recent = append(recent, logs[j])
		}
	}

	c.mu.Lock()
	c.logs = make([]AuditLog, 0, c.capacity)
	c.head = 0
	for i := len(recent) - 1; i >= 0; i-- {
		c.appendLogLocked(recent[i])
	}
	c.rebuildDerivedLocked()
	c.mu.Unlock()
	return nil
}

func readAuditLogFile(path string) ([]AuditLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	logs := make([]AuditLog, 0, 1024)
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var log AuditLog
		if err := json.Unmarshal([]byte(line), &log); err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return logs, nil
}

func (c *AuditCollector) maybeEnforceDiskRetention() error {
	c.mu.RLock()
	if time.Since(c.lastDiskCleanup) < auditDiskCleanupInterval {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	if err := c.enforceDiskRetention(); err != nil {
		return err
	}

	c.mu.Lock()
	c.lastDiskCleanup = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *AuditCollector) enforceDiskRetention() error {
	if c.logDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.logDir, 0o755); err != nil {
		return err
	}
	files, err := c.listAuditLogFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	c.mu.RLock()
	settings := c.settings
	c.mu.RUnlock()

	cutoffDay := time.Now().AddDate(0, 0, -(settings.RetentionDays - 1))
	cutoff := time.Date(cutoffDay.Year(), cutoffDay.Month(), cutoffDay.Day(), 0, 0, 0, 0, cutoffDay.Location())
	kept := make([]auditLogFileInfo, 0, len(files))
	for _, file := range files {
		if file.date.Before(cutoff) {
			if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		kept = append(kept, file)
	}

	maxBytes := int64(settings.MaxDiskSizeMB) * 1024 * 1024
	var total int64
	for _, file := range kept {
		total += file.size
	}
	for len(kept) > 0 && total > maxBytes {
		oldest := kept[0]
		if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= oldest.size
		kept = kept[1:]
	}
	return nil
}

func (c *AuditCollector) clearDiskLogs() error {
	if c.logDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.logDir, 0o755); err != nil {
		return err
	}
	files, err := c.listAuditLogFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (c *AuditCollector) GetDiskUsageBytes() int64 {
	files, err := c.listAuditLogFiles()
	if err != nil {
		return 0
	}
	var total int64
	for _, file := range files {
		total += file.size
	}
	return total
}

func (c *AuditCollector) listAuditLogFiles() ([]auditLogFileInfo, error) {
	if c.logDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(c.logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]auditLogFileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".ndjson") {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".ndjson")
		day, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errorsIsNotExist(err) {
				continue
			}
			return nil, err
		}
		files = append(files, auditLogFileInfo{
			path: filepath.Join(c.logDir, name),
			date: day,
			size: info.Size(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].date.Before(files[j].date)
	})
	return files, nil
}

func errorsIsNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || err == fs.ErrNotExist)
}
