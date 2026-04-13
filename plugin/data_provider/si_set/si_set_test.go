package si_set

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/netlist"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func TestSiSetLoadSource(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:         "geo",
			Name:       "geo",
			BindTo:     "geoip_cn",
			Enabled:    true,
			Behavior:   rulesource.BehaviorIPCIDR,
			MatchMode:  rulesource.MatchModeIPCIDRSet,
			Format:     rulesource.FormatList,
			SourceKind: rulesource.SourceKindLocal,
			Path:       "diversion/geo.list",
		}},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}

	p := &SiSet{configFile: filepath.Join("custom_config", "diversion_sources.yaml"), bindTo: "geoip_cn"}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if len(p.sources) != 1 || p.sources[0].ID != "geo" {
		t.Fatalf("unexpected sources: %+v", p.sources)
	}
}

func TestSiSetReloadAllRulesSkipsUnchangedSources(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:         "geo",
			Name:       "geo",
			BindTo:     "geoip_cn",
			Enabled:    true,
			Behavior:   rulesource.BehaviorIPCIDR,
			MatchMode:  rulesource.MatchModeIPCIDRSet,
			Format:     rulesource.FormatList,
			SourceKind: rulesource.SourceKindLocal,
			Path:       "diversion/geo.list",
		}},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "diversion"), 0o755); err != nil {
		t.Fatalf("MkdirAll(diversion): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "diversion", "geo.list"), []byte("1.1.1.0/24\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(geo.list): %v", err)
	}

	p := &SiSet{
		baseDir:    dir,
		configFile: filepath.Join("custom_config", "diversion_sources.yaml"),
		bindTo:     "geoip_cn",
		httpClient: &http.Client{},
		ctx:        context.Background(),
	}
	p.matcher.Store(netlist.NewList())
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}

	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		t.Fatalf("first reloadAllRules: %v", err)
	}
	first := p.matcher.Load()

	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		t.Fatalf("second reloadAllRules: %v", err)
	}
	if second := p.matcher.Load(); first != second {
		t.Fatal("expected unchanged source reload to keep existing matcher instance")
	}
}
