package domain_memory_pool

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func negativePoolQType(kind string) uint16 {
	if kind == "nov6" || kind == "nodenov6" {
		return dns.TypeAAAA
	}
	return dns.TypeA
}

func isolateNegativeTestStore(t *testing.T, pool *domainMemoryPool) {
	t.Helper()
	pool.dbPath = t.TempDir() + "/control.db"
	t.Cleanup(func() {
		_ = pool.Close()
		runtimesqlite.ResetPersistent(pool.dbPath)
	})
}

func TestNegativeBackgroundRequiresUserConfirmation(t *testing.T) {
	for _, kind := range []string{"nov4", "nov6", "nodenov4", "nodenov6"} {
		for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
			for _, fast := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/fast%v", kind, source.String(), fast), func(t *testing.T) {
					pool, consumer, enqueuer := newSourceTestPool(t, kind)
					isolateNegativeTestStore(t, pool)
					qtype, domain := negativePoolQType(kind), "audiences.me"
					negative := sourceQuery(domain, qtype, source, dns.RcodeNameError, false)
					// Repeated background evidence never replaces user confirmation.
					for i := 0; i < 3; i++ {
						executeSourceQuery(t, pool, fast, negative)
						entry := pool.stats[buildEntryKey(domain, 0)]
						if entry == nil || entry.Promoted || entry.Count != 0 || entry.Score != 0 || entry.LastSeenAtUnixMS != 0 || pool.totalCount != 0 {
							t.Fatalf("background created a blocking rule %+v", entry)
						}
						if pool.AllowHotRule(domain, time.Now()) || len(pool.snapshotPromotedRules()) != 0 {
							t.Fatal("unconfirmed negative reached active rules")
						}
					}
					// A subsequent valid answer cannot be suppressed by this record.
					assertNegativeRouteAnswer(t, pool, qtype, server.RequestSourceUser, true)
					assertNegativeRouteAnswer(t, pool, qtype, server.RequestSourceRefresh, true)
					assertNoHotRuleCall(t, consumer.addCh, 15*time.Millisecond)
					assertNoSourceRefresh(t, enqueuer)
					for i := 1; i <= pool.policy.promoteAfter; i++ {
						executeSourceQuery(t, pool, fast, sourceQuery(domain, qtype, server.RequestSourceUser, dns.RcodeNameError, false))
						entry := pool.stats[buildEntryKey(domain, 0)]
						if entry.Count != i || entry.Score != i || entry.Promoted != (i >= pool.policy.promoteAfter) {
							t.Fatalf("user threshold changed at observation %d: %+v", i, entry)
						}
					}
					_ = waitHotRuleCall(t, consumer.addCh)
					entry := pool.stats[buildEntryKey(domain, 0)]
					before := *entry
					if !pool.AllowHotRuleWithSource(domain, time.Now(), server.RequestSourceUser) || !pool.AllowHotRuleWithSource(domain, time.Now(), server.RequestSourcePrewarm) {
						t.Fatal("confirmed negative is unavailable to normal queries")
					}
					if pool.AllowHotRuleWithSource(domain, time.Now(), server.RequestSourceRefresh) || *entry != before {
						t.Fatal("refresh must bypass its own negative rule without changing evidence")
					}
					if err := pool.SaveToDisk(context.Background()); err != nil {
						t.Fatal(err)
					}
					assertNegativeRouteAnswer(t, pool, qtype, server.RequestSourceUser, false)
					assertNegativeRouteAnswer(t, pool, qtype, server.RequestSourceRefresh, true)
					assertNoSourceRefresh(t, enqueuer)
				})
			}
		}
	}
}

