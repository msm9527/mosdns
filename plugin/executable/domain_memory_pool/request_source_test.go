package domain_memory_pool

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

type sourceRefreshEnqueuer struct {
	jobs chan coremain.DomainRefreshJob
}

func (e *sourceRefreshEnqueuer) EnqueueDomainRefresh(_ context.Context, job coremain.DomainRefreshJob) bool {
	e.jobs <- job
	return true
}

func newSourceTestPool(t *testing.T, kind string) (*domainMemoryPool, *mockHotRuleConsumer, *sourceRefreshEnqueuer) {
	t.Helper()
	old := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() { coremain.MainConfigBaseDir = old })
	tag := "my_" + kind + "list"
	saveMemoryPoolPolicyForTest(t, tag, coremain.DomainPoolPolicy{
		Kind: coremain.DomainPoolKindMemory, PublishTo: "my_" + kind + "rule", RequeryTag: "requery",
		PromoteAfter: 2, TrackQType: true, TrackFlags: true, MaxDomains: 100, MaxVariantsPerDomain: 4,
		EvictionPolicy: "lru", StaleAfterMinutes: 360, RefreshCooldownMinutes: 120,
		FlushIntervalMS: 1000, PruneIntervalSec: 60,
	})
	consumer := newMockHotRuleConsumer()
	enqueuer := &sourceRefreshEnqueuer{jobs: make(chan coremain.DomainRefreshJob, 32)}
	manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"mapper": consumer, "requery": enqueuer})
	pool, err := newDomainMemoryPool(tag, manager, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool, consumer, enqueuer
}

func sourceQuery(name string, qtype uint16, source server.RequestSource, rcode int, hasAddress bool) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), qtype)
	qCtx := query_context.NewContext(q)
	qCtx.ServerMeta.RequestSource = source
	if rcode < 0 {
		return qCtx
	}
	r := new(dns.Msg)
	r.SetReply(q)
	r.Rcode = rcode
	if hasAddress {
		hdr := dns.RR_Header{Name: q.Question[0].Name, Rrtype: qtype, Class: dns.ClassINET, Ttl: 60}
		if qtype == dns.TypeA {
			r.Answer = []dns.RR{&dns.A{Hdr: hdr, A: net.IPv4(1, 1, 1, 1)}}
		} else if qtype == dns.TypeAAAA {
			r.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("2001:db8::1")}}
		}
	}
	qCtx.SetResponse(r)
	return qCtx
}

func executeSourceQuery(t *testing.T, pool *domainMemoryPool, fast bool, qCtx *query_context.Context) {
	t.Helper()
	exec := pool.Exec
	if fast {
		exec = pool.GetFastExec()
	}
	if err := exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	pool.drainPendingRecords()
}

func assertNoSourceRefresh(t *testing.T, enqueuer *sourceRefreshEnqueuer) {
	t.Helper()
	select {
	case job := <-enqueuer.jobs:
		t.Fatalf("unexpected on-demand job %+v", job)
	case <-time.After(15 * time.Millisecond):
	}
}

func TestBackgroundClassificationKeepsUserActivityZero(t *testing.T) {
	old := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() { coremain.MainConfigBaseDir = old })
	pool, err := newDomainMemoryPool("my_realiplist", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := new(dns.Msg)
	q.SetQuestion("background.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	qCtx.ServerMeta.RequestSource = server.RequestSourceRefresh
	r := new(dns.Msg)
	r.SetReply(q)
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: []byte{1, 1, 1, 1}}}
	qCtx.SetResponse(r)
	if err := pool.Exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	pool.drainPendingRecords()
	entry := pool.stats[buildEntryKey("background.example", 0)]
	if entry == nil || entry.Count != 0 || entry.Score != 0 || entry.LastSeenAtUnixMS != 0 || pool.totalCount != 0 {
		t.Fatalf("background classification recorded user activity entry=%+v total=%d", entry, pool.totalCount)
	}
	if !entry.Promoted || entry.LastVerifiedAtUnixMS <= 0 || entry.RefreshState != "clean" || entry.QTypeMask != qtypeMaskA {
		t.Fatalf("successful classification missing verification %+v", entry)
	}
}

