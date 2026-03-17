package coremain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigV2RejectsLegacyKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.v2.yaml")
	raw := `
version: v2
api:
  http: "127.0.0.1:9099"
legacy:
  include:
    - sub_config/cache.yaml
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, _, err := loadConfig(path); err == nil {
		t.Fatalf("expected config v2 with legacy keys to be rejected")
	}
}

func TestLoadConfigRejectsV1Document(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := `
api:
  http: "127.0.0.1:9099"
plugins:
  - tag: udp_all
    type: udp_server
    args:
      listen: ":53"
      entry: sequence_main
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, _, err := loadConfig(path); err == nil {
		t.Fatalf("expected v1 config document to be rejected")
	}
}

func TestLoadPureDeclarativeConfigV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.v2.yaml")
	raw := `
version: v2
api:
  http: "127.0.0.1:9099"
audit:
  sqlite_path: db/audit.db
  enabled: true
  overview_window_seconds: 120
rule_providers:
  - name: cache
    type: include
    source: sub_config/cache.yaml
upstreams:
  - name: domestic
    plugin_type: forward
    endpoints:
      - tls://1.1.1.1
policies:
  - name: sequence_main
    type: sequence
    args:
      - exec: $domestic
control:
  base_dir: config
  requery:
    - name: requery_main
      key: runtime/requery_main
  switches:
    - name: branch_cache
listeners:
  - name: udp_all
    protocol: udp
    listen: ":53"
    entry: sequence_main
    audit: true
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.API.HTTP != "127.0.0.1:9099" {
		t.Fatalf("unexpected api http %q", cfg.API.HTTP)
	}
	if cfg.Audit == nil || cfg.Audit.SQLitePath != "db/audit.db" || cfg.Audit.OverviewWindowSeconds != 120 {
		t.Fatalf("unexpected audit settings: %+v", cfg.Audit)
	}
	if len(cfg.Include) != 1 || cfg.Include[0] != "sub_config/cache.yaml" {
		t.Fatalf("unexpected include: %+v", cfg.Include)
	}
	if len(cfg.Plugins) != 5 {
		t.Fatalf("unexpected plugins: %+v", cfg.Plugins)
	}
	if cfg.Plugins[0].Tag != "domestic" || cfg.Plugins[1].Tag != "sequence_main" || cfg.Plugins[2].Tag != "requery_main" || cfg.Plugins[3].Tag != "branch_cache" || cfg.Plugins[4].Tag != "udp_all" {
		t.Fatalf("unexpected plugin order: %+v", cfg.Plugins)
	}
	switchArgs, ok := cfg.Plugins[3].Args.(map[string]any)
	if !ok || switchArgs["name"] != "branch_cache" {
		t.Fatalf("unexpected switch args: %#v", cfg.Plugins[3].Args)
	}
}
