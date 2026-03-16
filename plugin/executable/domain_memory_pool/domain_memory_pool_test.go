package domain_memory_pool

import (
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

func TestMemoryPoolAggregatesEntriesByBareDomain(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	if err := coremain.SaveMemoryPoolPoliciesToCustomConfig(map[string]coremain.DomainPoolPolicy{
		"my_realiplist": {
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
		},
	}); err != nil {
		t.Fatalf("SaveMemoryPoolPoliciesToCustomConfig: %v", err)
	}

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
	items, total, err := loaded.MemoryEntries("", 0, 10)
	if err != nil {
		t.Fatalf("MemoryEntries: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Domain != "example.com" {
		t.Fatalf("unexpected reloaded items: total=%d items=%+v", total, items)
	}
}
