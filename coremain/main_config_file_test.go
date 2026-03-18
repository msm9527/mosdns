package coremain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAuditSettingsToMainConfig(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	oldFilePath := MainConfigFilePath
	MainConfigBaseDir = t.TempDir()
	MainConfigFilePath = filepath.Join(MainConfigBaseDir, "config.yaml")
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
		MainConfigFilePath = oldFilePath
	})

	initial := `# main comment
version: v2
# audit comment
audit:
  enabled: true
  overview_window_seconds: 60
  raw_retention_days: 7
  aggregate_retention_days: 30
  max_storage_mb: 128
  sqlite_path: db/audit.db
  flush_batch_size: 256
  flush_interval_ms: 250
  maintenance_interval_seconds: 60
`
	if err := os.WriteFile(MainConfigFilePath, []byte(initial), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := saveAuditSettingsToMainConfig(AuditSettings{
		Enabled:                    false,
		OverviewWindowSeconds:      120,
		RawRetentionDays:           14,
		AggregateRetentionDays:     45,
		MaxStorageMB:               64,
		SQLitePath:                 "db/custom-audit.db",
		FlushBatchSize:             512,
		FlushIntervalMs:            500,
		MaintenanceIntervalSeconds: 90,
	})
	if err != nil {
		t.Fatalf("saveAuditSettingsToMainConfig() error = %v", err)
	}

	updated, err := os.ReadFile(MainConfigFilePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	body := string(updated)
	for _, want := range []string{
		"# audit comment",
		"enabled: false",
		"overview_window_seconds: 120",
		"raw_retention_days: 14",
		"aggregate_retention_days: 45",
		"max_storage_mb: 64",
		"sqlite_path: db/custom-audit.db",
		"flush_batch_size: 512",
		"flush_interval_ms: 500",
		"maintenance_interval_seconds: 90",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("updated config missing %q:\n%s", want, body)
		}
	}
}
