package mlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewLoggerCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "mosdns.log")

	logger, err := NewLogger(LogConfig{
		Level: "info",
		File:  path,
	})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	t.Cleanup(func() {
		_ = logger.Sync()
	})

	logger.Info("test logger")
	_ = logger.Sync()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}
}
