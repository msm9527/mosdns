package domain_stats_pool

import (
	"testing"

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