func TestBackgroundClassificationSurvivesVerifyBeforeWorker(t *testing.T) {
	pool, consumer, enqueuer := newSourceTestPool(t, "realip")
	qCtx := sourceQuery("verify-first.example", dns.TypeA, server.RequestSourceRefresh, dns.RcodeSuccess, true)
	if err := pool.Exec(context.Background(), qCtx); err != nil {
		t.Fatal(err)
	}
	// Requery's verify may run while the classification record is still queued.
	if n, err := pool.MarkDomainVerified(context.Background(), "verify-first.example", ""); n != 0 || err == nil {
		t.Fatalf("expected missing queued classification n=%d err=%v", n, err)
	}
	pool.drainPendingRecords()
	entry := pool.stats[buildEntryKey("verify-first.example", 0)]
	if entry == nil || !entry.Promoted || entry.Count != 0 || entry.LastSeenAtUnixMS != 0 || entry.LastVerifiedAtUnixMS <= 0 || entry.RefreshState != "clean" || !pool.AllowHotRule("verify-first.example", time.Now()) {
		t.Fatalf("classification depended on verify ordering %+v", entry)
	}
	_ = waitHotRuleCall(t, consumer.addCh)
	assertNoSourceRefresh(t, enqueuer)
}

func TestBackgroundParsedStaleCacheDoesNotVerify(t *testing.T) {
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		for _, fast := range []bool{false, true} {
			pool, consumer, enqueuer := newSourceTestPool(t, "realip")
			qCtx := sourceQuery("parsed-stale.example", dns.TypeA, source, dns.RcodeSuccess, true)
			qCtx.ServerMeta.FromUDP = false
			qCtx.SetCacheResponseStale(true)
			if qCtx.ResponsePayload() != nil {
				t.Fatal("fixture should exercise parsed TCP response")
			}
			executeSourceQuery(t, pool, fast, qCtx)
			if len(pool.stats) != 0 || pool.totalCount != 0 || pool.dirtyPending.Load() {
				t.Fatalf("parsed stale response established classification %+v", pool.stats)
			}
			assertNoHotRuleCall(t, consumer.addCh, 15*time.Millisecond)
			assertNoSourceRefresh(t, enqueuer)
		}
	}
}

func TestBackgroundClassificationAllPoolsAndExecutors(t *testing.T) {
	classifications := []struct {
		kind    string
		qtype   uint16
		rcode   int
		address bool
	}{
		{"realip", dns.TypeA, dns.RcodeSuccess, true},
		{"realip", dns.TypeAAAA, dns.RcodeSuccess, true},
		{"fakeip", dns.TypeA, dns.RcodeSuccess, true},
		{"fakeip", dns.TypeAAAA, dns.RcodeSuccess, true},
		{"nov4", dns.TypeA, dns.RcodeNameError, false},
		{"nov6", dns.TypeAAAA, dns.RcodeNameError, false},
		{"nodenov4", dns.TypeA, dns.RcodeNameError, false},
		{"nodenov6", dns.TypeAAAA, dns.RcodeNameError, false},
	}
	for _, c := range classifications {
		for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
			for _, fast := range []bool{false, true} {
				name := c.kind + "/" + dns.TypeToString[c.qtype] + "/" + source.String()
				if fast {
					name += "/fast"
				}
				t.Run(name, func(t *testing.T) {
					pool, consumer, enqueuer := newSourceTestPool(t, c.kind)
					qCtx := sourceQuery("classified.example", c.qtype, source, c.rcode, c.address)
					executeSourceQuery(t, pool, fast, qCtx)
					key := buildEntryKey("classified.example", 0)
					entry := pool.stats[key]
					if entry == nil || entry.Count != 0 || entry.Score != 0 || entry.LastSeenAtUnixMS != 0 || pool.totalCount != 0 {
						t.Fatalf("background demand leaked %+v total=%d", entry, pool.totalCount)
					}
					if !entry.Promoted || entry.LastVerifiedAtUnixMS <= 0 || entry.RefreshState != "clean" || entry.QTypeMask != qtypeToMask(c.qtype) || entry.LastSource != source.String() {
						t.Fatalf("classification evidence missing %+v", entry)
					}
					before := *entry
					pool.dirtyPending.Store(false)
					if !pool.AllowHotRuleWithSource(key.domain, time.Now(), source) {
						t.Fatal("fresh background classification is unusable")
					}
					if *entry != before || pool.dirtyPending.Load() {
						t.Fatal("background lookup rewrote verified memory")
					}
					call := waitHotRuleCall(t, consumer.addCh)
					if !reflect.DeepEqual(call.rules, []string{"full:classified.example"}) {
						t.Fatalf("unexpected hot publication %+v", call)
					}
					entry.RefreshState, entry.DirtyReason, entry.CooldownUntilUnixMS = "dirty", "stale", time.Now().Add(time.Hour).UnixMilli()
					executeSourceQuery(t, pool, fast, qCtx)
					if entry.Count != 0 || entry.Score != 0 || entry.LastSeenAtUnixMS != 0 || pool.totalCount != 0 || !entry.Promoted || entry.RefreshState != "clean" || entry.DirtyReason != "" || entry.CooldownUntilUnixMS != 0 {
						t.Fatalf("repeat did not verify without demand %+v", entry)
					}
					assertNoSourceRefresh(t, enqueuer)
					executeSourceQuery(t, pool, fast, sourceQuery(key.domain, c.qtype, server.RequestSourceUser, c.rcode, c.address))
					if entry.Count != 1 || entry.Score != 1 || entry.LastSeenAtUnixMS <= 0 || pool.totalCount != 1 || !entry.Promoted || entry.RefreshState != "clean" {
						t.Fatalf("first user request lost verified classification %+v total=%d", entry, pool.totalCount)
					}
					assertNoHotRuleCall(t, consumer.addCh, 15*time.Millisecond)
					assertNoSourceRefresh(t, enqueuer)
				})
			}
		}
	}
}

