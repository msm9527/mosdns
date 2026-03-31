package coremain

import (
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/mlog"
)

func TestResolveLogConfigForBaseDir(t *testing.T) {
	baseDir := t.TempDir()
	cfg := mlog.LogConfig{
		Level: "info",
		File:  "logs/mosdns.log",
	}

	got := resolveLogConfigForBaseDir(baseDir, cfg)

	want := filepath.Join(baseDir, "logs", "mosdns.log")
	if got.File != want {
		t.Fatalf("unexpected log file: got %q want %q", got.File, want)
	}
}
