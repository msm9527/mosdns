package domain_stats_pool

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

func TestStatsPoolAggregatesEntriesByBareDomain(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	if err := coremain.SaveMemoryPoolPoliciesToCustomConfig(map[string]coremain.DomainPoolPolicy{
		"top_domains": {
			Kind:                 coremain.DomainPoolKindStats,
			TrackQType:           false,
			TrackFlags:           true,
			MaxDomains:           100,
			MaxVariantsPerDomain: 4,
			EvictionPolicy:       "lru",
			FlushIntervalMS:      1000,
			PublishDebounceMS:    0,
			PruneIntervalSec:     60,
		},
	}); err != nil {
		t.Fatalf("SaveMemoryPoolPoliciesToCustomConfig: %v", err)
	}

	pool, err := newDomainStatsPool("top_domains", nil, nil)
	if err != nil {
		t.Fatalf("newDomainStatsPool: %v", err)
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

func TestStatsPoolSaveAndReload(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	pool, err := newDomainStatsPool("top_domains", nil, nil)
	if err != nil {
		t.Fatalf("newDomainStatsPool: %v", err)
	}
	pool.processRecord(&logItem{name: "example.com.", qtype: 1})
	if err := pool.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite: %v", err)
	}

	loaded, err := newDomainStatsPool("top_domains", nil, nil)
	if err != nil {
		t.Fatalf("newDomainStatsPool reload: %v", err)
	}
	if err := loaded.loadFromStore(); err != nil {
		t.Fatalf("loadFromStore: %v", err)
	}
	if loaded.shouldWrite(WriteModeShutdown) {
		t.Fatal("expected clean reloaded stats pool to skip shutdown write")
	}
	items, total, err := loaded.MemoryEntries("", 0, 10)
	if err != nil {
		t.Fatalf("MemoryEntries: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Domain != "example.com" {
		t.Fatalf("unexpected reloaded items: total=%d items=%+v", total, items)
	}
}

func TestStatsPoolPruneCompactsSparseState(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	pool, err := newDomainStatsPool("top_domains", nil, nil)
	if err != nil {
		t.Fatalf("newDomainStatsPool: %v", err)
	}

	expiredDate := time.Now().AddDate(0, 0, -120).Format("2006-01-02")
	freshDate := time.Now().UTC().Format("2006-01-02")

	pool.mu.Lock()
	for i := 0; i < stateCompactionMinEntries; i++ {
		domain := fmt.Sprintf("expired-%d.example", i)
		pool.stats[domain] = &statEntry{LastDate: expiredDate}
		pool.trackEntryCreatedLocked(domain)
	}
	for i := 0; i < stateCompactionMinEntries/4; i++ {
		domain := fmt.Sprintf("fresh-%d.example", i)
		pool.stats[domain] = &statEntry{LastDate: freshDate}
		pool.trackEntryCreatedLocked(domain)
	}

	oldStatsPtr := reflect.ValueOf(pool.stats).Pointer()
	oldVariantsPtr := reflect.ValueOf(pool.domainVariantCount).Pointer()
	pool.pruneExpiredLocked()
	newStatsPtr := reflect.ValueOf(pool.stats).Pointer()
	newVariantsPtr := reflect.ValueOf(pool.domainVariantCount).Pointer()
	pool.mu.Unlock()

	if len(pool.stats) != stateCompactionMinEntries/4 {
		t.Fatalf("len(pool.stats) = %d, want %d", len(pool.stats), stateCompactionMinEntries/4)
	}
	if pool.domainCount != stateCompactionMinEntries/4 {
		t.Fatalf("domainCount = %d, want %d", pool.domainCount, stateCompactionMinEntries/4)
	}
	if oldStatsPtr == newStatsPtr {
		t.Fatal("expected stats map to be compacted after prune")
	}
	if oldVariantsPtr == newVariantsPtr {
		t.Fatal("expected domainVariantCount map to be compacted after prune")
	}
}
