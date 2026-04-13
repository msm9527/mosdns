package sd_set

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
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

func TestSdSetReloadAllRulesSkipsUnchangedSources(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:         "geo",
			Name:       "geo",
			BindTo:     "geosite_cn",
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
	if err := os.MkdirAll(filepath.Join(dir, "diversion"), 0o755); err != nil {
		t.Fatalf("MkdirAll(diversion): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "diversion", "geo.list"), []byte("example.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(geo.list): %v", err)
	}

	p := &SdSet{
		baseDir:     dir,
		configFile:  filepath.Join("custom_config", "diversion_sources.yaml"),
		bindTo:      "geosite_cn",
		httpClient:  &http.Client{},
		ctx:         context.Background(),
		subscribers: make([]func(), 0),
	}
	p.matcher.Store(domain.NewDomainMixMatcher())
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}

	notifyCh := make(chan struct{}, 2)
	p.Subscribe(func() { notifyCh <- struct{}{} })

	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		t.Fatalf("first reloadAllRules: %v", err)
	}
	waitForSdSetNotify(t, notifyCh)

	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		t.Fatalf("second reloadAllRules: %v", err)
	}
	select {
	case <-notifyCh:
		t.Fatal("expected unchanged source reload to be skipped")
	case <-time.After(200 * time.Millisecond):
	}
}

func waitForSdSetNotify(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sd_set reload notification")
	}
}
