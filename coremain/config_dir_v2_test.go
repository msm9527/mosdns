package coremain

import (
	"path/filepath"
	"testing"
)

func TestConfigDirV2DoesNotIncludeRemovedCoreMode(t *testing.T) {
	cfg, fileUsed, err := loadConfig(filepath.Join("..", "config", "config.yaml"))
	if err != nil {
		t.Fatalf("load config dir v2: %v", err)
	}
	if cfg.Audit == nil || cfg.Audit.SQLitePath != "db/audit.db" {
		t.Fatalf("unexpected audit config: %+v", cfg.Audit)
	}
	plugins, err := collectConfigPlugins(fileUsed)
	if err != nil {
		t.Fatalf("collect config plugins: %v", err)
	}
	gotInventory := make(map[string]string, len(plugins))
	for _, plugin := range plugins {
		gotInventory[plugin.Tag] = plugin.Type
	}
	if _, ok := gotInventory["core_mode"]; ok {
		t.Fatalf("removed core_mode switch still exists in config inventory")
	}
	if gotInventory["branch_cache"] != "switch" {
		t.Fatalf("expected branch_cache switch in config inventory, got %q", gotInventory["branch_cache"])
	}
}

func collectConfigPlugins(path string) ([]PluginConfig, error) {
	cfg, fileUsed, err := loadConfig(path)
	if err != nil {
		return nil, err
	}

	plugins := make([]PluginConfig, 0, len(cfg.Plugins))
	for _, includePath := range cfg.Include {
		resolved := includePath
		if !filepath.IsAbs(includePath) {
			resolved = filepath.Join(filepath.Dir(fileUsed), includePath)
		}
		subPlugins, err := collectConfigPlugins(resolved)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, subPlugins...)
	}
	plugins = append(plugins, cfg.Plugins...)
	return plugins, nil
}
