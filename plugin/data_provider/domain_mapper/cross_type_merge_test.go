package domain_mapper

import (
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

type crossTypeRuleExporter struct {
	rules []string
}

func (m *crossTypeRuleExporter) GetRules() ([]string, error) {
	return append([]string(nil), m.rules...), nil
}

func (m *crossTypeRuleExporter) Subscribe(func()) {}

func (m *crossTypeRuleExporter) AllowHotRule(string, time.Time) bool {
	return true
}

func TestDomainMapperFastMatchMergesKeywordAndFullRules(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"geosite_cn": &crossTypeRuleExporter{rules: []string{"full:www.foo.example"}},
		"cusnocn":    &crossTypeRuleExporter{rules: []string{"keyword:foo.example"}},
	})
	dmAny, err := NewMapper(coremain.NewBP("unified_matcher1", m), &Args{
		Rules: []RuleConfig{
			{Tag: "cusnocn", Mark: 15, OutputTag: "订阅代理补充"},
			{Tag: "geosite_cn", Mark: 16, OutputTag: "订阅直连"},
		},
	})
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}

	marks, tags, ok := dmAny.(*DomainMapper).FastMatch("www.foo.example.")
	if !ok {
		t.Fatal("expected combined match")
	}
	if len(marks) != 2 || marks[0] != 15 || marks[1] != 16 {
		t.Fatalf("unexpected combined marks: %v", marks)
	}
	if tags != "订阅代理补充|订阅直连" && tags != "订阅直连|订阅代理补充" {
		t.Fatalf("unexpected combined tags: %q", tags)
	}
}

func TestDomainMapperFastMatchMergesRegexpAndDomainRules(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"geosite_cn": &crossTypeRuleExporter{rules: []string{"domain:foo.example"}},
		"cusnocn":    &crossTypeRuleExporter{rules: []string{"regexp:^www\\..*\\.example$"}},
	})
	dmAny, err := NewMapper(coremain.NewBP("unified_matcher1", m), &Args{
		Rules: []RuleConfig{
			{Tag: "cusnocn", Mark: 15, OutputTag: "订阅代理补充"},
			{Tag: "geosite_cn", Mark: 16, OutputTag: "订阅直连"},
		},
	})
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}

	marks, tags, ok := dmAny.(*DomainMapper).FastMatch("www.foo.example.")
	if !ok {
		t.Fatal("expected combined match")
	}
	if len(marks) != 2 || marks[0] != 15 || marks[1] != 16 {
		t.Fatalf("unexpected combined marks: %v", marks)
	}
	if tags != "订阅代理补充|订阅直连" && tags != "订阅直连|订阅代理补充" {
		t.Fatalf("unexpected combined tags: %q", tags)
	}
}

func TestDomainMapperFastMatchStaticHitWithoutHotRules(t *testing.T) {
	dm := newTestMapper(17, "未命中", &MatchResult{
		Marks:      []uint8{11},
		JoinedTags: "订阅直连",
	}, "Example.Org.")

	marks, tags, ok := dm.FastMatch("example.org.")
	if !ok {
		t.Fatal("expected static match")
	}
	if len(marks) != 1 || marks[0] != 11 {
		t.Fatalf("unexpected marks: %v", marks)
	}
	if tags != "订阅直连" {
		t.Fatalf("unexpected tags: %q", tags)
	}
}
