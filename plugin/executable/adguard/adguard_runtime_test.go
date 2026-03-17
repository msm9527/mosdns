package adguard_rule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdguardConfigPrefersRuntimeStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, configFile)

	rule := &OnlineRule{
		ID:                  "rule-1",
		Name:                "test",
		URL:                 "https://example.com/rules.txt",
		Enabled:             true,
		AutoUpdate:          true,
		UpdateIntervalHours: 24,
		LastUpdated:         time.Unix(1710000000, 0).UTC(),
	}

	p := &AdguardRule{
		dir:         dir,
		configFile:  configPath,
		onlineRules: map[string]*OnlineRule{"rule-1": rule},
	}
	if err := p.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	if err := os.WriteFile(configPath, []byte(`[]`), 0o644); err != nil {
		t.Fatalf("overwrite config file: %v", err)
	}

	p2 := &AdguardRule{
		dir:         dir,
		configFile:  configPath,
		onlineRules: make(map[string]*OnlineRule),
	}
	if err := p2.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(p2.onlineRules) != 1 || p2.onlineRules["rule-1"] == nil {
		t.Fatalf("unexpected runtime-loaded rules: %+v", p2.onlineRules)
	}
	if p2.onlineRules["rule-1"].localPath != filepath.Join(dir, "rule-1.rules") {
		t.Fatalf("unexpected localPath: %q", p2.onlineRules["rule-1"].localPath)
	}
}
