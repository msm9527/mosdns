package coremain

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"
)

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

func deltaPoolState(tag string, n int) DomainPoolState {
	state := DomainPoolState{Meta: DomainPoolMeta{PoolTag: tag, PoolKind: DomainPoolKindStats, Policy: defaultDomainPoolPolicy(tag), DomainCount: n, VariantCount: n}}
	for i := 0; i < n; i++ {
		domain := fmt.Sprintf("domain-%05d.example", i)
		state.Domains = append(state.Domains, DomainPoolDomain{PoolTag: tag, Domain: domain, TotalCount: 1})
		state.Variants = append(state.Variants, DomainPoolVariant{PoolTag: tag, Domain: domain, VariantKey: "q:1", TotalCount: 1})
	}
	return state
}

func TestDomainPoolStoreDelta(t *testing.T) {
	path := RuntimeStateDBPathForPath(t.TempDir() + "/config.yaml")
	state := deltaPoolState("top_domains", 2000)
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatal(err)
	}
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	db := store.db.DB()
	changes := func() int64 {
		var n int64
		if err := db.QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := changes()
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatal(err)
	}
	if n := changes() - before; n != 0 {
		t.Fatalf("unchanged snapshot changed %d rows", n)
	}
	loaded, _, err := LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil {
		t.Fatal(err)
	}
	before = changes()
	state.Domains[0].TotalCount++
	state.Variants[0].TotalCount++
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatal(err)
	}
	if n := changes() - before; n != 2 {
		t.Fatalf("single domain delta changed %d rows, want 2", n)
	}
	next, _, err := LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Domains[1].UpdatedAtUnixMS != next.Domains[1].UpdatedAtUnixMS {
		t.Fatal("untouched timestamp changed")
	}
}

func TestDomainPoolStoreDeleteClearAndAtomicity(t *testing.T) {
	path := RuntimeStateDBPathForPath(t.TempDir() + "/config.yaml")
	state := deltaPoolState("top_domains", 3)
	other := deltaPoolState("other_pool", 1)
	for _, s := range []DomainPoolState{state, other} {
		if err := SaveDomainPoolStateToPath(path, s); err != nil {
			t.Fatal(err)
		}
	}
	original, _, err := LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil {
		t.Fatal(err)
	}
	state.Domains = state.Domains[1:]
	state.Variants = state.Variants[1:]
	state.Domains[0].TotalCount = 9
	state.Variants[0].TotalCount = 9
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.DB().Exec("CREATE TEMP TRIGGER fail_variant BEFORE UPDATE ON domain_pool_variant BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := SaveDomainPoolStateToPath(path, state); err == nil {
		t.Fatal("expected transaction failure")
	}
	loaded, _, err := LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, loaded) {
		t.Fatal("failed save changed original state")
	}
	if _, err := store.db.DB().Exec("DROP TRIGGER fail_variant"); err != nil {
		t.Fatal(err)
	}
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Domains) != 2 || len(loaded.Variants) != 2 || loaded.Domains[0].TotalCount != 9 {
		t.Fatalf("unexpected delta %+v", loaded)
	}
	state.Domains = nil
	state.Variants = nil
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil || !ok || len(loaded.Domains) != 0 || len(loaded.Variants) != 0 {
		t.Fatalf("clear failed %+v %v", loaded, err)
	}
	loaded, ok, err = LoadDomainPoolStateFromPath(path, "other_pool")
	if err != nil || !ok || len(loaded.Domains) != 1 {
		t.Fatalf("other pool changed %+v %v", loaded, err)
	}
}

// legacyPoolSave retains the previous replacement algorithm for compatibility
// and identical-workload measurement, including its transaction boundary.
func legacyPoolSave(db *sql.DB, state DomainPoolState) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveDomainPoolMeta(tx, state.Meta); err != nil {
		return err
	}
	if err := clearDomainPoolRows(tx, state.Meta.PoolTag); err != nil {
		return err
	}
	if err := saveDomainPoolDomains(tx, state.Domains); err != nil {
		return err
	}
	if err := saveDomainPoolVariants(tx, state.Variants); err != nil {
		return err
	}
	return tx.Commit()
}

