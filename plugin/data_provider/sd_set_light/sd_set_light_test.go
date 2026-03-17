package sd_set_light

import (
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func TestSdSetLightLoadSource(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:         "geo",
			Name:       "geo",
			Enabled:    true,
			Behavior:   rulesource.BehaviorDomain,
			MatchMode:  rulesource.MatchModeDomainSet,
			Format:     rulesource.FormatList,
			SourceKind: rulesource.SourceKindLocal,
			Path:       "diversion/geo.list",
		}},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}

	p := &SdSetLight{configFile: filepath.Join("custom_config", "diversion_sources.yaml"), sourceID: "geo"}
	if err := p.loadSource(); err != nil {
		t.Fatalf("loadSource: %v", err)
	}
	if p.source.ID != "geo" {
		t.Fatalf("unexpected source: %+v", p.source)
	}
}
