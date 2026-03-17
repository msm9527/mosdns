package sd_set_light

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSdSetLightConfigPrefersRuntimeStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rules.json")

	p := &SdSetLight{
		sources:         map[string]*RuleSource{"test": {Name: "test", Type: "cuscn", Files: "a.srs", URL: "https://example.com/a.srs", Enabled: true}},
		localConfigFile: configPath,
	}
	if err := p.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`[]`), 0o644); err != nil {
		t.Fatalf("overwrite file: %v", err)
	}

	p2 := &SdSetLight{sources: make(map[string]*RuleSource), localConfigFile: configPath}
	if err := p2.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(p2.sources) != 1 || p2.sources["test"] == nil {
		t.Fatalf("unexpected sources: %+v", p2.sources)
	}
}