func TestDomainPoolStoreLegacyCompatibilityAndNewRows(t *testing.T) {
	path := t.TempDir() + "/control.db"
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	state := deltaPoolState("top_domains", 2)
	if err := legacyPoolSave(store.db.DB(), state); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil || !ok || len(loaded.Domains) != 2 {
		t.Fatalf("legacy load %+v %v", loaded, err)
	}
	state = deltaPoolState("top_domains", 3)
	state.Variants[0].VariantKey = "q:28"
	state.Domains[0].Promoted = true
	state.Domains[0].CooldownUntilUnixMS = 99
	if err := SaveDomainPoolStateToPath(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err = LoadDomainPoolStateFromPath(path, "top_domains")
	if err != nil || !ok || len(loaded.Domains) != 3 || len(loaded.Variants) != 3 {
		t.Fatalf("incremental load %+v %v", loaded, err)
	}
	for _, variant := range loaded.Variants {
		if variant.Domain == state.Domains[0].Domain && variant.VariantKey != "q:28" {
			t.Fatal("deleted variant survived")
		}
	}
	malformed := state
	malformed.Domains = append(append([]DomainPoolDomain(nil), state.Domains...), state.Domains[0])
	if err := SaveDomainPoolStateToPath(path, malformed); err == nil {
		t.Fatal("duplicate accepted")
	}
	malformed = state
	malformed.Domains = nil
	if err := SaveDomainPoolStateToPath(path, malformed); err == nil {
		t.Fatal("orphan variant accepted")
	}
}

func TestDomainPoolPersistenceIdenticalWorkloadBytes(t *testing.T) {
	type measure struct {
		bytes, rows    int64
		transactions   int
		maxFramesPerTx int
	}
	run := func(legacy bool) (measure, DomainPoolState) {
		path := t.TempDir() + "/control.db"
		store, err := getRuntimeStateStoreByPath(path)
		if err != nil {
			t.Fatal(err)
		}
		db := store.db.DB()
		state := deltaPoolState("top_domains", 2000)
		// Seed both stores with the previous version's schema and save algorithm.
		if err := legacyPoolSave(db, state); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("PRAGMA wal_autocheckpoint=0; PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			t.Fatal(err)
		}
		var before, after int64
		if err := db.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
			t.Fatal(err)
		}
		m := measure{}
		save := func() {
			if legacy {
				err = legacyPoolSave(db, state)
			} else {
				err = SaveDomainPoolStateToPath(path, state)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		// One refresh batch verifies 32 domains, then a periodic single-domain
		// observation follows. Both executions reach the same durable final state.
		for i := 0; i < 32; i++ {
			state.Domains[i].RefreshState = "clean"
			state.Domains[i].LastVerifiedAtUnixMS = 100
			state.Variants[i].RefreshState = "clean"
			state.Variants[i].LastVerifiedAtUnixMS = 100
			if legacy {
				save()
			}
		}
		if !legacy {
			save()
		}
		state.Domains[50].TotalCount++
		state.Variants[50].TotalCount++
		save()
		if err := db.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
			t.Fatal(err)
		}
		m.rows = after - before
		wal, err := os.ReadFile(path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		m.bytes = int64(len(wal))
		if len(wal) < 32 {
			t.Fatal("missing WAL header")
		}
		pageSize := int(binary.BigEndian.Uint32(wal[8:12]))
		if pageSize == 1 {
			pageSize = 65536
		}
		frames := 0
		// A nonzero database-size field in a WAL frame marks an actual commit.
		for offset := 32; offset+24+pageSize <= len(wal); offset += 24 + pageSize {
			frames++
			if binary.BigEndian.Uint32(wal[offset+4:offset+8]) != 0 {
				m.transactions++
				if frames > m.maxFramesPerTx {
					m.maxFramesPerTx = frames
				}
				frames = 0
			}
		}

		loaded, ok, err := LoadDomainPoolStateFromPath(path, "top_domains")
		if err != nil || !ok {
			t.Fatal(err)
		}
		if len(loaded.Domains) != 2000 || len(loaded.Variants) != 2000 {
			t.Fatal("workload lost pool rows")
		}
		loaded.Meta.UpdatedAtUnixMS = 0
		for i := range loaded.Domains {
			loaded.Domains[i].UpdatedAtUnixMS = 0
		}
		for i := range loaded.Variants {
			loaded.Variants[i].UpdatedAtUnixMS = 0
		}
		return m, loaded
	}
	old, oldState := run(true)
	next, nextState := run(false)
	if !reflect.DeepEqual(oldState, nextState) {
		t.Fatal("identical workload reached different durable state")
	}
	t.Logf("same 2000-domain pool, 32 verified domains plus one observed domain: legacy WAL_bytes=%d changed_rows=%d transactions=%d max_frames_per_tx=%d; delta+batch WAL_bytes=%d changed_rows=%d transactions=%d max_frames_per_tx=%d", old.bytes, old.rows, old.transactions, old.maxFramesPerTx, next.bytes, next.rows, next.transactions, next.maxFramesPerTx)
	if next.bytes >= old.bytes || next.rows >= old.rows || next.transactions != 2 {
		t.Fatalf("write amplification did not fall old=%+v next=%+v", old, next)
	}
}
