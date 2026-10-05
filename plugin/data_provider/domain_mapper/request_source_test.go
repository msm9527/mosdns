package domain_mapper

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
)

type sourceValidationSpy struct {
	*mockRuleExporter
	expires       time.Time
	calls         []server.RequestSource
	notifications int
	dirty         bool
}

func (s *sourceValidationSpy) HasRuntimeHotRuleValidation() bool { return true }
func (s *sourceValidationSpy) AllowHotRule(string, time.Time) bool {
	panic("source-aware provider must not use the legacy validator")
}
func (s *sourceValidationSpy) AllowHotRuleWithSource(domain string, now time.Time, source server.RequestSource) bool {
	if domain != "stale.example" {
		panic("unexpected validation domain")
	}
	s.calls = append(s.calls, source)
	if now.Before(s.expires) {
		return true
	}
	if !source.IsBackground() {
		s.notifications++
		s.dirty = true
	}
	return false
}

func mapperWithSourceProvider(t *testing.T, provider data_provider.RuleExporter) *DomainMapper {
	t.Helper()
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"memory": provider})
	v, err := NewMapper(coremain.NewBP("mapper", m), &Args{Rules: []RuleConfig{{Tag: "memory", Mark: 11, OutputTag: "memory"}}})
	if err != nil {
		t.Fatal(err)
	}
	return v.(*DomainMapper)
}

func TestRequestSourceValidatorAcrossMapperEntryPoints(t *testing.T) {
	for _, location := range []string{"compiled", "hot"} {
		for _, entry := range []string{"fast", "legacy_fast", "exec", "fast_exec"} {
			for _, source := range []server.RequestSource{server.RequestSourceUnspecified, server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
				if entry == "legacy_fast" && source != server.RequestSourceUser {
					continue
				}
				for _, stale := range []bool{false, true} {
					t.Run(location+"/"+entry+"/"+source.String()+"/"+map[bool]string{true: "stale", false: "valid"}[stale], func(t *testing.T) {
						exporter := &mockRuleExporter{}
						if location == "compiled" {
							exporter.rules = []string{"full:stale.example"}
						} else {
							exporter.hotRules = []string{"full:stale.example"}
						}
						spy := &sourceValidationSpy{mockRuleExporter: exporter, expires: time.Now().Add(time.Hour)}
						if stale {
							spy.expires = time.Now().Add(-time.Hour)
						}
						dm := mapperWithSourceProvider(t, spy)
						matched := false
						switch entry {
						case "fast":
							_, _, matched = dm.FastMatchWithSource("stale.example.", source)
						case "legacy_fast":
							_, _, matched = dm.FastMatch("stale.example.")
						default:
							qCtx := newTestQueryContext("stale.example.")
							qCtx.ServerMeta.RequestSource = source
							execute := dm.Exec
							if entry == "fast_exec" {
								execute = dm.GetFastExec()
							}
							if err := execute(context.Background(), qCtx); err != nil {
								t.Fatal(err)
							}
							matched = qCtx.HasFastFlag(11)
							if !stale {
								value, ok := qCtx.GetValue(query_context.KeyDomainSet)
								if !ok || value != "memory" {
									t.Fatalf("unexpected tag %v", value)
								}
							}
						}
						if matched == stale {
							t.Fatalf("matched %v for stale %v", matched, stale)
						}
						wantSource := source
						if source == server.RequestSourceUnspecified {
							wantSource = server.RequestSourceUser
						}
						if len(spy.calls) != 1 || spy.calls[0] != wantSource {
							t.Fatalf("validation sources %v, want %v", spy.calls, wantSource)
						}
						wantNotify := 0
						if stale && !source.IsBackground() {
							wantNotify = 1
						}
						if spy.notifications != wantNotify || spy.dirty != (wantNotify > 0) {
							t.Fatalf("notifications %d dirty %v, want %d", spy.notifications, spy.dirty, wantNotify)
						}
					})
				}
			}
		}
	}
}

func TestLegacyDynamicProviderRejectsBackgroundWithoutSideEffects(t *testing.T) {
	for _, location := range []string{"compiled", "hot"} {
		t.Run(location, func(t *testing.T) {
			calls := 0
			provider := &mockRuleExporter{allowHotRule: func(string, time.Time) bool { calls++; return true }}
			if location == "compiled" {
				provider.rules = []string{"full:stale.example"}
			} else {
				provider.hotRules = []string{"full:stale.example"}
			}
			dm := mapperWithSourceProvider(t, provider)
			for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
				if _, _, ok := dm.FastMatchWithSource("stale.example.", source); ok {
					t.Fatal("legacy dynamic provider matched background")
				}
			}
			if calls != 0 {
				t.Fatalf("background called legacy validator %d times", calls)
			}
			if _, _, ok := dm.FastMatch("stale.example."); !ok || calls != 1 {
				t.Fatalf("legacy user compatibility lost, calls %d", calls)
			}
		})
	}
}

func TestBackgroundStaticRulesRetainCompiledMatching(t *testing.T) {
	provider := &mockRuleExporter{rules: []string{"domain:example.org", "full:exact.example", "keyword:needle", "regexp:^regex[.]example$"}, hotRules: []string{"full:hot.example"}}
	dm := mapperWithSourceProvider(t, provider)
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		for _, domain := range []string{"sub.example.org.", "exact.example.", "contains-needle.example.", "regex.example.", "hot.example."} {
			if marks, tags, ok := dm.FastMatchWithSource(domain, source); !ok || len(marks) != 1 || marks[0] != 11 || tags != "memory" {
				t.Fatalf("%s %s yielded %v %s %v", source, domain, marks, tags, ok)
			}
		}
	}
}

func TestBackgroundDynamicRejectionPreservesStaticMatch(t *testing.T) {
	spy := &sourceValidationSpy{mockRuleExporter: &mockRuleExporter{rules: []string{"full:stale.example"}}, expires: time.Now().Add(-time.Hour)}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"memory": spy,
		"static": &mockRuleExporter{rules: []string{"domain:example"}},
	})
	v, err := NewMapper(coremain.NewBP("mapper", m), &Args{Rules: []RuleConfig{{Tag: "memory", Mark: 11}, {Tag: "static", Mark: 8}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		marks, tags, ok := v.(*DomainMapper).FastMatchWithSource("stale.example.", source)
		if !ok || len(marks) != 1 || marks[0] != 8 || tags != "static" {
			t.Fatalf("static result lost %v %s %v", marks, tags, ok)
		}
	}
	if spy.notifications != 0 || spy.dirty {
		t.Fatal("background mixed match created user demand")
	}
}
