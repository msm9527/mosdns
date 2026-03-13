package coremain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigV2LegacyCompile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.v2.yaml")
	raw := `
version: v2
api:
  http: "127.0.0.1:9099"
legacy:
  include:
    - sub_config/cache.yaml
  plugins:
    - tag: udp_all
      type: udp_server
      args:
        listen: ":53"
        entry: sequence_main
`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, fileUsed, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if fileUsed != path {
		t.Fatalf("unexpected fileUsed %q", fileUsed)
	}
	if cfg.API.HTTP != "127.0.0.1:9099" {
		t.Fatalf("unexpected api http %q", cfg.API.HTTP)
	}
	if len(cfg.Include) != 1 || cfg.Include[0] != "sub_config/cache.yaml" {
		t.Fatalf("unexpected include: %+v", cfg.Include)
	}
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].Type != "udp_server" {
		t.Fatalf("unexpected plugins: %+v", cfg.Plugins)
	}
}

func TestMigrateConfigToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := `
log:
  level: info
api:
  http: "127.0.0.1:9099"
include:
  - sub_config/cache.yaml
plugins:
  - tag: udp_all
    type: udp_server
    args:
      listen: ":53"
      entry: sequence_main
      enable_audit: true
`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	data, outputPath, err := migrateConfigToV2(path, "")
	if err != nil {
		t.Fatalf("migrateConfigToV2() error = %v", err)
	}
	if !strings.Contains(string(data), "version: v2") {
		t.Fatalf("expected version in migrated config, got:\n%s", string(data))
	}
	if !strings.Contains(string(data), "legacy:") {
		t.Fatalf("expected legacy block in migrated config, got:\n%s", string(data))
	}
	if outputPath != filepath.Join(dir, "config.v2.yaml") {
		t.Fatalf("unexpected output path %q", outputPath)
	}
}