func TestBackgroundInvalidClassificationDoesNotPromote(t *testing.T) {
	for _, kind := range []string{"realip", "fakeip", "nov4", "nov6", "nodenov4", "nodenov6"} {
		t.Run(kind, func(t *testing.T) {
			pool, consumer, enqueuer := newSourceTestPool(t, kind)
			qtype := uint16(dns.TypeA)
			if kind == "nov6" || kind == "nodenov6" {
				qtype = dns.TypeAAAA
			}
			for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
				for _, fast := range []bool{false, true} {
					for _, rcode := range []int{-1, dns.RcodeServerFailure, dns.RcodeRefused, dns.RcodeSuccess} {
						executeSourceQuery(t, pool, fast, sourceQuery("invalid.example", qtype, source, rcode, false))
					}
					goodCode := dns.RcodeNameError
					if kind == "realip" || kind == "fakeip" {
						goodCode = dns.RcodeSuccess
					}
					stale := sourceQuery("invalid.example", qtype, source, goodCode, true)
					stale.SetResponsePayload(&query_context.ResponsePayload{Msg: stale.R(), Stale: true})
					executeSourceQuery(t, pool, fast, stale)
					wrongType := uint16(dns.TypeAAAA)
					if qtype == dns.TypeAAAA {
						wrongType = dns.TypeA
					}
					wrong := sourceQuery("invalid.example", wrongType, source, goodCode, false)
					// Positive pools require an address matching the query type.
					executeSourceQuery(t, pool, fast, wrong)
				}
			}
			if len(pool.stats) != 0 || pool.totalCount != 0 || pool.dirtyPending.Load() {
				t.Fatalf("invalid classification created memory %+v", pool.stats)
			}
			assertNoHotRuleCall(t, consumer.addCh, 15*time.Millisecond)
			// A failed refresh cannot clear existing dirty state or fabricate verification.
			key := buildEntryKey("invalid.example", 0)
			pool.stats[key] = &statEntry{Promoted: true, Count: 12, Score: 17, QTypeMask: qtypeToMask(qtype),
				RefreshState: "dirty", DirtyReason: "stale", CooldownUntilUnixMS: time.Now().Add(time.Hour).UnixMilli(),
				LastSeenAtUnixMS: time.Now().Add(-7 * time.Hour).UnixMilli()}
			pool.trackEntryCreatedLocked(key.domain)
			before := *pool.stats[key]
			for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
				for _, fast := range []bool{false, true} {
					executeSourceQuery(t, pool, fast, sourceQuery(key.domain, qtype, source, dns.RcodeServerFailure, true))
				}
			}
			if *pool.stats[key] != before || pool.dirtyPending.Load() || pool.totalCount != 0 {
				t.Fatalf("failed refresh mutated existing classification %+v", pool.stats[key])
			}
			assertNoSourceRefresh(t, enqueuer)
		})
	}
}

