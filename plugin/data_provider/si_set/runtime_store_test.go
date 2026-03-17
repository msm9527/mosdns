package si_set

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSiSetConfigPrefersRuntimeStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rules.json")

	p := &SiSet{
		sources:         map[string]*RuleSource{"test": {Name: "test", Type: "geoipcn", Files: "a.srs", URL: "https://example.com/a.srs", Enabled: true}},
		localConfigFile: configPath,
	}
	if err := p.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`[]`), 0o644); err != nil {
		t.Fatalf("overwrite file: %v", err)
	}

	p2 := &SiSet{sources: make(map[string]*RuleSource), localConfigFile: configPath}
	if err := p2.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(p2.sources) != 1 || p2.sources["test"] == nil {
		t.Fatalf("unexpected sources: %+v", p2.sources)
	}
}
