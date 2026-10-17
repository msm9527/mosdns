package domain_mapper

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

type gatedRuntimeExporter struct {
	calls   [4]atomic.Uint64
	invalid atomic.Bool
}

func (*gatedRuntimeExporter) GetRules() ([]string, error) {
	return []string{"regexp:^stale[.]example$"}, nil
}
func (*gatedRuntimeExporter) Subscribe(func()) {}
func (*gatedRuntimeExporter) SnapshotHotRules() ([]string, error) {
	return []string{"full:stale.example"}, nil
}
func (*gatedRuntimeExporter) AllowHotRule(string, time.Time) bool {
	panic("source-aware provider called through the legacy interface")
}
func (p *gatedRuntimeExporter) AllowHotRuleWithSource(_ string, _ time.Time, source server.RequestSource) bool {
	p.calls[source].Add(1)
	return source == server.RequestSourceUser && !p.invalid.Load()
}

func TestMatcherEvidenceCacheIsIndependentOfRequestSource(t *testing.T) {
	provider := new(gatedRuntimeExporter)
	dm := mapperWithSourceProvider(t, provider)
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceUser, server.RequestSourceRefresh, server.RequestSourceUser} {
		before := provider.calls[source].Load()
		marks, tags, matched := dm.FastMatchWithSource("stale.example.", source)
		if matched != (source == server.RequestSourceUser) || provider.calls[source].Load() != before+1 {
			t.Fatalf("source=%s matched=%v calls=%d", source, matched, provider.calls[source].Load()-before)
		}
		if matched && (!reflect.DeepEqual(marks, []uint8{11}) || tags != "memory") {
			t.Fatalf("source=%s marks=%v tags=%q", source, marks, tags)
		}
	}
	provider.invalid.Store(true)
	if _, _, ok := dm.FastMatch("stale.example."); ok {
		t.Fatal("cached evidence bypassed an invalidated provider")
	}
	provider.invalid.Store(false)
	if _, _, ok := dm.FastMatch("stale.example."); !ok {
		t.Fatal("previous invalid verdict prevented a new valid match")
	}
}

func TestMatcherCacheConcurrentRebuildAndHotPublication(t *testing.T) {
	provider := new(gatedRuntimeExporter)
	dm := mapperWithSourceProvider(t, provider)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			source := server.RequestSourceUser
			if worker%2 != 0 {
				source = server.RequestSourceRefresh
			}
			for i := 0; i < 200; i++ {
				if _, _, ok := dm.FastMatchWithSource("stale.example.", source); ok != (source == server.RequestSourceUser) {
					t.Errorf("source=%s returned %v during rebuild", source, ok)
					return
				}
			}
		}(worker)
	}
	for i := 0; i < 25; i++ {
		if err := dm.ReplaceHotRules("memory", nil); err != nil {
			t.Fatal(err)
		}
		dm.rebuild()
		if err := dm.AddHotRules("memory", []string{"full:stale.example"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourceRefresh} {
		if got := provider.calls[source].Load(); got != 800 {
			t.Errorf("source=%s validation count=%d want=800", source, got)
		}
	}
}

func TestCompiledMatchCachePreservesDNSCaseAndRootMatching(t *testing.T) {
	m := newCompiledMatcherSet()
	for _, rule := range []string{"full:.", "domain:example"} {
		if err := m.Add(rule, &compiledMatch{staticResult: &MatchResult{Marks: []uint8{11}, JoinedTags: "static"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".", "UPPER.Example.", "upper.example.", "UPPER.EXAMPLE", "."} {
		for repeat := 0; repeat < 2; repeat++ {
			if result, ok := m.Match(name); !ok || !reflect.DeepEqual(result.staticResult.Marks, []uint8{11}) {
				t.Errorf("query %q did not match before/after warming cache", name)
			}
		}
	}
}
