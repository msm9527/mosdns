package coremain

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

type legacyInventory struct {
    Plugins []struct {
        Tag  string `json:"tag"`
        Type string `json:"type"`
    } `json:"plugins"`
}

func TestConfigDirV2MatchesLegacyPluginParity(t *testing.T) {
    fixturePath := filepath.Join("testdata", "config_legacy_ordered.json")
    data, err := os.ReadFile(fixturePath)
    if err != nil {
        t.Fatalf("read fixture: %v", err)
    }
    var fixture legacyInventory
    if err := json.Unmarshal(data, &fixture); err != nil {
        t.Fatalf("decode fixture: %v", err)
    }

    cfg, _, err := loadConfig(filepath.Join("..", "config", "config.yaml"))
    if err != nil {
        t.Fatalf("load config dir v2: %v", err)
    }
    if cfg.Audit == nil || cfg.Audit.SQLitePath != "db/audit.db" {
        t.Fatalf("unexpected audit config: %+v", cfg.Audit)
    }
    if len(cfg.Plugins) != len(fixture.Plugins) {
        t.Fatalf("plugin count mismatch: got %d want %d", len(cfg.Plugins), len(fixture.Plugins))
    }
    for i, expected := range fixture.Plugins {
        got := cfg.Plugins[i]
        if got.Tag != expected.Tag || got.Type != expected.Type {
            t.Fatalf("plugin parity mismatch at %d: got %s/%s want %s/%s", i, got.Tag, got.Type, expected.Tag, expected.Type)
        }
    }
}