func TestUserDemandPromotionAndExpiredVerifiedHistory(t *testing.T) {
	for _, fast := range []bool{false, true} {
		pool, consumer, enqueuer := newSourceTestPool(t, "realip")
		domain := "user.example"
		executeSourceQuery(t, pool, fast, sourceQuery(domain, dns.TypeA, server.RequestSourceUser, dns.RcodeSuccess, true))
		entry := pool.stats[buildEntryKey(domain, 0)]
		if entry == nil || entry.Count != 1 || entry.Score != 1 || entry.Promoted || pool.totalCount != 1 {
			t.Fatalf("first user observation incorrect %+v", entry)
		}
		// Unspecified remains compatible with direct executor callers.
		executeSourceQuery(t, pool, fast, sourceQuery(domain, dns.TypeA, server.RequestSourceUnspecified, dns.RcodeSuccess, true))
		if entry.Count != 2 || entry.Score != 2 || !entry.Promoted || pool.totalCount != 2 {
			t.Fatalf("user threshold promotion changed %+v", entry)
		}
		_ = waitHotRuleCall(t, consumer.addCh)
		select {
		case job := <-enqueuer.jobs:
			if job.Domain != domain || job.Reason != "observed" {
				t.Fatalf("unexpected job %+v", job)
			}
		case <-time.After(time.Second):
			t.Fatal("user threshold did not request refresh")
		}
		// Long-decayed verification keeps its historical identity for stale matching.
		entry.Count, entry.Score = 0, 0
		entry.LastVerifiedAtUnixMS = time.Now().AddDate(0, 0, -30).UnixMilli()
		entry.LastDirtyAtUnixMS = entry.LastVerifiedAtUnixMS
		entry.LastSeenAtUnixMS = 0
		entry.RefreshState, entry.DirtyReason, entry.CooldownUntilUnixMS = "clean", "", 0
		executeSourceQuery(t, pool, fast, sourceQuery(domain, dns.TypeA, server.RequestSourceUser, dns.RcodeSuccess, true))
		if !entry.Promoted || entry.Count != 1 || entry.Score != 1 || pool.AllowHotRuleWithSource(domain, time.Now(), server.RequestSourcePrewarm) {
			t.Fatalf("expired history lost identity or bypassed validity %+v", entry)
		}
		select {
		case job := <-enqueuer.jobs:
			if job.Reason != "stale" {
				t.Fatalf("unexpected history refresh %+v", job)
			}
		case <-time.After(time.Second):
			t.Fatal("expired user history did not request refresh")
		}
	}
}

func TestBackgroundHotValidationHasNoSideEffects(t *testing.T) {
	pool, _, enqueuer := newSourceTestPool(t, "realip")
	now := time.Now().UTC()
	key := buildEntryKey("expired.example", 0)
	pool.stats[key] = &statEntry{Promoted: true, QTypeMask: qtypeMaskA, Count: 5, Score: 5,
		LastSeenAtUnixMS: now.Add(-7 * time.Hour).UnixMilli(), LastVerifiedAtUnixMS: now.Add(-7 * time.Hour).UnixMilli(), RefreshState: "clean"}
	pool.trackEntryCreatedLocked(key.domain)
	before := *pool.stats[key]
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		if pool.AllowHotRuleWithSource(key.domain, now, source) {
			t.Fatal("background accepted expired hot rule")
		}
		if *pool.stats[key] != before || pool.dirtyPending.Load() {
			t.Fatalf("background rejection mutated state %+v", pool.stats[key])
		}
	}
	assertNoSourceRefresh(t, enqueuer)
	if pool.AllowHotRule(key.domain, now) {
		t.Fatal("legacy user accepted expired rule")
	}
	select {
	case job := <-enqueuer.jobs:
		if job.Domain != key.domain || job.Reason != "stale" {
			t.Fatalf("unexpected job %+v", job)
		}
	case <-time.After(time.Second):
		t.Fatal("user rejection did not enqueue refresh")
	}
	entry := pool.stats[key]
	if entry.RefreshState != "dirty" || entry.DirtyReason != "stale" || entry.CooldownUntilUnixMS <= now.UnixMilli() || !pool.dirtyPending.Load() {
		t.Fatalf("user refresh state missing %+v", entry)
	}
	before = *entry
	pool.dirtyPending.Store(false)
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		if pool.AllowHotRuleWithSource(key.domain, now, source) || *entry != before || pool.dirtyPending.Load() {
			t.Fatalf("background dirty rejection changed state %+v", entry)
		}
	}
	assertNoSourceRefresh(t, enqueuer)
}

