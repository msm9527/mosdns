package qname

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_set"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

type runtimeExporter struct {
	valid      bool
	calls      int
	demand     int
	lastSource server.RequestSource
	lastDomain string
}

func (*runtimeExporter) GetRules() ([]string, error) {
	return []string{"full:node.example"}, nil
}
func (*runtimeExporter) Subscribe(func()) {}
func (p *runtimeExporter) AllowHotRuleWithSource(name string, now time.Time, source server.RequestSource) bool {
	if now.IsZero() {
		panic("missing validation time")
	}
	p.calls++
	p.lastSource, p.lastDomain = source, name
	if !source.IsBackground() && !p.valid {
		p.demand++
	}
	return p.valid && source != server.RequestSourceRefresh
}

type staticExporter struct{}

func (staticExporter) GetRules() ([]string, error) { return []string{"full:node.example"}, nil }
func (staticExporter) Subscribe(func())            {}

type legacyRuntimeExporter struct {
	staticExporter
	calls int
}

func (p *legacyRuntimeExporter) AllowHotRule(string, time.Time) bool { p.calls++; return true }

func setupRuntimeMatcher(t testing.TB, args *domain_set.Args, expression string, pool any) (*fastQnameMatcher, *domain_set.DomainSet) {
	t.Helper()
	plugins := map[string]any{"pool": pool}
	manager := coremain.NewTestMosdnsWithPlugins(plugins)
	provider, err := domain_set.Init(coremain.NewBP("generated", manager), args)
	if err != nil {
		t.Fatal(err)
	}
	plugins["generated"] = provider
	matcher, err := QuickSetup(sequence.NewBQ(func(tag string) any { return plugins[tag] }, nil, zap.NewNop()), expression)
	if err != nil {
		t.Fatal(err)
	}
	return matcher.(*fastQnameMatcher), provider.(*domain_set.DomainSet)
}

func runtimeQuery(source server.RequestSource) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion("NoDe.Example.", dns.TypeA)
	ctx := query_context.NewContext(q)
	ctx.ServerMeta.RequestSource = source
	return ctx
}

func checkRuntimeMatcher(t *testing.T, matcher *fastQnameMatcher, source server.RequestSource, want bool) {
	t.Helper()
	q := runtimeQuery(source)
	got, err := matcher.Match(context.Background(), q)
	if err != nil || got != want {
		t.Fatalf("normal source=%s matched=%v want=%v err=%v", source, got, want, err)
	}
	if got := matcher.GetFastCheck()(q); got != want {
		t.Fatalf("fast source=%s matched=%v want=%v", source, got, want)
	}
}

func TestGeneratedQNameChecksCurrentValidityAndSource(t *testing.T) {
	for _, source := range []server.RequestSource{server.RequestSourceUnspecified, server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		t.Run(source.String(), func(t *testing.T) {
			pool := &runtimeExporter{valid: true}
			matcher, _ := setupRuntimeMatcher(t, &domain_set.Args{GeneratedFrom: "pool"}, "$generated", pool)
			checkRuntimeMatcher(t, matcher, source, source != server.RequestSourceRefresh)
			// The exported domain remains present. Changing runtime validity must
			// take effect immediately, without waiting for a rule reload.
			pool.valid = false
			checkRuntimeMatcher(t, matcher, source, false)
			wantSource := source
			if wantSource == server.RequestSourceUnspecified {
				wantSource = server.RequestSourceUser
			}
			if pool.calls != 4 || pool.lastSource != wantSource || pool.lastDomain != "node.example" {
				t.Fatalf("validation calls=%d source=%s domain=%q", pool.calls, pool.lastSource, pool.lastDomain)
			}
			wantDemand := 0
			if !source.IsBackground() {
				wantDemand = 2
			}
			if pool.demand != wantDemand {
				t.Fatalf("demand=%d want=%d", pool.demand, wantDemand)
			}
		})
	}
}

func TestGeneratedQNameKeepsStaticAlternatives(t *testing.T) {
	file := filepath.Join(t.TempDir(), "static.txt")
	if err := os.WriteFile(file, []byte("full:node.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args *domain_set.Args
		expr string
	}{
		{"expression_in_generated_set", &domain_set.Args{GeneratedFrom: "pool", Exps: []string{"full:node.example"}}, "$generated"},
		{"file_in_generated_set", &domain_set.Args{GeneratedFrom: "pool", Files: []string{file}}, "$generated"},
		{"anonymous_expression", &domain_set.Args{GeneratedFrom: "pool"}, "$generated full:node.example"},
		{"anonymous_file", &domain_set.Args{GeneratedFrom: "pool"}, "$generated &" + file},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matcher, _ := setupRuntimeMatcher(t, tc.args, tc.expr, &runtimeExporter{})
			for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
				checkRuntimeMatcher(t, matcher, source, true)
			}
		})
	}
}

