package adguard_rule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
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

func TestAdguardMatchHonorsImportantPriority(t *testing.T) {
	p := buildAdguardRuleForTest(t, "||example.org^$important\n@@||example.org^\n")
	if _, blocked := p.Match("www.example.org"); !blocked {
		t.Fatal("expected important deny to override normal allow")
	}

	p = buildAdguardRuleForTest(t, "||example.org^$important\n@@||example.org^$important\n")
	if _, blocked := p.Match("www.example.org"); blocked {
		t.Fatal("expected important allow to override important deny")
	}
}

func TestAdguardMatchSupportsBadfilterAndDenyAllow(t *testing.T) {
	p := buildAdguardRuleForTest(t, "||example.org^\n||example.org^$badfilter\n")
	if _, blocked := p.Match("example.org"); blocked {
		t.Fatal("expected badfilter to remove deny rule")
	}

	p = buildAdguardRuleForTest(t, "||example.org^$denyallow=sub.example.org\n")
	if _, blocked := p.Match("www.example.org"); !blocked {
		t.Fatal("expected www.example.org to remain blocked")
	}
	if _, blocked := p.Match("sub.example.org"); blocked {
		t.Fatal("expected denyallow domain to bypass block")
	}
}

func buildAdguardRuleForTest(t *testing.T, raw string) *AdguardRule {
	t.Helper()

	result, err := rulesource.ParseAdguardBytes(rulesource.FormatRules, []byte(raw))
	if err != nil {
		t.Fatalf("ParseAdguardBytes: %v", err)
	}
	p := &AdguardRule{
		importantAllowMatcher: domain.NewDomainMixMatcher(),
		importantDenyMatcher:  domain.NewDomainMixMatcher(),
		allowMatcher:          domain.NewDomainMixMatcher(),
		denyMatcher:           domain.NewDomainMixMatcher(),
	}
	if err := mergeAdguardResult(
		result,
		p.importantAllowMatcher,
		p.importantDenyMatcher,
		p.allowMatcher,
		p.denyMatcher,
		&p.denyRules,
	); err != nil {
		t.Fatalf("mergeAdguardResult: %v", err)
	}
	return p
}
