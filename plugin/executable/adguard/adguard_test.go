package adguard_rule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func TestAdguardLoadSources(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:         "block",
			Name:       "block",
			Enabled:    true,
			Behavior:   rulesource.BehaviorAdguard,
			MatchMode:  rulesource.MatchModeAdguardNative,
			Format:     rulesource.FormatRules,
			SourceKind: rulesource.SourceKindLocal,
			Path:       "adguard/block.rules",
		}},
	}
	if err := coremain.SaveAdguardSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveAdguardSourcesToCustomConfig: %v", err)
	}

	p := &AdguardRule{configFile: filepath.Join("custom_config", "adguard_sources.yaml")}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if len(p.sources) != 1 || p.sources[0].ID != "block" {
		t.Fatalf("unexpected sources: %+v", p.sources)
	}
}

func TestAdguardLoadSourcesRejectsCommentOnlyConfig(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	if err := os.MkdirAll(filepath.Join(dir, "custom_config"), 0o755); err != nil {
		t.Fatalf("MkdirAll(custom_config): %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "custom_config", "adguard_sources.yaml"),
		[]byte("# empty truth source\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(adguard_sources.yaml): %v", err)
	}

	p := &AdguardRule{configFile: filepath.Join("custom_config", "adguard_sources.yaml")}
	err := p.loadSources()
	if err == nil {
		t.Fatal("expected loadSources to fail")
	}
	if !strings.Contains(err.Error(), "has no sources") {
		t.Fatalf("unexpected error: %v", err)
	}
}
