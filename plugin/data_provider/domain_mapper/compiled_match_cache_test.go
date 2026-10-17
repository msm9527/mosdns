package domain_mapper

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

func TestCompiledMatchCacheCombinesRuleTypesAndCachesMisses(t *testing.T) {
	m := newCompiledMatcherSet()
	for i, rule := range []string{"full:prefix.wild.example", "domain:wild.example", "regexp:^prefix[.]wild[.]example$", "keyword:wild"} {
		mark := uint8(50 + i)
		if err := m.Add(rule, &compiledMatch{staticResult: &MatchResult{Marks: []uint8{mark}, JoinedTags: fmt.Sprint(mark)}}); err != nil {
			t.Fatal(err)
		}
	}
	first, ok := m.Match("prefix.wild.example.")
	if !ok || !reflect.DeepEqual(first.staticResult.Marks, []uint8{50, 51, 52, 53}) {
		t.Fatalf("combined evidence missing: %+v %v", first, ok)
	}
	if again, ok := m.Match("prefix.wild.example."); !ok || again != first {
		t.Fatal("repeated rule evidence was recomputed")
	}
	if _, ok := m.Match("absent.example."); ok {
		t.Fatal("unexpected miss match")
	}
	if evidence, found := m.matchCache().Get("absent.example."); !found || evidence != nil {
		t.Fatal("negative evidence was not cached")
	}
	if err := m.Add("full:absent.example", first); err != nil {
		t.Fatal(err)
	}
	if evidence, ok := m.Match("absent.example."); !ok || evidence != first {
		t.Fatal("adding an unpublished rule retained negative evidence")
	}
}

func TestCompiledMatchCacheIsBoundedAndConcurrent(t *testing.T) {
	m := newCompiledMatcherSet()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < compiledMatchCacheSize/4; i++ {
				if _, ok := m.Match(fmt.Sprintf("absent-%d-%d.example.", worker, i)); ok {
					t.Error("unexpected match")
				}
			}
		}(worker)
	}
	wg.Wait()
	if size := m.matchCache().Len(); size == 0 || size > compiledMatchCacheSize {
		t.Fatalf("cache size=%d limit=%d", size, compiledMatchCacheSize)
	}
}

func TestCompiledMatchCacheDoesNotOutliveRebuildOrHideHotRules(t *testing.T) {
	provider := &mockRuleExporter{rules: []string{"regexp:^old[.]example$"}}
	dm := mapperWithSourceProvider(t, provider)
	if _, _, ok := dm.FastMatch("old.example."); !ok {
		t.Fatal("initial rule did not match")
	}
	if _, _, ok := dm.FastMatch("new.example."); ok {
		t.Fatal("unexpected initial match")
	}
	previous := dm.loadMatcher()
	provider.rules = []string{"regexp:^new[.]example$"}
	dm.rebuild()
	if dm.loadMatcher() == previous {
		t.Fatal("rebuild reused the old matcher snapshot")
	}
	if _, _, ok := dm.FastMatch("old.example."); ok {
		t.Fatal("rebuild retained old positive evidence")
	}
	if _, _, ok := dm.FastMatch("new.example."); !ok {
		t.Fatal("rebuild retained old negative evidence")
	}
	if _, _, ok := dm.FastMatch("hot.example."); ok {
		t.Fatal("unexpected hot match")
	}
	if err := dm.AddHotRules("memory", []string{"full:hot.example"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := dm.FastMatch("hot.example."); !ok {
		t.Fatal("cached miss hid new hot evidence")
	}
	if err := dm.ReplaceHotRules("memory", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := dm.FastMatch("hot.example."); ok {
		t.Fatal("removed hot evidence was cached")
	}
}

func TestCompiledMatchCacheRetainsDynamicValidation(t *testing.T) {
	provider := &sourceValidationSpy{mockRuleExporter: &mockRuleExporter{rules: []string{"regexp:^stale[.]example$"}}, expires: time.Now().Add(time.Hour)}
	dm := mapperWithSourceProvider(t, provider)
	if _, _, ok := dm.FastMatch("stale.example."); !ok {
		t.Fatal("initial dynamic rule did not match")
	}
	provider.expires = time.Now().Add(-time.Hour)
	if _, _, ok := dm.FastMatchWithSource("stale.example.", server.RequestSourceRefresh); ok {
		t.Fatal("cached evidence bypassed dynamic expiry")
	}
	if len(provider.calls) != 2 || provider.notifications != 0 {
		t.Fatalf("calls=%v notifications=%d", provider.calls, provider.notifications)
	}
}
