package domain_memory_pool

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

type hotRuleCall struct {
	providerTag string
	rules       []string
}

type mockHotRuleConsumer struct {
	addCh     chan hotRuleCall
	replaceCh chan hotRuleCall
}

func newMockHotRuleConsumer() *mockHotRuleConsumer {
	return &mockHotRuleConsumer{
		addCh:     make(chan hotRuleCall, 8),
		replaceCh: make(chan hotRuleCall, 8),
	}
}

func (m *mockHotRuleConsumer) AddHotRules(providerTag string, rules []string) error {
	m.addCh <- hotRuleCall{providerTag: providerTag, rules: append([]string(nil), rules...)}
	return nil
}

func (m *mockHotRuleConsumer) ReplaceHotRules(providerTag string, rules []string) error {
	m.replaceCh <- hotRuleCall{providerTag: providerTag, rules: append([]string(nil), rules...)}
	return nil
}

func saveMemoryPoolPolicyForTest(t *testing.T, tag string, policy coremain.DomainPoolPolicy) {
	t.Helper()
	if err := coremain.SaveMemoryPoolPoliciesToCustomConfig(map[string]coremain.DomainPoolPolicy{
		tag: policy,
	}); err != nil {
		t.Fatalf("SaveMemoryPoolPoliciesToCustomConfig: %v", err)
	}
}

func waitHotRuleCall(t *testing.T, ch <-chan hotRuleCall) hotRuleCall {
	t.Helper()
	select {
	case call := <-ch:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hot rule call")
		return hotRuleCall{}
	}
}

func assertNoHotRuleCall(t *testing.T, ch <-chan hotRuleCall, wait time.Duration) {
	t.Helper()
	select {
	case call := <-ch:
		t.Fatalf("unexpected hot rule call: %+v", call)
	case <-time.After(wait):
	}
}

func TestMemoryPoolAggregatesEntriesByBareDomain(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	saveMemoryPoolPolicyForTest(t, "my_realiplist", coremain.DomainPoolPolicy{
		Kind:                   coremain.DomainPoolKindMemory,
		PublishTo:              "my_realiprule",
		RequeryTag:             "requery",
		PromoteAfter:           2,
		TrackQType:             true,
		TrackFlags:             true,
		MaxDomains:             100,
		MaxVariantsPerDomain:   4,
		EvictionPolicy:         "lru",
		StaleAfterMinutes:      360,
		RefreshCooldownMinutes: 120,
		FlushIntervalMS:        1000,
		PublishDebounceMS:      0,
		PruneIntervalSec:       60,
	})

	pool, err := newDomainMemoryPool("my_realiplist", nil, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}
	pool.processRecord(&logItem{name: "example.com.", qtype: 1, ad: true})
	pool.processRecord(&logItem{name: "example.com.", qtype: 28, cd: true})

	items, total, err := pool.MemoryEntries("", 0, 10)
	if err != nil {
		t.Fatalf("MemoryEntries: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("unexpected items: total=%d items=%+v", total, items)
	}
	if items[0].Domain != "example.com" || items[0].Count != 2 {
		t.Fatalf("unexpected aggregate entry: %+v", items[0])
	}
}

func TestMemoryPoolSaveAndReload(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	pool, err := newDomainMemoryPool("my_fakeiplist", nil, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}
	pool.processRecord(&logItem{name: "example.com.", qtype: 1})
	if err := pool.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite: %v", err)
	}

	loaded, err := newDomainMemoryPool("my_fakeiplist", nil, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool reload: %v", err)
	}
	if err := loaded.loadFromStore(); err != nil {
		t.Fatalf("loadFromStore: %v", err)
	}
	if loaded.shouldWrite(WriteModeShutdown) {
		t.Fatal("expected clean reloaded pool to skip shutdown write")
	}
	items, total, err := loaded.MemoryEntries("", 0, 10)
	if err != nil {
		t.Fatalf("MemoryEntries: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Domain != "example.com" {
		t.Fatalf("unexpected reloaded items: total=%d items=%+v", total, items)
	}
}

func TestMemoryPoolShutdownWriteWhenHotReplacePending(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	pool, err := newDomainMemoryPool("my_fakeiplist", nil, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}

	pool.hasRulesHash = true
	pool.hotNeedsReplace.Store(true)
	if !pool.shouldWrite(WriteModeShutdown) {
		t.Fatal("expected shutdown write when hot rule replacement is pending")
	}
}

func TestMemoryPoolPushesImmediateHotRuleOnPromotion(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	saveMemoryPoolPolicyForTest(t, "my_realiplist", coremain.DomainPoolPolicy{
		Kind:                 coremain.DomainPoolKindMemory,
		PublishTo:            "my_realiprule",
		PromoteAfter:         2,
		MaxDomains:           100,
		MaxVariantsPerDomain: 4,
		EvictionPolicy:       "lru",
		FlushIntervalMS:      1000,
		PruneIntervalSec:     60,
	})

	consumer := newMockHotRuleConsumer()
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"mapper": consumer})
	pool, err := newDomainMemoryPool("my_realiplist", m, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	pool.processRecord(&logItem{name: "example.com.", source: "live"})
	assertNoHotRuleCall(t, consumer.addCh, 100*time.Millisecond)

	pool.processRecord(&logItem{name: "example.com.", source: "live"})
	call := waitHotRuleCall(t, consumer.addCh)
	if call.providerTag != "my_realiprule" {
		t.Fatalf("unexpected publish_to: %+v", call)
	}
	if len(call.rules) != 1 || call.rules[0] != "full:example.com" {
		t.Fatalf("unexpected hot add rules: %+v", call)
	}

	rules, err := pool.SnapshotHotRules()
	if err != nil {
		t.Fatalf("SnapshotHotRules: %v", err)
	}
	if len(rules) != 1 || rules[0] != "full:example.com" {
		t.Fatalf("unexpected snapshot rules: %v", rules)
	}
}

