package sd_set_light

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	p := &SdSetLight{configFile: filepath.Join("custom_config", "diversion_sources.yaml"), bindTo: "geosite_cn"}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if len(p.sources) != 1 || p.sources[0].ID != "geo" {
		t.Fatalf("unexpected sources: %+v", p.sources)
	}
}

func TestSdSetLightReloadAllRulesSkipsUnchangedSources(t *testing.T) {
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

	p := &SdSetLight{
		baseDir:    dir,
		configFile: filepath.Join("custom_config", "diversion_sources.yaml"),
		bindTo:     "geosite_cn",
		httpClient: &http.Client{},
		ctx:        context.Background(),
	}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}

	notifyCh := make(chan struct{}, 2)
	p.Subscribe(func() { notifyCh <- struct{}{} })

	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		t.Fatalf("first reloadAllRules: %v", err)
	}
	waitForSdSetLightNotify(t, notifyCh)

	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		t.Fatalf("second reloadAllRules: %v", err)
	}
	select {
	case <-notifyCh:
		t.Fatal("expected unchanged source reload to be skipped")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSdSetLightAllowsMissingRemoteCacheOnStartup(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:                  "remote",
			Name:                "remote",
			BindTo:              "geosite_cn",
			Enabled:             true,
			Behavior:            rulesource.BehaviorDomain,
			MatchMode:           rulesource.MatchModeDomainSet,
			Format:              rulesource.FormatList,
			SourceKind:          rulesource.SourceKindRemote,
			Path:                "diversion/remote.list",
			URL:                 "https://example.invalid/remote.list",
			AutoUpdate:          true,
			UpdateIntervalHours: 24,
		}},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}

	p := &SdSetLight{
		baseDir:     dir,
		configFile:  filepath.Join("custom_config", "diversion_sources.yaml"),
		bindTo:      "geosite_cn",
		httpClient:  &http.Client{Transport: failingRoundTripper{}},
		ctx:         context.Background(),
		subscribers: make([]func(), 0),
	}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if err := p.reloadAllRules(coremain.StartupRuleSourceSyncOptions()); err != nil {
		t.Fatalf("startup reloadAllRules: %v", err)
	}
	if rules, err := p.GetRules(); err != nil || len(rules) != 0 {
		t.Fatalf("expected empty rules on missing cache, rules=%v err=%v", rules, err)
	}
	statuses, err := coremain.ListRuleSourceStatusByScope(p.runtimeDBPath(), rulesource.ScopeDiversion)
	if err != nil {
		t.Fatalf("ListRuleSourceStatusByScope: %v", err)
	}
	if status := statuses["remote"]; !strings.Contains(status.LastError, "download deferred") {
		t.Fatalf("expected deferred remote status, got %+v", status)
	}
}

func TestSdSetLightListEntriesSupportsQueryAndPaging(t *testing.T) {
	p := &SdSetLight{
		pluginTag: "auto_confirm_direct",
		rules: []string{
			"domain:example.com",
			"full:api.example.com",
			"domain:google.com",
		},
	}

	items, total, err := p.ListEntries("example", 1, 1)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(items) != 1 || items[0].Value != "full:api.example.com" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestSdSetLightReplaceListRuntimeWritesSingleLocalSource(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{{
			ID:         "auto_confirm_direct_local",
			Name:       "auto",
			BindTo:     "auto_confirm_direct",
			Enabled:    true,
			Behavior:   rulesource.BehaviorDomain,
			MatchMode:  rulesource.MatchModeDomainSet,
			Format:     rulesource.FormatList,
			SourceKind: rulesource.SourceKindLocal,
			Path:       "diversion/auto_confirm_direct.list",
		}},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}

	p := &SdSetLight{
		baseDir:     dir,
		configFile:  filepath.Join("custom_config", "diversion_sources.yaml"),
		bindTo:      "auto_confirm_direct",
		httpClient:  &http.Client{},
		ctx:         context.Background(),
		subscribers: make([]func(), 0),
	}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}

	notifyCh := make(chan struct{}, 1)
	p.Subscribe(func() { notifyCh <- struct{}{} })

	replaced, err := p.ReplaceListRuntime(context.Background(), []string{
		"domain:example.com",
		"",
		"full:api.example.com",
	})
	if err != nil {
		t.Fatalf("ReplaceListRuntime: %v", err)
	}
	if replaced != 2 {
		t.Fatalf("expected replaced=2, got %d", replaced)
	}
	waitForSdSetLightNotify(t, notifyCh)

	rules, err := p.GetRules()
	if err != nil {
		t.Fatalf("GetRules: %v", err)
	}
	if strings.Join(rules, ",") != "domain:example.com,full:api.example.com" {
		t.Fatalf("unexpected rules: %#v", rules)
	}
	body, err := os.ReadFile(filepath.Join(dir, "diversion", "auto_confirm_direct.list"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := string(body); got != "domain:example.com\nfull:api.example.com\n" {
		t.Fatalf("unexpected file content: %q", got)
	}
}

func TestSdSetLightReplaceListRuntimeRejectsMultiSourceBinding(t *testing.T) {
	dir := t.TempDir()
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() { coremain.MainConfigBaseDir = "" })

	cfg := rulesource.Config{
		Sources: []rulesource.Source{
			{
				ID:         "one",
				Name:       "one",
				BindTo:     "auto_confirm_direct",
				Enabled:    true,
				Behavior:   rulesource.BehaviorDomain,
				MatchMode:  rulesource.MatchModeDomainSet,
				Format:     rulesource.FormatList,
				SourceKind: rulesource.SourceKindLocal,
				Path:       "diversion/one.list",
			},
			{
				ID:         "two",
				Name:       "two",
				BindTo:     "auto_confirm_direct",
				Enabled:    true,
				Behavior:   rulesource.BehaviorDomain,
				MatchMode:  rulesource.MatchModeDomainSet,
				Format:     rulesource.FormatList,
				SourceKind: rulesource.SourceKindLocal,
				Path:       "diversion/two.list",
			},
		},
	}
	if err := coremain.SaveDiversionSourcesToCustomConfig(cfg); err != nil {
		t.Fatalf("SaveDiversionSourcesToCustomConfig: %v", err)
	}

	p := &SdSetLight{
		baseDir:    dir,
		configFile: filepath.Join("custom_config", "diversion_sources.yaml"),
		bindTo:     "auto_confirm_direct",
	}
	if err := p.loadSources(); err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if _, err := p.ReplaceListRuntime(context.Background(), []string{"domain:example.com"}); err == nil {
		t.Fatal("expected multi-source binding to be read-only")
	}
}

func TestSdSetLightReplaceListRuntimeRejectsRemoteSource(t *testing.T) {
	p := &SdSetLight{
		pluginTag: "geosite_cn",
		baseDir:   t.TempDir(),
		sources: []rulesource.Source{{
			ID:         "geosite_cn",
			Name:       "geo",
			BindTo:     "geosite_cn",
			Enabled:    true,
			Behavior:   rulesource.BehaviorDomain,
			MatchMode:  rulesource.MatchModeDomainSet,
			Format:     rulesource.FormatSRS,
			SourceKind: rulesource.SourceKindRemote,
			Path:       "diversion/geosite-cn.srs",
			URL:        "https://example.invalid/geosite-cn.srs",
		}},
	}
	if _, err := p.ReplaceListRuntime(context.Background(), []string{"domain:example.com"}); err == nil {
		t.Fatal("expected remote source to be read-only")
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

func waitForSdSetLightNotify(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sd_set_light reload notification")
	}
}