func TestNegativeDirtyRefreshNeedsActualNegativeEvidence(t *testing.T) {
	for _, kind := range []string{"nov4", "nov6", "nodenov4", "nodenov6"} {
		for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
			for _, fast := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/fast%v", kind, source.String(), fast), func(t *testing.T) {
					pool, consumer, enqueuer := newSourceTestPool(t, kind)
					isolateNegativeTestStore(t, pool)
					qtype, domain := negativePoolQType(kind), "audiences.me"
					stale := time.Now().Add(-7 * time.Hour).UnixMilli()
					key := buildEntryKey(domain, 0)
					entry := &statEntry{Count: 2, Score: 3, Promoted: true, QTypeMask: qtypeToMask(qtype),
						LastSeenAtUnixMS: stale, LastVerifiedAtUnixMS: stale, LastDirtyAtUnixMS: stale, RefreshState: "clean"}
					pool.stats[key] = entry
					pool.trackEntryCreatedLocked(domain)
					if pool.AllowHotRuleWithSource(domain, time.Now(), source) || entry.RefreshState != "clean" {
						t.Fatal("background stale lookup created demand or accepted expired evidence")
					}
					if pool.AllowHotRule(domain, time.Now()) || entry.RefreshState != "dirty" {
						t.Fatal("expired user lookup did not disable the negative rule")
					}
					select {
					case job := <-enqueuer.jobs:
						if job.Domain != domain || job.Reason != "stale" {
							t.Fatalf("unexpected refresh %+v", job)
						}
					case <-time.After(time.Second):
						t.Fatal("expired negative did not request refresh")
					}
					before := *entry
					for _, rcode := range []int{dns.RcodeServerFailure, dns.RcodeRefused, dns.RcodeSuccess} {
						executeSourceQuery(t, pool, fast, sourceQuery(domain, qtype, source, rcode, true))
					}
					if n, err := pool.MarkDomainVerified(context.Background(), domain, ""); err != nil || n != 0 || *entry != before {
						t.Fatalf("completion without negative evidence reactivated rule n=%d err=%v entry=%+v", n, err, entry)
					}
					if err := pool.SaveToDisk(context.Background()); err != nil {
						t.Fatal(err)
					}
					for i := 0; i < 3; i++ {
						if pool.AllowHotRule(domain, time.Now()) || *entry != before {
							t.Fatal("dirty rule was reactivated or ignored refresh cooldown")
						}
					}
					assertNegativeRouteAnswer(t, pool, qtype, server.RequestSourceUser, true)
					assertNegativeRouteAnswer(t, pool, qtype, server.RequestSourceRefresh, true)
					assertNoSourceRefresh(t, enqueuer)
					// An actual fresh NXDOMAIN may still renew a confirmed negative.
					executeSourceQuery(t, pool, fast, sourceQuery(domain, qtype, source, dns.RcodeNameError, false))
					if !entry.Promoted || entry.RefreshState != "clean" || entry.DirtyReason != "" || entry.CooldownUntilUnixMS != 0 || !pool.AllowHotRule(domain, time.Now()) {
						t.Fatalf("fresh negative failed to verify confirmed history %+v", entry)
					}
					if entry.Count != before.Count || entry.Score != before.Score || entry.LastSeenAtUnixMS != before.LastSeenAtUnixMS || pool.totalCount != 0 {
						t.Fatal("background verification changed user activity")
					}
					assertNoHotRuleCall(t, consumer.addCh, 15*time.Millisecond)
					assertNoSourceRefresh(t, enqueuer)
				})
			}
		}
	}
}

func TestNegativeLoadDemotesUnconfirmedLegacyRules(t *testing.T) {
	for _, kind := range []string{"nov4", "nov6", "nodenov4", "nodenov6", "realip", "fakeip"} {
		t.Run(kind, func(t *testing.T) {
			old := coremain.MainConfigBaseDir
			coremain.MainConfigBaseDir = t.TempDir()
			t.Cleanup(func() { coremain.MainConfigBaseDir = old })
			path, tag := t.TempDir()+"/control.db", "my_"+kind+"list"
			pool, err := newDomainMemoryPoolWithDeps(tag, nil, nil, nil, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { runtimesqlite.ResetPersistent(path) })
			stamp := time.Now().Add(-time.Minute).UnixMilli()
			for _, seed := range []struct {
				domain string
				flags  uint8
				count  int
			}{{"zero.example", 0, 0}, {"one.example", 0, 1}, {"valid.example", 0, 2}, {"mixed.example", 0, 0}, {"mixed.example", 1, 2}} {
				key := buildEntryKey(seed.domain, seed.flags)
				pool.stats[key] = &statEntry{Count: seed.count, Score: seed.count, Promoted: true, QTypeMask: qtypeToMask(negativePoolQType(kind)),
					LastSeenAtUnixMS: stamp, LastVerifiedAtUnixMS: stamp, LastDirtyAtUnixMS: stamp, RefreshState: "clean"}
				pool.trackEntryCreatedLocked(seed.domain)
			}
			if err := pool.saveState(pool.buildSnapshot(WriteModeSave).state); err != nil {
				t.Fatal(err)
			}
			loaded, err := newDomainMemoryPoolWithDeps(tag, nil, nil, nil, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := loaded.loadFromStore(); err != nil {
				t.Fatal(err)
			}
			negative := loaded.isNegativePool()
			for key, entry := range loaded.stats {
				want := !negative || entry.Count >= loaded.policy.promoteAfter
				if entry.Promoted != want {
					t.Fatalf("legacy promotion not migrated key=%+v entry=%+v", key, entry)
				}
			}
			wantRules := []string{"full:mixed.example", "full:one.example", "full:valid.example", "full:zero.example"}
			if negative {
				wantRules = []string{"full:mixed.example", "full:valid.example"}
			}
			if got, _ := loaded.GetRules(); !reflect.DeepEqual(got, wantRules) || loaded.dirtyPending.Load() != negative {
				t.Fatalf("legacy active rules or migration persistence wrong rules=%v pending=%v", got, loaded.dirtyPending.Load())
			}
			if loaded.AllowHotRule("zero.example", time.Now()) == negative || !loaded.AllowHotRule("mixed.example", time.Now()) {
				t.Fatal("runtime validity did not preserve qualifying flag variants")
			}
			if err := loaded.SaveToDisk(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, ok, err := coremain.LoadDomainPoolStateFromPath(path, tag)
			if err != nil || !ok || !reflect.DeepEqual(normalizePoolHotRules(buildRulesFromStoredDomains(state.Domains)), wantRules) {
				t.Fatalf("downgrade did not persist %+v err=%v", state.Domains, err)
			}
			for _, variant := range state.Variants {
				if negative && variant.TotalCount < loaded.policy.promoteAfter && variant.Promoted {
					t.Fatal("unconfirmed variant remained promoted on disk")
				}
			}
		})
	}
}
