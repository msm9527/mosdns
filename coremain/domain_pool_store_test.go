package coremain

import "testing"

func TestDomainPoolStoreRoundTrip(t *testing.T) {
	path := RuntimeStateDBPathForPath(t.TempDir() + "/config.yaml")
	state := DomainPoolState{
		Meta: DomainPoolMeta{
			PoolTag:              "my_realiplist",
			PoolKind:             DomainPoolKindMemory,
			MemoryID:             "realip",
			Policy:               defaultDomainPoolPolicy("my_realiplist"),
			DomainCount:          1,
			VariantCount:         2,
			DirtyDomainCount:     1,
			PromotedDomainCount:  1,
			PublishedDomainCount: 1,
			TotalObservations:    10,
			LastFlushAtUnixMS:    100,
		},
		Domains: []DomainPoolDomain{{
			PoolTag:           "my_realiplist",
			Domain:            "example.com",
			TotalCount:        10,
			Score:             10,
			QTypeMask:         3,
			VariantCount:      2,
			DirtyVariantCount: 1,
			Promoted:          true,
			RefreshState:      "dirty",
		}},
		Variants: []DomainPoolVariant{
			{
				PoolTag:    "my_realiplist",
				Domain:     "example.com",
				VariantKey: "q:1|f:0",
				TotalCount: 6,
				Score:      6,
			},
			{
				PoolTag:    "my_realiplist",
				Domain:     "example.com",
				VariantKey: "q:2|f:0",
				TotalCount: 4,
				Score:      4,
				Promoted:   true,
			},
		},
	}

	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatalf("SaveDomainPoolStateToPath: %v", err)
	}

	loaded, ok, err := LoadDomainPoolStateFromPath(path, "my_realiplist")
	if err != nil {
		t.Fatalf("LoadDomainPoolStateFromPath: %v", err)
	}
	if !ok {
		t.Fatal("expected domain pool state to exist")
	}
	if loaded.Meta.Policy.PublishTo != "my_realiprule" {
		t.Fatalf("unexpected policy: %+v", loaded.Meta.Policy)
	}
	if len(loaded.Domains) != 1 || loaded.Domains[0].Domain != "example.com" {
		t.Fatalf("unexpected domains: %+v", loaded.Domains)
	}
	if len(loaded.Variants) != 2 {
		t.Fatalf("unexpected variants: %+v", loaded.Variants)
	}
}

func TestDomainPoolStoreListQueries(t *testing.T) {
	path := RuntimeStateDBPathForPath(t.TempDir() + "/config.yaml")
	state := DomainPoolState{
		Meta: DomainPoolMeta{
			PoolTag:      "top_domains",
			PoolKind:     DomainPoolKindStats,
			MemoryID:     "top",
			Policy:       defaultDomainPoolPolicy("top_domains"),
			DomainCount:  2,
			VariantCount: 2,
		},
		Domains: []DomainPoolDomain{
			{PoolTag: "top_domains", Domain: "beta.com", TotalCount: 2, Score: 2},
			{PoolTag: "top_domains", Domain: "alpha.com", TotalCount: 5, Score: 5},
		},
		Variants: []DomainPoolVariant{
			{PoolTag: "top_domains", Domain: "alpha.com", VariantKey: "q:0|f:1", TotalCount: 5, Score: 5},
			{PoolTag: "top_domains", Domain: "beta.com", VariantKey: "q:0|f:1", TotalCount: 2, Score: 2},
		},
	}
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatalf("SaveDomainPoolStateToPath: %v", err)
	}

	domains, total, err := ListDomainPoolDomainsFromPath(path, DomainPoolDomainQuery{
		PoolTag: "top_domains",
		Query:   "a",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListDomainPoolDomainsFromPath: %v", err)
	}
	if total != 2 || domains[0].Domain != "alpha.com" {
		t.Fatalf("unexpected domain query result: total=%d items=%+v", total, domains)
	}

	variants, total, err := ListDomainPoolVariantsFromPath(path, DomainPoolVariantQuery{
		PoolTag: "top_domains",
		Domain:  "alpha",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListDomainPoolVariantsFromPath: %v", err)
	}
	if total != 1 || variants[0].Domain != "alpha.com" {
		t.Fatalf("unexpected variant query result: total=%d items=%+v", total, variants)
	}
}