func TestGeneratedQNameNestedAndMultipleProviders(t *testing.T) {
	pool := &runtimeExporter{}
	plugins := map[string]any{"pool": pool}
	manager := coremain.NewTestMosdnsWithPlugins(plugins)
	addSet := func(tag string, args *domain_set.Args) {
		t.Helper()
		set, err := domain_set.Init(coremain.NewBP(tag, manager), args)
		if err != nil {
			t.Fatal(err)
		}
		plugins[tag] = set
	}
	addSet("dynamic", &domain_set.Args{GeneratedFrom: "pool"})
	addSet("static", &domain_set.Args{Exps: []string{"full:node.example"}})
	addSet("nested_dynamic", &domain_set.Args{Sets: []string{"dynamic"}})
	addSet("nested_mixed", &domain_set.Args{Sets: []string{"dynamic", "static"}})
	for _, expr := range []string{"$dynamic $static", "$nested_mixed", "$nested_dynamic"} {
		matcher, err := QuickSetup(sequence.NewBQ(func(tag string) any { return plugins[tag] }, nil, zap.NewNop()), expr)
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
			checkRuntimeMatcher(t, matcher.(*fastQnameMatcher), source, expr != "$nested_dynamic")
		}
	}
}

func TestStaticGeneratedQNameStillMatchesBackground(t *testing.T) {
	matcher, _ := setupRuntimeMatcher(t, &domain_set.Args{GeneratedFrom: "pool"}, "$generated", staticExporter{})
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		checkRuntimeMatcher(t, matcher, source, true)
	}
}

func TestGeneratedQNameSkipsLegacyValidatorForBackground(t *testing.T) {
	pool := &legacyRuntimeExporter{}
	matcher, _ := setupRuntimeMatcher(t, &domain_set.Args{GeneratedFrom: "pool"}, "$generated", pool)
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		checkRuntimeMatcher(t, matcher, source, false)
	}
	if pool.calls != 0 {
		t.Fatal("background matching invoked a validator without source support")
	}
	checkRuntimeMatcher(t, matcher, server.RequestSourceUser, true)
	if pool.calls != 2 {
		t.Fatalf("user validation calls=%d", pool.calls)
	}
}

func TestGeneratedQNamePreservesStaticRulesOfGeneratedProvider(t *testing.T) {
	pool := &runtimeExporter{}
	plugins := map[string]any{"pool": pool}
	manager := coremain.NewTestMosdnsWithPlugins(plugins)
	inner, err := domain_set.Init(coremain.NewBP("inner", manager), &domain_set.Args{GeneratedFrom: "pool", Exps: []string{"full:node.example"}})
	if err != nil {
		t.Fatal(err)
	}
	plugins["inner"] = inner
	outer, err := domain_set.Init(coremain.NewBP("outer", manager), &domain_set.Args{GeneratedFrom: "inner"})
	if err != nil {
		t.Fatal(err)
	}
	plugins["outer"] = outer
	matcher, err := QuickSetup(sequence.NewBQ(func(tag string) any { return plugins[tag] }, nil, zap.NewNop()), "$outer")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		checkRuntimeMatcher(t, matcher.(*fastQnameMatcher), source, true)
	}
}

func TestGeneratedQNameReloadPreservesStaticAndRuntimeRules(t *testing.T) {
	pool := &runtimeExporter{}
	matcher, set := setupRuntimeMatcher(t, &domain_set.Args{GeneratedFrom: "pool", Exps: []string{"full:static.example"}}, "$generated", pool)
	if err := set.ReloadControlConfig(&coremain.GlobalOverrides{}, nil); err != nil {
		t.Fatal(err)
	}
	checkRuntimeMatcher(t, matcher, server.RequestSourceUser, false)
	q := runtimeQuery(server.RequestSourceRefresh)
	q.Q().Question[0].Name = "static.example."
	if matched, err := matcher.Match(context.Background(), q); err != nil || !matched {
		t.Fatalf("static rule lost on reload matched=%v err=%v", matched, err)
	}
	pool.valid = true
	checkRuntimeMatcher(t, matcher, server.RequestSourceUser, true)
}

func TestQNameStaticAndValidGeneratedDoNotAllocate(t *testing.T) {
	for _, args := range []*domain_set.Args{{Exps: []string{"full:node.example"}}, {GeneratedFrom: "pool"}} {
		matcher, _ := setupRuntimeMatcher(t, args, "$generated", &runtimeExporter{valid: true})
		q := runtimeQuery(server.RequestSourceUser)
		q.Q().Question[0].Name = "node.example."
		fast := matcher.GetFastCheck()
		allocations := testing.AllocsPerRun(100, func() {
			if matched, _ := matcher.Match(context.Background(), q); !matched || !fast(q) {
				t.Fatal("expected a valid domain match")
			}
		})
		if allocations != 0 {
			t.Fatalf("qname match allocated %g objects", allocations)
		}
	}
}

func BenchmarkQNameRuntimeValidation(b *testing.B) {
	for _, generated := range []bool{false, true} {
		args := &domain_set.Args{Exps: []string{"full:node.example"}}
		name := "static"
		if generated {
			name = "generated"
			args = &domain_set.Args{GeneratedFrom: "pool"}
		}
		matcher, _ := setupRuntimeMatcher(b, args, "$generated", &runtimeExporter{valid: true})
		q := runtimeQuery(server.RequestSourceUser)
		q.Q().Question[0].Name = "node.example."
		for _, validated := range []bool{false, true} {
			entry := name + "/raw"
			match := matcher.Matcher.Match
			if validated {
				entry, match = name+"/validated", matcher.Match
			}
			b.Run(entry, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if ok, _ := match(context.Background(), q); !ok {
						b.Fatal("domain did not match")
					}
				}
			})
		}
	}
}
