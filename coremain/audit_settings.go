package coremain

import (
	"path/filepath"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

const (
	auditDefaultOverviewWindowSeconds      = 60
	auditMaxOverviewWindowSeconds          = 3600
	auditDefaultRawRetentionDays           = 7
	auditMaxRawRetentionDays               = 365
	auditDefaultAggregateRetentionDays     = 30
	auditMaxAggregateRetentionDays         = 365
	auditDefaultMaxStorageMB               = 128
	auditMaxStorageMB                      = 10240
	auditDefaultFlushBatchSize             = 256
	auditMaxFlushBatchSize                 = 4096
	auditDefaultFlushIntervalMs            = 250
	auditMaxFlushIntervalMs                = 5000
	auditDefaultMaintenanceIntervalSeconds = 60
	auditMaxMaintenanceIntervalSeconds     = 3600
	auditRealtimeBucketCount               = 3600
	runtimeStateKeyAuditConfig             = "settings_v3"
	auditSQLiteFilename                    = "audit.db"
)

func defaultAuditSettings() AuditSettings {
	return AuditSettings{
		Enabled:                    true,
		OverviewWindowSeconds:      auditDefaultOverviewWindowSeconds,
		RawRetentionDays:           auditDefaultRawRetentionDays,
		AggregateRetentionDays:     auditDefaultAggregateRetentionDays,
		MaxStorageMB:               auditDefaultMaxStorageMB,
		FlushBatchSize:             auditDefaultFlushBatchSize,
		FlushIntervalMs:            auditDefaultFlushIntervalMs,
		MaintenanceIntervalSeconds: auditDefaultMaintenanceIntervalSeconds,
	}
}

func normalizeAuditSettings(settings AuditSettings) AuditSettings {
	defaults := defaultAuditSettings()
	if settings.OverviewWindowSeconds <= 0 {
		settings.OverviewWindowSeconds = defaults.OverviewWindowSeconds
	}
	if settings.OverviewWindowSeconds > auditMaxOverviewWindowSeconds {
		settings.OverviewWindowSeconds = auditMaxOverviewWindowSeconds
	}
	if settings.RawRetentionDays <= 0 {
		settings.RawRetentionDays = defaults.RawRetentionDays
	}
	if settings.RawRetentionDays > auditMaxRawRetentionDays {
		settings.RawRetentionDays = auditMaxRawRetentionDays
	}
	if settings.AggregateRetentionDays <= 0 {
		settings.AggregateRetentionDays = defaults.AggregateRetentionDays
	}
	if settings.AggregateRetentionDays > auditMaxAggregateRetentionDays {
		settings.AggregateRetentionDays = auditMaxAggregateRetentionDays
	}
	if settings.MaxStorageMB <= 0 {
		settings.MaxStorageMB = defaults.MaxStorageMB
	}
	if settings.MaxStorageMB > auditMaxStorageMB {
		settings.MaxStorageMB = auditMaxStorageMB
	}
	if settings.FlushBatchSize <= 0 {
		settings.FlushBatchSize = defaults.FlushBatchSize
	}
	if settings.FlushBatchSize > auditMaxFlushBatchSize {
		settings.FlushBatchSize = auditMaxFlushBatchSize
	}
	if settings.FlushIntervalMs <= 0 {
		settings.FlushIntervalMs = defaults.FlushIntervalMs
	}
	if settings.FlushIntervalMs > auditMaxFlushIntervalMs {
		settings.FlushIntervalMs = auditMaxFlushIntervalMs
	}
	if settings.MaintenanceIntervalSeconds <= 0 {
		settings.MaintenanceIntervalSeconds = defaults.MaintenanceIntervalSeconds
	}
	if settings.MaintenanceIntervalSeconds > auditMaxMaintenanceIntervalSeconds {
		settings.MaintenanceIntervalSeconds = auditMaxMaintenanceIntervalSeconds
	}
	return settings
}

func loadAuditSettings(configBaseDir string, base *AuditSettings) AuditSettings {
	settings := defaultAuditSettings()
	if base != nil {
		settings = mergeAuditSettings(settings, *base)
	}
	if runtimeSettings, ok, err := loadAuditSettingsFromRuntimeStore(configBaseDir); err == nil && ok {
		settings = mergeAuditSettings(settings, runtimeSettings)
	} else if err != nil {
		mlog.L().Warn("failed to load audit settings from runtime store", zap.Error(err))
	}
	settings = normalizeAuditSettings(settings)
	mlog.L().Info("loaded audit settings",
		zap.Bool("enabled", settings.Enabled),
		zap.Int("overview_window_seconds", settings.OverviewWindowSeconds),
		zap.Int("raw_retention_days", settings.RawRetentionDays),
		zap.Int("aggregate_retention_days", settings.AggregateRetentionDays),
		zap.Int("max_storage_mb", settings.MaxStorageMB))
	return settings
}

func mergeAuditSettings(base, override AuditSettings) AuditSettings {
	if strings.TrimSpace(override.SQLitePath) == "" {
		override.SQLitePath = base.SQLitePath
	}
	return normalizeAuditSettings(override)
}

func saveAuditSettings(configBaseDir string, settings AuditSettings) error {
	store, err := getRuntimeStateStoreByPath(runtimeStateDBPathForBaseDir(configBaseDir))
	if err != nil {
		return err
	}
	return store.put(runtimeStateNamespaceAudit, runtimeStateKeyAuditConfig, normalizeAuditSettings(settings))
}

func loadAuditSettingsFromRuntimeStore(configBaseDir string) (AuditSettings, bool, error) {
	store, err := getRuntimeStateStoreByPath(runtimeStateDBPathForBaseDir(configBaseDir))
	if err != nil {
		return AuditSettings{}, false, err
	}
	var settings AuditSettings
	ok, err := store.get(runtimeStateNamespaceAudit, runtimeStateKeyAuditConfig, &settings)
	if err != nil {
		return AuditSettings{}, false, err
	}
	return settings, ok, nil
}

func defaultAuditSQLitePath(configBaseDir string) string {
	baseDir := configBaseDir
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, auditLogsDirname, auditSQLiteFilename)
}

func resolveAuditSQLitePath(configBaseDir, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return defaultAuditSQLitePath(configBaseDir)
	}
	if filepath.IsAbs(configured) || configBaseDir == "" {
		return configured
	}
	return filepath.Join(configBaseDir, configured)
}