func TestMemoryPoolFlushReplacesHotRules(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	saveMemoryPoolPolicyForTest(t, "my_realiplist", coremain.DomainPoolPolicy{
		Kind:                 coremain.DomainPoolKindMemory,
		PublishTo:            "my_realiprule",
		PromoteAfter:         1,
		MaxDomains:           100,
		MaxVariantsPerDomain: 4,
		EvictionPolicy:       "lru",
		FlushIntervalMS:      1000,
		PruneIntervalSec:     60,
	})

	consumer := newMockHotRuleConsumer()
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"mapper": consumer})
	pool, err := newDomainMemoryPool("my_realiplist", m, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	pool.processRecord(&logItem{name: "flush.example.", source: "live"})
	_ = waitHotRuleCall(t, consumer.addCh)

	if err := pool.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite save: %v", err)
	}
	saveCall := waitHotRuleCall(t, consumer.replaceCh)
	if len(saveCall.rules) != 1 || saveCall.rules[0] != "full:flush.example" {
		t.Fatalf("unexpected save replace rules: %+v", saveCall)
	}

	if err := pool.FlushRuntime(context.Background()); err != nil {
		t.Fatalf("FlushRuntime: %v", err)
	}
	flushCall := waitHotRuleCall(t, consumer.replaceCh)
	if len(flushCall.rules) != 0 {
		t.Fatalf("expected empty replace after flush, got %+v", flushCall)
	}
}

func TestMemoryPoolHotRulesRespectPublishDebounce(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	saveMemoryPoolPolicyForTest(t, "my_realiplist", coremain.DomainPoolPolicy{
		Kind:                 coremain.DomainPoolKindMemory,
		PublishTo:            "my_realiprule",
		PromoteAfter:         1,
		MaxDomains:           100,
		MaxVariantsPerDomain: 4,
		EvictionPolicy:       "lru",
		FlushIntervalMS:      1000,
		PublishDebounceMS:    150,
		PruneIntervalSec:     60,
	})

	consumer := newMockHotRuleConsumer()
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"mapper": consumer})
	pool, err := newDomainMemoryPool("my_realiplist", m, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	pool.processRecord(&logItem{name: "debounce.example.", source: "live"})
	assertNoHotRuleCall(t, consumer.addCh, 80*time.Millisecond)

	rules, err := pool.SnapshotHotRules()
	if err != nil {
		t.Fatalf("SnapshotHotRules before debounce: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no applied hot rules before debounce, got %v", rules)
	}

	call := waitHotRuleCall(t, consumer.addCh)
	if len(call.rules) != 1 || call.rules[0] != "full:debounce.example" {
		t.Fatalf("unexpected debounced add: %+v", call)
	}

	rules, err = pool.SnapshotHotRules()
	if err != nil {
		t.Fatalf("SnapshotHotRules after debounce: %v", err)
	}
	if len(rules) != 1 || rules[0] != "full:debounce.example" {
		t.Fatalf("unexpected applied hot rules after debounce: %v", rules)
	}
}

func TestMemoryPoolSaveCancelsPendingHotAdd(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	saveMemoryPoolPolicyForTest(t, "my_realiplist", coremain.DomainPoolPolicy{
		Kind:                 coremain.DomainPoolKindMemory,
		PublishTo:            "my_realiprule",
		PromoteAfter:         1,
		MaxDomains:           100,
		MaxVariantsPerDomain: 4,
		EvictionPolicy:       "lru",
		FlushIntervalMS:      1000,
		PublishDebounceMS:    200,
		PruneIntervalSec:     60,
	})

	consumer := newMockHotRuleConsumer()
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"mapper": consumer})
	pool, err := newDomainMemoryPool("my_realiplist", m, nil)
	if err != nil {
		t.Fatalf("newDomainMemoryPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	pool.processRecord(&logItem{name: "save.example.", source: "live"})
	assertNoHotRuleCall(t, consumer.addCh, 60*time.Millisecond)

	if err := pool.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite save: %v", err)
	}
	replaceCall := waitHotRuleCall(t, consumer.replaceCh)
	if len(replaceCall.rules) != 1 || replaceCall.rules[0] != "full:save.example" {
		t.Fatalf("unexpected replace rules: %+v", replaceCall)
	}

	assertNoHotRuleCall(t, consumer.addCh, 260*time.Millisecond)
}
