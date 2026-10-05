package domain_set_light

import (
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_mapper"
)

type legacyValidationSpy struct {
	*mockRuleExporter
	calls int
}

func (s *legacyValidationSpy) AllowHotRule(string, time.Time) bool { s.calls++; return true }

type sourceValidationSpy struct {
	*legacyValidationSpy
	calls         []server.RequestSource
	notifications int
	dirty         bool
}

func (s *sourceValidationSpy) AllowHotRuleWithSource(domain string, now time.Time, source server.RequestSource) bool {
	if domain != "stale.example" || now.IsZero() {
		panic("validation inputs were lost")
	}
	s.calls = append(s.calls, source)
	if !source.IsBackground() {
		s.notifications++
		s.dirty = true
	}
	return false
}

func TestGeneratedValidatorPreservesRequestSource(t *testing.T) {
	for _, source := range []server.RequestSource{server.RequestSourceUnspecified, server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		t.Run(source.String(), func(t *testing.T) {
			spy := &sourceValidationSpy{legacyValidationSpy: &legacyValidationSpy{mockRuleExporter: &mockRuleExporter{}}}
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{"pool": spy})
			ds := &DomainSetLight{bp: coremain.NewBP("rules", m), generatedFrom: "pool"}
			if ds.AllowHotRuleWithSource("stale.example", time.Now(), source) {
				t.Fatal("stale rule accepted")
			}
			expected := source
			if expected == server.RequestSourceUnspecified {
				expected = server.RequestSourceUser
			}
			if len(spy.calls) != 1 || spy.calls[0] != expected || spy.legacyValidationSpy.calls != 0 {
				t.Fatalf("source calls %v legacy calls %d", spy.calls, spy.legacyValidationSpy.calls)
			}
			wantNotify := 0
			if !source.IsBackground() {
				wantNotify = 1
			}
			if spy.notifications != wantNotify || spy.dirty != (wantNotify > 0) {
				t.Fatalf("notifications %d dirty %v", spy.notifications, spy.dirty)
			}
		})
	}
}

func TestGeneratedLegacyValidatorRejectsBackgroundWithoutCallingIt(t *testing.T) {
	spy := &legacyValidationSpy{mockRuleExporter: &mockRuleExporter{}}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"pool": spy})
	ds := &DomainSetLight{bp: coremain.NewBP("rules", m), generatedFrom: "pool"}
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		if ds.AllowHotRuleWithSource("stale.example", time.Now(), source) {
			t.Fatal("legacy background validation accepted")
		}
	}
	if spy.calls != 0 {
		t.Fatalf("background called legacy validator %d times", spy.calls)
	}
	if !ds.AllowHotRule("stale.example", time.Now()) || spy.calls != 1 {
		t.Fatal("legacy user validation lost")
	}
}

func TestStaticGeneratedProviderAllowsBackgroundValidation(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"static": &mockRuleExporter{}})
	for _, ds := range []*DomainSetLight{{}, {bp: coremain.NewBP("rules", m), generatedFrom: "static"}} {
		for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
			if !ds.AllowHotRuleWithSource("static.example", time.Now(), source) {
				t.Fatalf("static validation rejected %s", source)
			}
		}
	}
}

func TestMapperUsesGeneratedSourceValidation(t *testing.T) {
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		t.Run(source.String(), func(t *testing.T) {
			spy := &sourceValidationSpy{legacyValidationSpy: &legacyValidationSpy{mockRuleExporter: &mockRuleExporter{}}}
			ds := &DomainSetLight{generatedFrom: "pool", rules: []string{"full:stale.example"}}
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{"pool": spy, "rules": ds})
			ds.bp = coremain.NewBP("rules", m)
			v, err := domain_mapper.NewMapper(coremain.NewBP("mapper", m), &domain_mapper.Args{Rules: []domain_mapper.RuleConfig{{Tag: "rules", Mark: 11}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, ok := v.(*domain_mapper.DomainMapper).FastMatchWithSource("stale.example.", source); ok {
				t.Fatal("generated stale rule accepted")
			}
			if len(spy.calls) != 1 || spy.calls[0] != source {
				t.Fatalf("source not passed through mapper and generated rules %v", spy.calls)
			}
			if source.IsBackground() && (spy.notifications != 0 || spy.dirty) {
				t.Fatal("generated background match created demand")
			}
		})
	}
}