func TestBackgroundVerificationPreservesHistoricalDemandAndRestores(t *testing.T) {
	pool, consumer, enqueuer := newSourceTestPool(t, "realip")
	pool.dbPath = t.TempDir() + "/control.db"
	t.Cleanup(func() { runtimesqlite.ResetPersistent(pool.dbPath) })
	key := buildEntryKey("history.example", 0)
	pool.processRecord(&logItem{name: key.domain, qtype: dns.TypeA, source: "seed"})
	entry := pool.stats[key]
	entry.Count, entry.Score = 19, 23
	pool.totalCount = 19
	beforeSeen := entry.LastSeenAtUnixMS
	entry.RefreshState, entry.DirtyReason, entry.CooldownUntilUnixMS = "dirty", "stale", time.Now().Add(time.Hour).UnixMilli()
	executeSourceQuery(t, pool, false, sourceQuery(key.domain, dns.TypeAAAA, server.RequestSourceRefresh, dns.RcodeSuccess, true))
	if entry.Count != 19 || entry.Score != 23 || entry.LastSeenAtUnixMS != beforeSeen || pool.totalCount != 19 || entry.QTypeMask != qtypeMaskA|qtypeMaskAAAA || entry.RefreshState != "clean" || entry.DirtyReason != "" || entry.CooldownUntilUnixMS != 0 {
		t.Fatalf("historical demand or verification changed incorrectly %+v", entry)
	}
	_ = waitHotRuleCall(t, consumer.addCh)
	executeSourceQuery(t, pool, true, sourceQuery("zero.example", dns.TypeA, server.RequestSourcePrewarm, dns.RcodeSuccess, true))
	_ = waitHotRuleCall(t, consumer.addCh)
	zero := *pool.stats[buildEntryKey("zero.example", 0)]
	if err := pool.SaveToDisk(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := newDomainMemoryPoolWithDeps(pool.pluginTag, nil, nil, nil, pool.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.loadFromStore(); err != nil {
		t.Fatal(err)
	}
	if got := loaded.stats[buildEntryKey("zero.example", 0)]; got == nil || *got != zero || !loaded.AllowHotRule("zero.example", time.Now()) {
		t.Fatalf("Count0 classification did not restore %+v", got)
	}
	if got := loaded.stats[key]; got == nil || *got != *entry || loaded.totalCount != 19 {
		t.Fatalf("historical counts did not restore %+v total=%d", got, loaded.totalCount)
	}
	verifiedAt := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	loaded.stats[key].RefreshState, loaded.stats[key].DirtyReason, loaded.stats[key].CooldownUntilUnixMS = "dirty", "stale", 999
	if n, err := loaded.MarkDomainVerified(context.Background(), key.domain, verifiedAt); err != nil || n != 1 {
		t.Fatalf("verify n=%d err=%v", n, err)
	}
	state, _, err := coremain.LoadDomainPoolStateFromPath(pool.dbPath, pool.pluginTag)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range state.Variants {
		if v.Domain == key.domain && (v.LastVerifiedAtUnixMS != parseStampUnixMS(verifiedAt) || v.RefreshState != "clean" || v.DirtyReason != "" || v.CooldownUntilUnixMS != 0 || v.TotalCount != 19 || v.Score != 23 || v.LastSeenAtUnixMS != beforeSeen) {
			t.Fatalf("verify failed to preserve demand %+v", v)
		}
	}
	assertNoSourceRefresh(t, enqueuer)
}

func TestBackgroundRetentionAndLRUUseVerificationOnlyWithoutUserTime(t *testing.T) {
	pool, _, _ := newSourceTestPool(t, "realip")
	now := time.Now()
	for domain, entry := range map[string]*statEntry{
		"background-old.example": {Promoted: true, LastVerifiedAtUnixMS: now.AddDate(0, 0, -90).UnixMilli()},
		"background-new.example": {Promoted: true, LastVerifiedAtUnixMS: now.UnixMilli()},
		"user-old.example":       {Promoted: true, Count: 5, LastSeenAtUnixMS: now.AddDate(0, 0, -90).UnixMilli(), LastVerifiedAtUnixMS: now.UnixMilli()},
		"user-new.example":       {Promoted: true, Count: 5, LastSeenAtUnixMS: now.Add(-time.Hour).UnixMilli(), LastVerifiedAtUnixMS: now.UnixMilli()},
	} {
		pool.stats[buildEntryKey(domain, 0)] = entry
		pool.trackEntryCreatedLocked(domain)
	}
	pool.pruneExpiredLocked()
	if len(pool.stats) != 2 || pool.stats[buildEntryKey("background-new.example", 0)] == nil || pool.stats[buildEntryKey("user-new.example", 0)] == nil {
		t.Fatalf("retention used incorrect timestamps %+v", pool.stats)
	}
	if n := pool.evictLRUEntriesLocked(1); n != 1 || pool.stats[buildEntryKey("user-new.example", 0)] != nil {
		t.Fatalf("LRU treated Count0 as oldest %+v", pool.stats)
	}
}
