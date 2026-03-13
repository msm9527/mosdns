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

	data, outputPath, err := migrateConfigToV2(path, "", false)
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

func TestMigrateConfigToPureDeclarativeV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := `
api:
  http: "127.0.0.1:9099"
include:
  - sub_config/cache.yaml
plugins:
  - tag: domestic
    type: forward
    args:
      upstreams:
        - tls://1.1.1.1
  - tag: webinfo_client
    type: webinfo
    args:
      file: config/webinfo/clientname.json
  - tag: requery_main
    type: requery
    args:
      file: config/webinfo/requeryconfig.json
  - tag: core_mode
    type: switch
    args:
      name: core_mode
      state_file_path: config/switches.json
  - tag: sequence_main
    type: sequence
    args:
      - exec: $domestic
  - tag: udp_all
    type: udp_server
    args:
      listen: ":53"
      entry: sequence_main
      enable_audit: true
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	data, _, err := migrateConfigToV2(path, "", true)
	if err != nil {
		t.Fatalf("migrateConfigToV2() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "legacy:") {
		t.Fatalf("expected pure declarative output, got:\n%s", text)
	}
	if !strings.Contains(text, "rule_providers:") || !strings.Contains(text, "upstreams:") || !strings.Contains(text, "listeners:") || !strings.Contains(text, "runtime:") {
		t.Fatalf("expected declarative sections in output, got:\n%s", text)
	}
}

func TestLoadPureDeclarativeConfigV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.v2.yaml")
	raw := `
version: v2
api:
  http: "127.0.0.1:9099"
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
runtime:
  base_dir: config
  webinfo:
    - name: webinfo_client
      file: webinfo/clientname.json
  requery:
    - name: requery_main
      file: webinfo/requeryconfig.json
  switches:
    - name: core_mode
      state_file: switches.json
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
	if len(cfg.Include) != 1 || cfg.Include[0] != "sub_config/cache.yaml" {
		t.Fatalf("unexpected include: %+v", cfg.Include)
	}
	if len(cfg.Plugins) != 6 {
		t.Fatalf("unexpected plugins: %+v", cfg.Plugins)
	}
	if cfg.Plugins[0].Tag != "domestic" || cfg.Plugins[1].Tag != "sequence_main" || cfg.Plugins[2].Tag != "webinfo_client" || cfg.Plugins[3].Tag != "requery_main" || cfg.Plugins[4].Tag != "core_mode" || cfg.Plugins[5].Tag != "udp_all" {
		t.Fatalf("unexpected plugin order: %+v", cfg.Plugins)
	}
}
