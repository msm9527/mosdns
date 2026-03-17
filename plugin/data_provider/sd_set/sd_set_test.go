package sd_set

import (
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func TestSdSetLoadSource(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{
			{
				ID:         "geo-a",
				Name:       "geo-a",
				BindTo:     "geosite_cn",
				Enabled:    true,
				Behavior:   rulesource.BehaviorDomain,
				MatchMode:  rulesource.MatchModeDomainSet,
				Format:     rulesource.FormatList,
				SourceKind: rulesource.SourceKindLocal,
				Path:       "diversion/geo-a.list",
			},
			{
				ID:         "geo-b",
				Name:       "geo-b",
				BindTo:     "geosite_cn",
				Enabled:    true,
				Behavior:   rulesource.BehaviorDomain,
				MatchMode:  rulesource.MatchModeDomainSet,
				Format:     rulesource.FormatList,
				SourceKind: rulesource.SourceKindLocal,
				Path:       "diversion/geo-b.list",
			},
		},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}

	p := &SdSet{configFile: filepath.Join("custom_config", "diversion_sources.yaml"), bindTo: "geosite_cn"}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if len(p.sources) != 2 || p.sources[0].ID != "geo-a" || p.sources[1].ID != "geo-b" {
		t.Fatalf("unexpected sources: %+v", p.sources)
	}
}
