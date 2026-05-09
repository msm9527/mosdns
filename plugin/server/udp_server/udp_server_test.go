package udp_server

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestFastCacheItemAtomicFieldAlignment(t *testing.T) {
	var item fastCacheItem
	if offset := unsafe.Offsetof(item.expire); offset != 0 {
		t.Fatalf("expire must stay at struct offset 0 for 32-bit atomic alignment, got %d", offset)
	}
}

func mustFastCacheItem(t testing.TB, fc *fastCache, name string, qtype uint16) *fastCacheItem {
	t.Helper()
	hash := fastCachePolicyHash(fastQNameHashString(name, qtype), false)
	item, _ := fc.findItem(hash, name, qtype)
	if item == nil {
		t.Fatalf("expected fast cache item for %s/%d", name, qtype)
	}
	return item
}

func TestInferFastBypassWarmupSec(t *testing.T) {
	if got := inferFastBypassWarmupSec("sequence_requery", ":53"); got != defaultFastBypassWarmupRequery {
		t.Fatalf("requery entry warmup = %d, want %d", got, defaultFastBypassWarmupRequery)
	}
	if got := inferFastBypassWarmupSec("sequence_main", ":7766"); got != defaultFastBypassWarmupRequery {
		t.Fatalf("requery listen warmup = %d, want %d", got, defaultFastBypassWarmupRequery)
	}
	if got := inferFastBypassWarmupSec("sequence_6666", ":53"); got != defaultFastBypassWarmupMain {
		t.Fatalf("main warmup = %d, want %d", got, defaultFastBypassWarmupMain)
	}
}

func TestArgsInitSetsDefaultStaleRetry(t *testing.T) {
	args := &Args{}
	args.init()
	if args.FastCacheInternalTTL != 120 {
		t.Fatalf("internal ttl = %d, want 120", args.FastCacheInternalTTL)
	}
	if args.FastCacheStaleRetrySec != defaultStaleRefreshRetrySec {
		t.Fatalf("stale retry = %d, want %d", args.FastCacheStaleRetrySec, defaultStaleRefreshRetrySec)
	}
	if args.FastCacheStaleMaxSec != 300 {
		t.Fatalf("stale max = %d, want 300", args.FastCacheStaleMaxSec)
	}
	if args.FastCacheTTLMax != 600 {
		t.Fatalf("ttl max = %d, want 600", args.FastCacheTTLMax)
	}
	if args.FastListenerWorkers < 1 {
		t.Fatalf("listener workers = %d, want at least 1", args.FastListenerWorkers)
	}
}

func TestResolveFastCacheSlotsUsesBudget(t *testing.T) {
	responseSlots, ruleSlots := resolveFastCacheSlots(fastCacheConfig{memoryBudgetMB: 4})
	if responseSlots != 32768 || ruleSlots != 16384 {
		t.Fatalf("4MB slots = %d/%d, want 32768/16384", responseSlots, ruleSlots)
	}
	responseSlots, ruleSlots = resolveFastCacheSlots(fastCacheConfig{memoryBudgetMB: 16})
	if responseSlots != 131072 || ruleSlots != 65536 {
		t.Fatalf("16MB slots = %d/%d, want 131072/65536", responseSlots, ruleSlots)
	}
}

func TestNormalizeFastSlotCount(t *testing.T) {
	if got := normalizeFastSlotCount(1000, cacheWays, cacheSize*cacheWays); got != 1024 {
		t.Fatalf("normalized slots = %d, want 1024", got)
	}
	if got := normalizeFastSlotCount(1, cacheWays, cacheSize*cacheWays); got != cacheWays {
		t.Fatalf("normalized tiny slots = %d, want %d", got, cacheWays)
	}
	if got := normalizeFastSlotCount(cacheSize*cacheWays*2, cacheWays, cacheSize*cacheWays); got != cacheSize*cacheWays {
		t.Fatalf("normalized max slots = %d, want %d", got, cacheSize*cacheWays)
	}
}

func TestInferFastListenerWorkers(t *testing.T) {
	if got := inferFastListenerWorkers("sequence_requery", ":7766"); got != 1 {
		t.Fatalf("requery listener workers = %d, want 1", got)
	}
	if runtime.GOOS != "linux" {
		if got := inferFastListenerWorkers("sequence_6666", ":53"); got != 1 {
			t.Fatalf("non-linux main listener workers = %d, want 1", got)
		}
		return
	}
	got := inferFastListenerWorkers("sequence_6666", ":53")
	if got < 1 || got > defaultMainListenerWorkers {
		t.Fatalf("linux main listener workers = %d, want 1..%d", got, defaultMainListenerWorkers)
	}
}

func mustPack(t *testing.T, m *dns.Msg) []byte {
	t.Helper()
	b, err := m.Pack()
	if err != nil {
		t.Fatalf("pack dns msg: %v", err)
	}
	return b
}

func makeQuery(t *testing.T, name string, qtype uint16, id uint16) []byte {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id
	return mustPack(t, q)
}

func makeQueryWithOPT(t *testing.T, name string, qtype uint16, id uint16) []byte {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id
	q.SetEdns0(1232, false)
	return mustPack(t, q)
}

func makeAnswer(t *testing.T, name string, qtype uint16, id uint16, ttl uint32) []byte {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id

	r := new(dns.Msg)
	r.SetReply(q)
	var rr dns.RR
	var err error
	switch qtype {
	case dns.TypeAAAA:
		rr, err = dns.NewRR(fmt.Sprintf("%s %d IN AAAA 2001:db8::1", name, ttl))
	default:
		rr, err = dns.NewRR(fmt.Sprintf("%s %d IN A 1.1.1.1", name, ttl))
	}
	if err != nil {
		t.Fatalf("new rr: %v", err)
	}
	r.Answer = []dns.RR{rr}
	return mustPack(t, r)
}

func makeAnswerWithIP(t *testing.T, name string, qtype uint16, id uint16, ttl uint32, ip string) []byte {
	t.Helper()
	return makeAnswerWithIPNoTest(name, qtype, id, ttl, ip)
}

func mustPackNoTest(m *dns.Msg) []byte {
	b, err := m.Pack()
	if err != nil {
		panic(err)
	}
	return b
}

func makeQueryNoTest(name string, qtype uint16, id uint16) []byte {
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id
	return mustPackNoTest(q)
}

func makeAnswerNoTest(name string, qtype uint16, id uint16, ttl uint32) []byte {
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id

	r := new(dns.Msg)
	r.SetReply(q)
	var rr dns.RR
	var err error
	switch qtype {
	case dns.TypeAAAA:
		rr, err = dns.NewRR(fmt.Sprintf("%s %d IN AAAA 2001:db8::1", name, ttl))
	default:
		rr, err = dns.NewRR(fmt.Sprintf("%s %d IN A 1.1.1.1", name, ttl))
	}
	if err != nil {
		panic(err)
	}
	r.Answer = []dns.RR{rr}
	return mustPackNoTest(r)
}

func makeAnswerWithIPNoTest(name string, qtype uint16, id uint16, ttl uint32, ip string) []byte {
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id

	r := new(dns.Msg)
	r.SetReply(q)
	switch qtype {
	case dns.TypeAAAA:
		r.Answer = []dns.RR{&dns.AAAA{
			Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
			AAAA: net.ParseIP(ip),
		}}
	default:
		r.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
			A:   net.ParseIP(ip).To4(),
		}}
	}
	return mustPackNoTest(r)
}

func makeNXDomainWithSOA(t *testing.T, name string, id uint16, ttl uint32) []byte {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = id

	resp := new(dns.Msg)
	resp.SetRcode(q, dns.RcodeNameError)
	soa, err := dns.NewRR(fmt.Sprintf(
		"%s %d IN SOA ns1.%s hostmaster.%s 1 60 60 60 60",
		name, ttl, name, name,
	))
	if err != nil {
		t.Fatalf("new soa rr: %v", err)
	}
	resp.Ns = []dns.RR{soa}
	return mustPack(t, resp)
}

func findCollisionCandidates(t *testing.T, baseName string, qtype uint16, count int) []string {
	t.Helper()
	baseHash := fastQNameHashString(baseName, qtype)
	baseSlot := fastCacheBucketIndex(baseHash, cacheMask)
	candidates := make([]string, 0, count)
	for i := 0; len(candidates) < count && i < 2_000_000; i++ {
		name := fmt.Sprintf("c-%d.example.org.", i)
		hash := fastQNameHashString(name, qtype)
		if hash != baseHash && fastCacheBucketIndex(hash, cacheMask) == baseSlot {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) != count {
		t.Fatalf("failed to find %d collision candidates, got %d", count, len(candidates))
	}
	return candidates
}

func TestParseFastQuestion(t *testing.T) {
	q := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
	qname, qtype, end, ok := parseFastQuestion(len(q), q)
	if !ok {
		t.Fatal("expected parse success")
	}
	if qname != "example.org." {
		t.Fatalf("unexpected qname: %q", qname)
	}
	if qtype != dns.TypeA {
		t.Fatalf("unexpected qtype: %d", qtype)
	}
	if end != len(q) {
		t.Fatalf("unexpected q end: got %d want %d", end, len(q))
	}

	bad := append([]byte(nil), q...)
	bad[4], bad[5] = 0, 2 // qdcount = 2
	if _, _, _, ok := parseFastQuestion(len(bad), bad); ok {
		t.Fatal("expected parse failure for qdcount != 1")
	}
}

func TestFastCacheCollisionProtection(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:   time.Minute,
		ttlMax:        30,
		responseSlots: cacheSize * cacheWays,
		ruleSlots:     ruleSize * ruleWays,
	}, stats)

	baseName := "example.org."
	qtype := uint16(dns.TypeA)
	baseHash := fastQNameHashString(baseName, qtype)
	collisionName := findCollisionCandidates(t, baseName, qtype, 1)[0]

	fc.Store(collisionName, qtype, makeAnswer(t, collisionName, qtype, 0x2222, 30), "", false)

	buf := make([]byte, 512)
	query := makeQuery(t, baseName, qtype, 0x9999)
	copy(buf, query)

	action, _, _, _, _ := fc.GetOrUpdating(baseHash, buf, baseName, qtype, true)
	if action != server.FastActionContinue {
		t.Fatalf("expected cache miss due to collision protection, got action=%d", action)
	}
	if stats.cacheCollision.Load() == 0 {
		t.Fatal("expected cache collision metric to increase")
	}
}

func TestFastCacheKeepsConfiguredCollidingEntries(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)

	qtype := uint16(dns.TypeA)
	names := append([]string{"base.example.org."}, findCollisionCandidates(t, "base.example.org.", qtype, cacheWays)...)
	for i, name := range names[:cacheWays] {
		fc.Store(name, qtype, makeAnswer(t, name, qtype, uint16(0x1000+i), 30), name, false)
	}

	for _, name := range names[:cacheWays] {
		query := makeQuery(t, name, qtype, 0x9999)
		buf := make([]byte, 512)
		copy(buf, query)
		hash := fastQNameHashString(name, qtype)
		action, _, _, _, _ := fc.GetOrUpdating(hash, buf, name, qtype, true)
		if action != server.FastActionReply {
			t.Fatalf("expected cache hit for %s, got action=%d", name, action)
		}
	}

	fc.Store(names[cacheWays], qtype, makeAnswer(t, names[cacheWays], qtype, 0x3333, 30), names[cacheWays], false)
	if stats.cacheEviction.Load() == 0 {
		t.Fatal("expected cache eviction after bucket ways are full")
	}
}

func TestFastCacheLargeSequentialDomainSetDoesNotEvict(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)

	const total = 10_000
	const suffix = "msmcachetest.localtest."
	qtype := uint16(dns.TypeA)
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("rr-%06d.%s", i, suffix)
		resp := makeAnswer(t, name, qtype, uint16(i), 30)
		fc.Store(name, qtype, resp, "", false)
	}
	if got := fc.Len(); got < total-32 {
		t.Fatalf("cache len = %d, want at least %d", got, total-32)
	}
	if evictions := stats.cacheEviction.Load(); evictions > 32 {
		t.Fatalf("too many evictions for sequential domain set: %d", evictions)
	}

	hits := 0
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("rr-%06d.%s", i, suffix)
		query := makeQuery(t, name, qtype, 0x9999)
		buf := make([]byte, 512)
		copy(buf, query)
		hash := fastQNameHashString(name, qtype)
		action, _, _, _, _ := fc.GetOrUpdating(hash, buf, name, qtype, true)
		if action == server.FastActionReply {
			hits++
		}
	}
	if hits < total-32 {
		t.Fatalf("cache hits = %d, want at least %d", hits, total-32)
	}
}

func TestFastCacheStoreClampTTLAndPreserveTxID(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMin:      5,
		ttlMax:      30,
	}, stats)

	name := "ttl.example.org."
	qtype := uint16(dns.TypeA)
	resp := makeAnswer(t, name, qtype, 0x1111, 120)
	fc.Store(name, qtype, resp, "dset", false)

	query := makeQuery(t, name, qtype, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)

	h := fastQNameHashString(name, qtype)
	action, respLen, _, _, _ := fc.GetOrUpdating(h, buf, name, qtype, true)
	if action != server.FastActionReply {
		t.Fatalf("expected cache hit, got action=%d", action)
	}

	var out dns.Msg
	if err := out.Unpack(buf[:respLen]); err != nil {
		t.Fatalf("unpack cached response: %v", err)
	}
	if out.Id != 0x9999 {
		t.Fatalf("txid should come from request, got %x", out.Id)
	}
	if len(out.Answer) != 1 {
		t.Fatalf("unexpected answer count: %d", len(out.Answer))
	}
	if out.Answer[0].Header().Ttl != 30 {
		t.Fatalf("ttl should be clamped to 30, got %d", out.Answer[0].Header().Ttl)
	}

	// Lower-bound clamp
	respLow := makeAnswer(t, name, qtype, 0x1111, 1)
	fc.Store(name, qtype, respLow, "dset", false)
	buf = make([]byte, len(respLow))
	copy(buf, query)
	action, respLen, _, _, _ = fc.GetOrUpdating(h, buf, name, qtype, true)
	if action != server.FastActionReply {
		t.Fatalf("expected cache hit after re-store, got action=%d", action)
	}
	if err := out.Unpack(buf[:respLen]); err != nil {
		t.Fatalf("unpack low ttl response: %v", err)
	}
	if out.Answer[0].Header().Ttl != 5 {
		t.Fatalf("ttl should be clamped to 5, got %d", out.Answer[0].Header().Ttl)
	}
}

func TestFastCacheClampsAuthorityTTLForNegativeResponse(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMin:      5,
		ttlMax:      30,
	}, stats)

	name := "negative.example.org."
	qtype := uint16(dns.TypeA)
	resp := makeNXDomainWithSOA(t, name, 0x1111, 120)
	fc.Store(name, qtype, resp, "negative", false)

	query := makeQuery(t, name, qtype, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)
	hash := fastQNameHashString(name, qtype)
	action, respLen, _, _, _ := fc.GetOrUpdating(hash, buf, name, qtype, true)
	if action != server.FastActionReply {
		t.Fatalf("expected cache hit, got action=%d", action)
	}

	var out dns.Msg
	if err := out.Unpack(buf[:respLen]); err != nil {
		t.Fatalf("unpack negative response: %v", err)
	}
	if out.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got rcode=%d", out.Rcode)
	}
	if len(out.Ns) != 1 {
		t.Fatalf("expected authority SOA, got %d records", len(out.Ns))
	}
	if out.Ns[0].Header().Ttl != 30 {
		t.Fatalf("authority ttl should be clamped to 30, got %d", out.Ns[0].Header().Ttl)
	}
}

func TestFastCacheStoresAuditMetaOnlyWhenEnabled(t *testing.T) {
	name := "audit-cache.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x1111, 30)

	fcNoAudit := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, &fastStats{})
	if !fcNoAudit.Store(name, dns.TypeA, resp, "dset", false) {
		t.Fatal("expected no-audit cache store")
	}
	ptr := mustFastCacheItem(t, fcNoAudit, name, dns.TypeA)
	if ptr.audit.responseCode != "" || ptr.audit.answerCount != 0 || len(ptr.audit.answers) != 0 {
		t.Fatalf("no-audit cache item should not store audit details: %+v", ptr.audit)
	}

	fcAudit := newFastCache(fastCacheConfig{
		internalTTL:  time.Minute,
		ttlMax:       30,
		auditEnabled: true,
	}, &fastStats{})
	if !fcAudit.Store(name, dns.TypeA, resp, "dset", false) {
		t.Fatal("expected audit cache store")
	}
	ptr = mustFastCacheItem(t, fcAudit, name, dns.TypeA)
	if ptr.audit.responseCode != "NOERROR" || ptr.audit.answerCount != 1 {
		t.Fatalf("unexpected audit header: %+v", ptr.audit)
	}
	if len(ptr.audit.answers) != 1 || ptr.audit.answers[0].Type != "A" {
		t.Fatalf("unexpected audit answers: %+v", ptr.audit.answers)
	}
}

func TestFastCacheRespectsFakeIPToggle(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)

	name := "fake.example."
	qtype := uint16(dns.TypeA)
	resp := makeAnswerWithIP(t, name, qtype, 0x1111, 30, "28.1.2.3")
	fc.Store(name, qtype, resp, "fakeip", true)

	query := makeQuery(t, name, qtype, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)
	h := fastQNameHashString(name, qtype)

	action, _, _, _, _ := fc.GetOrUpdating(h, buf, name, qtype, false)
	if action != server.FastActionContinue {
		t.Fatalf("expected fakeip cache to be bypassed when disabled, got %d", action)
	}

	copy(buf, query)
	action, respLen, _, _, _ := fc.GetOrUpdating(h, buf, name, qtype, true)
	if action != server.FastActionReply {
		t.Fatalf("expected fakeip cache hit when enabled, got %d", action)
	}
	var out dns.Msg
	if err := out.Unpack(buf[:respLen]); err != nil {
		t.Fatalf("unpack fakeip response: %v", err)
	}
	if out.Id != 0x9999 {
		t.Fatalf("expected txid from request, got %x", out.Id)
	}
}

func TestIsFakeIPResponse(t *testing.T) {
	if !isFakeIPResponse(makeAnswerWithIP(t, "fake.example.", dns.TypeA, 0x1111, 30, "30.2.3.4")) {
		t.Fatal("expected fake response to be detected")
	}
	if isFakeIPResponse(makeAnswerWithIP(t, "real.example.", dns.TypeA, 0x1111, 30, "1.1.1.1")) {
		t.Fatal("expected real response not to be detected as fake")
	}
}

func TestFastCacheHonorsConfiguredStaleRetryWindow(t *testing.T) {
	name := "retry.example."
	qtype := uint16(dns.TypeA)
	hash := fastQNameHashString(name, qtype)
	resp := makeAnswer(t, name, qtype, 0x1111, 30)

	fcSlow := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		staleRetry:  time.Hour,
	}, &fastStats{})
	fcSlow.Store(name, qtype, resp, "", false)
	item := mustFastCacheItem(t, fcSlow, name, qtype)
	atomic.StoreInt64(&item.expire, time.Now().Add(-30*time.Second).Unix())
	atomic.StoreUint32(&item.updating, 1)

	buf := make([]byte, len(resp))
	copy(buf, makeQuery(t, name, qtype, 0x9999))
	_, _, _, _, staleRefresh := fcSlow.GetOrUpdating(hash, buf, name, qtype, true)
	if staleRefresh {
		t.Fatal("expected stale refresh to stay suppressed before configured retry window")
	}

	fcFast := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		staleRetry:  time.Second,
	}, &fastStats{})
	fcFast.Store(name, qtype, resp, "", false)
	item = mustFastCacheItem(t, fcFast, name, qtype)
	atomic.StoreInt64(&item.expire, time.Now().Add(-30*time.Second).Unix())
	atomic.StoreUint32(&item.updating, 1)

	buf = make([]byte, len(resp))
	copy(buf, makeQuery(t, name, qtype, 0x9999))
	_, _, _, _, staleRefresh = fcFast.GetOrUpdating(hash, buf, name, qtype, true)
	if !staleRefresh {
		t.Fatal("expected stale refresh after retry window elapsed")
	}
}

func TestFastCacheStopsServingStaleAfterMaxWindow(t *testing.T) {
	name := "stale-max.example."
	qtype := uint16(dns.TypeA)
	hash := fastQNameHashString(name, qtype)
	resp := makeAnswer(t, name, qtype, 0x1111, 30)

	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		staleRetry:  time.Second,
		staleMax:    5 * time.Second,
	}, &fastStats{})
	fc.Store(name, qtype, resp, "", false)
	item := mustFastCacheItem(t, fc, name, qtype)
	atomic.StoreInt64(&item.expire, time.Now().Add(-10*time.Second).Unix())
	atomic.StoreUint32(&item.updating, 1)

	buf := make([]byte, len(resp))
	copy(buf, makeQuery(t, name, qtype, 0x9999))
	action, _, _, _, staleRefresh := fc.GetOrUpdating(hash, buf, name, qtype, true)
	if action != server.FastActionContinue {
		t.Fatalf("expected stale item to fall through after max window, got action=%d", action)
	}
	if staleRefresh {
		t.Fatal("expected stale refresh flag to be disabled after max stale window")
	}
}

func TestFastCacheDomainSetTTLOverrideKeepsHighChurnHot(t *testing.T) {
	name := "content.steamcontent.com."
	qtype := uint16(dns.TypeA)
	hash := fastQNameHashString(name, qtype)
	resp := makeAnswer(t, name, qtype, 0x1111, 30)

	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Second,
		staleRetry:  time.Hour,
		staleMax:    time.Second,
		domainSetTTL: []fastDomainSetTTLPolicy{{
			Sets:        []string{"高变化域名"},
			InternalTTL: 43200,
			StaleMax:    43200,
		}},
	}, &fastStats{})
	if !fc.storeWithRuleRevision(name, qtype, resp, "国外分流|高变化域名", false, false, 0, fastRuleRevision{}) {
		t.Fatal("expected high-churn response to be stored")
	}

	item := mustFastCacheItem(t, fc, name, qtype)
	if remaining := time.Until(time.Unix(atomic.LoadInt64(&item.expire), 0)); remaining < 11*time.Hour {
		t.Fatalf("expected high-churn internal ttl override around 12h, remaining=%s", remaining)
	}

	atomic.StoreInt64(&item.expire, time.Now().Add(-10*time.Second).Unix())
	atomic.StoreUint32(&item.updating, 1)
	buf := make([]byte, len(resp))
	copy(buf, makeQuery(t, name, qtype, 0x9999))
	action, _, _, _, staleRefresh := fc.GetOrUpdating(hash, buf, name, qtype, true)
	if action != server.FastActionReply {
		t.Fatalf("expected high-churn stale response within domain-set stale window, got action=%d", action)
	}
	if staleRefresh {
		t.Fatal("expected stale refresh flag to stay false while another refresh is updating")
	}
}

func TestFastCachePurgeDomainsAndFlush(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)

	fc.Store("purge-fast.example.", dns.TypeA, makeAnswer(t, "purge-fast.example.", dns.TypeA, 0x1111, 30), "direct", false)
	fc.Store("keep-fast.example.", dns.TypeA, makeAnswer(t, "keep-fast.example.", dns.TypeA, 0x2222, 30), "direct", false)

	if got := fc.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}

	if purged := fc.PurgeDomains([]string{"purge-fast.example"}, nil); purged != 1 {
		t.Fatalf("PurgeDomains() = %d, want 1", purged)
	}
	if got := fc.Len(); got != 1 {
		t.Fatalf("Len() after purge = %d, want 1", got)
	}

	fc.Flush()
	if got := fc.Len(); got != 0 {
		t.Fatalf("Len() after flush = %d, want 0", got)
	}
}

func TestFastCacheNormalizesStoredQName(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)

	resp := makeAnswer(t, "Mixed.Example.", dns.TypeA, 0x1111, 30)
	fc.Store("Mixed.Example.", dns.TypeA, resp, "direct", false)

	query := makeQuery(t, "mixed.example.", dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)
	question, ok := parseFastQuestionMeta(len(query), buf)
	if !ok {
		t.Fatal("parse fast question")
	}
	action, respLen, _, _, _ := fc.getOrUpdatingWire(question.hash, buf, question.qnameWire(buf), dns.TypeA, true, fastRuleRevision{})
	if action != server.FastActionReply {
		t.Fatalf("expected normalized qname cache hit, got action=%d", action)
	}
	var out dns.Msg
	if err := out.Unpack(buf[:respLen]); err != nil {
		t.Fatalf("unpack response: %v", err)
	}
	if out.Id != 0x9999 {
		t.Fatalf("expected txid from request, got %x", out.Id)
	}
	if purged := fc.PurgeDomains([]string{"MIXED.EXAMPLE"}, nil); purged != 1 {
		t.Fatalf("PurgeDomains normalized name = %d, want 1", purged)
	}
}

func TestUdpServerSnapshotCacheStats(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:      5 * time.Second,
		staleRetry:       10 * time.Second,
		staleMax:         30 * time.Second,
		ttlMin:           1,
		ttlMax:           30,
		bypassDomainSets: []string{"DDNS域名"},
	}, stats)

	name := "stats.example."
	qtype := uint16(dns.TypeA)
	resp := makeAnswer(t, name, qtype, 0x1111, 30)
	fc.Store(name, qtype, resp, "stats", false)

	query := makeQuery(t, name, qtype, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)
	hash := fastQNameHashString(name, qtype)
	stats.cacheLookup.Add(1)
	action, _, _, _, _ := fc.GetOrUpdating(hash, buf, name, qtype, true)
	if action != server.FastActionReply {
		t.Fatalf("expected cache hit before snapshot, got action=%d", action)
	}

	snapshot := (&UdpServer{fc: fc}).SnapshotCacheStats()
	if snapshot.Name != "UDP fast path" {
		t.Fatalf("unexpected snapshot name: %q", snapshot.Name)
	}
	if snapshot.BackendSize != 1 {
		t.Fatalf("BackendSize = %d, want 1", snapshot.BackendSize)
	}
	if snapshot.Counters["query_total"] != 1 || snapshot.Counters["hit_total"] != 1 {
		t.Fatalf("unexpected generic counters: %+v", snapshot.Counters)
	}
	if snapshot.Counters["cache_store"] != 1 || snapshot.Counters["cache_hit"] != 1 {
		t.Fatalf("unexpected fast counters: %+v", snapshot.Counters)
	}
	if snapshot.Config["runtime_cache_kind"] != "udp_fast" {
		t.Fatalf("unexpected runtime cache kind: %+v", snapshot.Config)
	}
	if snapshot.Config["internal_ttl"] != 5 || snapshot.Config["stale_retry_seconds"] != 10 || snapshot.Config["stale_max_seconds"] != 30 {
		t.Fatalf("unexpected ttl config: %+v", snapshot.Config)
	}
}

type testSwitchPlugin struct {
	value string
}

func (s testSwitchPlugin) GetValue() string {
	return s.value
}

type testRevisionSwitchPlugin struct {
	value    string
	revision uint64
}

func (s testRevisionSwitchPlugin) GetValue() string {
	return s.value
}

func (s testRevisionSwitchPlugin) ValueCode() uint64 {
	switch s.value {
	case "on":
		return fastSwitchValueOn
	case "off":
		return fastSwitchValueOff
	case "all":
		return fastSwitchValueAll
	case "blacklist":
		return fastSwitchValueBlacklist
	case "whitelist":
		return fastSwitchValueWhitelist
	default:
		return fastSwitchValueUnknown
	}
}

type testDomainMapperPlugin struct {
	marks []uint8
	tag   string
	match bool
}

func (m testDomainMapperPlugin) FastMatch(qname string) ([]uint8, string, bool) {
	return m.marks, m.tag, m.match
}

type testRevisionDomainMapperPlugin struct {
	testDomainMapperPlugin
	revision string
	calls    *atomic.Uint64
}

func (m testRevisionDomainMapperPlugin) FastMatch(qname string) ([]uint8, string, bool) {
	if m.calls != nil {
		m.calls.Add(1)
	}
	return m.testDomainMapperPlugin.FastMatch(qname)
}

func (m testRevisionDomainMapperPlugin) CacheRevision() string {
	return m.revision
}

type testNumericRevisionDomainMapperPlugin struct {
	testDomainMapperPlugin
	revision uint64
	calls    *atomic.Uint64
}

func (m testNumericRevisionDomainMapperPlugin) FastMatch(qname string) ([]uint8, string, bool) {
	if m.calls != nil {
		m.calls.Add(1)
	}
	return m.testDomainMapperPlugin.FastMatch(qname)
}

func (m testNumericRevisionDomainMapperPlugin) CacheRevision() string {
	return fmt.Sprintf("%d", m.revision)
}

func (m testNumericRevisionDomainMapperPlugin) CacheRevisionUint64() uint64 {
	return m.revision
}

type testCacheRevisionPlugin struct {
	revision string
}

func (p testCacheRevisionPlugin) CacheRevision() string {
	return p.revision
}

type testIPSetPlugin struct {
	match bool
}

func (p testIPSetPlugin) Match(addr netip.Addr) bool {
	return p.match
}

type countingIPSetPlugin struct {
	match    bool
	calls    atomic.Uint64
	revision atomic.Uint64
}

func (p *countingIPSetPlugin) Match(addr netip.Addr) bool {
	p.calls.Add(1)
	return p.match
}

func (p *countingIPSetPlugin) CacheRevisionUint64() uint64 {
	return p.revision.Load()
}

type pooledHandler struct {
	payload []byte
	called  chan struct{}
}

func (h pooledHandler) Handle(_ context.Context, _ *dns.Msg, _ server.QueryMeta, _ func(*dns.Msg) (*[]byte, error)) *[]byte {
	buf := pool.GetBuf(len(h.payload))
	copy(*buf, h.payload)
	if h.called != nil {
		select {
		case h.called <- struct{}{}:
		default:
		}
	}
	return buf
}

func TestBuildFastBypassSetsMapperMarksOnlyOnMatch(t *testing.T) {
	tests := []struct {
		name        string
		dm          testDomainMapperPlugin
		wantMarkSet bool
	}{
		{
			name: "miss does not set mark",
			dm: testDomainMapperPlugin{
				match: false,
			},
			wantMarkSet: false,
		},
		{
			name: "hit sets returned mark",
			dm: testDomainMapperPlugin{
				marks: []uint8{17},
				tag:   "命中",
				match: true,
			},
			wantMarkSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{
				"udp_fast_path":    testSwitchPlugin{value: "on"},
				"unified_matcher1": tt.dm,
			})
			bp := coremain.NewBP("udp_test", m)
			fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
				internalTTL: time.Minute,
			}, &fastStats{}), &fastStats{}, 0)

			req := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
			action, _, marks, _, matched, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
			if action != server.FastActionContinue {
				t.Fatalf("expected continue action, got %d", action)
			}
			if staleRefresh {
				t.Fatal("unexpected stale refresh flag on normal mapper match test")
			}
			if matched != tt.dm.match {
				t.Fatalf("matched = %v, want %v", matched, tt.dm.match)
			}

			gotMarkSet := (marks & (uint64(1) << 17)) != 0
			if gotMarkSet != tt.wantMarkSet {
				t.Fatalf("mark 17 set = %v, want %v, marks=%064b", gotMarkSet, tt.wantMarkSet, marks)
			}
		})
	}
}

func TestBuildFastBypassRejectsByRuleMark(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"block_response":   testSwitchPlugin{value: "on"},
		"unified_matcher1": testDomainMapperPlugin{marks: []uint8{1}, match: true},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQuery(t, "blocked.example.", dns.TypeA, 0x1234)
	buf := append([]byte(nil), req...)
	action, respLen, marks, _, _, staleRefresh := fastBypass(len(buf), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected fast reject reply, got %d", action)
	}
	if staleRefresh {
		t.Fatal("reject path should not request stale refresh")
	}
	if respLen != len(req) {
		t.Fatalf("unexpected reply length: got %d want %d", respLen, len(req))
	}
	if marks != 0 {
		t.Fatalf("reject path should not return pre marks, got %064b", marks)
	}
	if gotRcode := buf[3] & 0x0F; gotRcode != 3 {
		t.Fatalf("expected NXDOMAIN-style reject rcode=3, got %d", gotRcode)
	}
}

func TestBuildFastBypassRejectClearsHeaderCounts(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"block_response":   testSwitchPlugin{value: "on"},
		"unified_matcher1": testDomainMapperPlugin{marks: []uint8{1}, match: true},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQueryWithOPT(t, "blocked.example.", dns.TypeA, 0x1234)
	buf := append([]byte(nil), req...)
	action, respLen, _, _, _, _ := fastBypass(len(buf), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected fast reject reply, got %d", action)
	}
	if respLen >= len(req) {
		t.Fatalf("expected reject response to drop extra section, got respLen=%d reqLen=%d", respLen, len(req))
	}
	if got := binary.BigEndian.Uint16(buf[6:8]); got != 0 {
		t.Fatalf("answer count should be zero, got %d", got)
	}
	if got := binary.BigEndian.Uint16(buf[8:10]); got != 0 {
		t.Fatalf("ns count should be zero, got %d", got)
	}
	if got := binary.BigEndian.Uint16(buf[10:12]); got != 0 {
		t.Fatalf("extra count should be zero, got %d", got)
	}
}

func TestBuildFastBypassClientIPWhitelistFastMarks(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":       testSwitchPlugin{value: "on"},
		"client_proxy_mode":   testSwitchPlugin{value: "whitelist"},
		"client_ip_whitelist": testIPSetPlugin{match: false},
		"client_ip_blacklist": testIPSetPlugin{match: false},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
	action, _, marks, _, _, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("192.168.5.13:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected continue action, got %d", action)
	}
	if staleRefresh {
		t.Fatal("client_ip fast marks should not request stale refresh")
	}
	if (marks & (uint64(1) << 48)) == 0 {
		t.Fatalf("expected fast mark 48 to indicate client_ip fast-checked, got %064b", marks)
	}
	if (marks & (uint64(1) << 39)) == 0 {
		t.Fatalf("expected fast mark 39 for direct-path branch, got %064b", marks)
	}
}

func TestBuildFastBypassClientIPWhitelistMatchKeepsProxyPath(t *testing.T) {
	whitelist := &countingIPSetPlugin{match: true}
	blacklist := &countingIPSetPlugin{}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":       testSwitchPlugin{value: "on"},
		"client_proxy_mode":   testSwitchPlugin{value: "whitelist"},
		"client_ip_whitelist": whitelist,
		"client_ip_blacklist": blacklist,
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
	addr := netip.MustParseAddrPort("192.168.5.13:5353")
	for i := 0; i < 3; i++ {
		action, _, marks, _, _, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), addr)
		if action != server.FastActionContinue {
			t.Fatalf("expected continue action, got %d", action)
		}
		if staleRefresh {
			t.Fatal("client_ip fast marks should not request stale refresh")
		}
		if (marks & (uint64(1) << 48)) == 0 {
			t.Fatalf("expected fast mark 48 to indicate client_ip fast-checked, got %064b", marks)
		}
		if (marks & (uint64(1) << 39)) != 0 {
			t.Fatalf("whitelisted client should keep proxy path, got %064b", marks)
		}
	}
	if got := whitelist.calls.Load(); got != 1 {
		t.Fatalf("expected repeated LAN client whitelist checks to be cached, got %d calls", got)
	}
	if got := blacklist.calls.Load(); got != 0 {
		t.Fatalf("whitelist mode should not check blacklist, got %d calls", got)
	}
}

func TestBuildFastBypassClientIPWhitelistKeepsLoopbackOnProxyPath(t *testing.T) {
	whitelist := &countingIPSetPlugin{}
	blacklist := &countingIPSetPlugin{}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":       testSwitchPlugin{value: "on"},
		"client_proxy_mode":   testSwitchPlugin{value: "whitelist"},
		"client_ip_whitelist": whitelist,
		"client_ip_blacklist": blacklist,
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
	for _, addr := range []string{"127.0.0.1:5353", "[::1]:5353"} {
		t.Run(addr, func(t *testing.T) {
			action, _, marks, _, _, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort(addr))
			if action != server.FastActionContinue {
				t.Fatalf("expected continue action, got %d", action)
			}
			if staleRefresh {
				t.Fatal("loopback client_ip fast marks should not request stale refresh")
			}
			if (marks & (uint64(1) << 48)) == 0 {
				t.Fatalf("expected fast mark 48 to indicate client_ip fast-checked, got %064b", marks)
			}
			if (marks & (uint64(1) << 39)) != 0 {
				t.Fatalf("loopback should keep proxy path in whitelist mode, got %064b", marks)
			}
		})
	}
	if got := whitelist.calls.Load(); got != 0 {
		t.Fatalf("loopback should skip whitelist lookup, got %d calls", got)
	}
	if got := blacklist.calls.Load(); got != 0 {
		t.Fatalf("loopback should skip blacklist lookup, got %d calls", got)
	}
}

func TestBuildFastBypassClientIPBlacklistFastMarks(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":       testSwitchPlugin{value: "on"},
		"client_proxy_mode":   testSwitchPlugin{value: "blacklist"},
		"client_ip_whitelist": testIPSetPlugin{match: false},
		"client_ip_blacklist": testIPSetPlugin{match: true},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
	action, _, marks, _, _, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("192.168.5.13:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected continue action, got %d", action)
	}
	if staleRefresh {
		t.Fatal("client_ip fast marks should not request stale refresh")
	}
	if (marks & (uint64(1) << 48)) == 0 {
		t.Fatalf("expected fast mark 48 to indicate client_ip fast-checked, got %064b", marks)
	}
	if (marks & (uint64(1) << 39)) == 0 {
		t.Fatalf("expected fast mark 39 for direct-path branch, got %064b", marks)
	}
}

func TestBuildFastBypassCacheHitReturnsReply(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "cached.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "缓存命中", false)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, respLen, marks, dset, _, staleRefresh := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected cache reply, got %d", action)
	}
	if staleRefresh {
		t.Fatal("cache hit should not request stale refresh")
	}
	if marks != 0 {
		t.Fatalf("cache hit should not return pre marks, got %064b", marks)
	}
	if dset != "缓存命中" {
		t.Fatalf("unexpected domain set: %q", dset)
	}
	var out dns.Msg
	if err := out.Unpack(buf[:respLen]); err != nil {
		t.Fatalf("unpack cached reply: %v", err)
	}
	if out.Id != 0x9999 {
		t.Fatalf("expected txid from request, got %x", out.Id)
	}
}

func TestBuildFastAuditLogFromWireParsesCacheHit(t *testing.T) {
	name := "cached.example."
	req := makeQuery(t, name, dns.TypeA, 0x9999)
	resp := makeAnswerWithIP(t, name, dns.TypeA, 0x9999, 30, "1.2.3.4")
	question, ok := parseFastQuestionMeta(len(req), req)
	if !ok {
		t.Fatal("parse fast question")
	}

	start := time.Now().Add(-2 * time.Millisecond)
	log, _, ok := buildFastAuditLogFromWire(
		start,
		question,
		resp,
		netip.MustParseAddrPort("192.0.2.10:5353"),
		&fastAuditClientIPCache{},
		"缓存命中",
		coremain.AuditCacheFastHit,
		fastAuditResponseMetaFromPayload("cached.example", resp),
	)
	if !ok {
		t.Fatal("build fast audit log")
	}
	if log.ClientIP != "192.0.2.10" {
		t.Fatalf("client ip = %q", log.ClientIP)
	}
	if log.QueryName != "cached.example" || log.QueryType != "A" || log.QueryClass != "IN" {
		t.Fatalf("unexpected query metadata: name=%q type=%q class=%q", log.QueryName, log.QueryType, log.QueryClass)
	}
	if log.ResponseCode != "NOERROR" || log.AnswerCount != 1 {
		t.Fatalf("unexpected response metadata: code=%q answers=%d", log.ResponseCode, log.AnswerCount)
	}
	if len(log.Answers) != 1 || log.Answers[0].Type != "A" || log.Answers[0].TTL != 30 || log.Answers[0].Data != "1.2.3.4" {
		t.Fatalf("unexpected answers: %+v", log.Answers)
	}
	if log.DomainSetRaw != "缓存命中" || log.CacheStatus != coremain.AuditCacheFastHit || log.Transport != "udp" {
		t.Fatalf("unexpected audit tags: domain_set=%q cache=%q transport=%q", log.DomainSetRaw, log.CacheStatus, log.Transport)
	}
	if log.DurationMs <= 0 {
		t.Fatalf("duration should be positive, got %.6f", log.DurationMs)
	}
}

func TestCollectFastAuditFromWireRecordsRealtimeOverview(t *testing.T) {
	oldCollector := coremain.GlobalAuditCollector
	collector := coremain.NewAuditCollector(coremain.AuditSettings{Enabled: true}, t.TempDir())
	collector.StartWorker()
	coremain.GlobalAuditCollector = collector
	t.Cleanup(func() {
		collector.StopWorker()
		coremain.GlobalAuditCollector = oldCollector
	})

	name := "overview-cache.example."
	req := makeQuery(t, name, dns.TypeA, 0x9999)
	resp := makeAnswer(t, name, dns.TypeA, 0x9999, 30)
	question, ok := parseFastQuestionMeta(len(req), req)
	if !ok {
		t.Fatal("parse fast question")
	}

	collectFastAuditFromWire(
		true,
		time.Now(),
		question,
		resp,
		netip.MustParseAddrPort("192.0.2.11:5353"),
		&fastAuditClientIPCache{},
		"缓存命中",
		coremain.AuditCacheFastHit,
		fastAuditResponseMetaFromPayload("overview-cache.example", resp),
	)

	var overview coremain.AuditOverview
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		overview = collector.GetOverview(60)
		if overview.QueryCount == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if overview.QueryCount != 1 {
		t.Fatalf("query count = %d, want 1", overview.QueryCount)
	}
	if overview.CacheHitCount != 1 {
		t.Fatalf("cache hit count = %d, want 1", overview.CacheHitCount)
	}
	if overview.AverageDurationMs < 0 || overview.AverageDurationMs > 10 {
		t.Fatalf("average duration looks wrong: %.6f", overview.AverageDurationMs)
	}
}

func TestBuildFastBypassCacheHitSkipsMapperWhenRevisionMatches(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "revision-hit.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, false, 0, "rev1") {
		t.Fatal("expected cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"unified_matcher1": testRevisionDomainMapperPlugin{revision: "rev1", calls: &calls},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected cache reply, got %d", action)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected revision-matched cache hit to skip mapper, got %d calls", calls.Load())
	}
}

func TestBuildFastBypassCacheHitUsesNumericRevisionWithoutMapper(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "numeric-revision-hit.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithRuleRevision(name, dns.TypeA, resp, "", false, false, 0, fastRuleRevision{
		domainMapper: fastNumericRevisionValue(7),
	}) {
		t.Fatal("expected cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{revision: 7, calls: &calls},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected cache reply, got %d", action)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected numeric revision cache hit to skip mapper, got %d calls", calls.Load())
	}
}

func TestBuildFastBypassCacheHitRefreshesWhenRevisionChanges(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "revision-miss.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, false, 0, "rev1") {
		t.Fatal("expected cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"unified_matcher1": testRevisionDomainMapperPlugin{revision: "rev2", calls: &calls},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected stale revision to continue to full chain, got %d", action)
	}
	if calls.Load() == 0 {
		t.Fatal("expected mapper to run after revision mismatch")
	}
}

func TestBuildFastBypassDoesNotReuseStaleDomainSetAfterMapperRevisionChanges(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:      time.Minute,
		ttlMax:           30,
		bypassDomainSets: []string{"高变化域名"},
	}, stats)
	name := "content.steamcontent.com."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithRuleRevision(name, dns.TypeA, resp, "记忆直连", false, false, 1<<11, fastRuleRevision{
		domainMapper: fastNumericRevisionValue(1),
	}) {
		t.Fatal("expected stale response cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{
			testDomainMapperPlugin: testDomainMapperPlugin{marks: []uint8{17}, tag: "高变化域名", match: true},
			revision:               2,
			calls:                  &calls,
		},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, marks, dset, matched, staleRefresh := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected stale response to continue after high-churn rematch, got %d", action)
	}
	if staleRefresh {
		t.Fatal("high-churn rematch must not request stale refresh from stale response cache")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected mapper to rerun after stale domain_mapper revision, got %d calls", calls.Load())
	}
	if dset != "高变化域名" || !matched {
		t.Fatalf("expected high-churn rematch, dset=%q matched=%v", dset, matched)
	}
	if (marks & (1 << 17)) == 0 {
		t.Fatalf("expected high-churn route mark to be set, got %064b", marks)
	}
	if stats.bypassCacheReply.Load() != 0 {
		t.Fatalf("stale high-churn response must not be served as fast hit, got %d", stats.bypassCacheReply.Load())
	}
}

func TestBuildFastBypassClientDirectUsesSeparateCacheVariant(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "client-direct-cache.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	ruleRevision := fastRuleRevision{
		domainMapper: fastNumericRevisionValue(7),
		control:      fastControlRevisionFromSwitches(fastSwitchValue{}, fastSwitchValue{}, fastSwitchValue{}, fastSwitchValue{}),
	}
	if !fc.storeWithRuleRevision(name, dns.TypeA, resp, "记忆直连", false, true, 1<<39, ruleRevision) {
		t.Fatal("expected client-direct cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":       testSwitchPlugin{value: "on"},
		"client_proxy_mode":   testSwitchPlugin{value: "whitelist"},
		"client_ip_whitelist": testIPSetPlugin{match: false},
		"unified_matcher1":    testNumericRevisionDomainMapperPlugin{revision: 7, calls: &calls},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	directBuf := make([]byte, len(resp))
	copy(directBuf, req)
	action, _, _, dset, _, staleRefresh := fastBypass(len(req), directBuf, netip.MustParseAddrPort("192.168.20.171:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected client-direct cache reply, got %d", action)
	}
	if staleRefresh {
		t.Fatal("client-direct cache hit should not request stale refresh")
	}
	if dset != "记忆直连" {
		t.Fatalf("unexpected domain set: %q", dset)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected client-direct cache hit to skip mapper, got %d calls", calls.Load())
	}

	loopbackBuf := make([]byte, len(resp))
	copy(loopbackBuf, req)
	action, _, _, _, _, _ = fastBypass(len(req), loopbackBuf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action == server.FastActionReply {
		t.Fatal("ordinary client must not reuse client-direct cache variant")
	}
}

func TestBuildFastBypassCacheHitRefreshesWhenRewriteRevisionChanges(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "rewrite-revision-miss.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, false, 0, "dm1|rewrite1") {
		t.Fatal("expected cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"unified_matcher1": testRevisionDomainMapperPlugin{revision: "dm1", calls: &calls},
		"rewrite":          testCacheRevisionPlugin{revision: "rewrite2"},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected rewrite revision mismatch to continue to full chain, got %d", action)
	}
	if calls.Load() == 0 {
		t.Fatal("expected mapper to run after rewrite revision mismatch")
	}
}

func TestBuildFastBypassCacheHitRefreshesWhenControlSwitchChanges(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "control-switch-miss.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithRuleRevision(name, dns.TypeA, resp, "direct", false, false, 0, fastRuleRevision{
		domainMapper: fastNumericRevisionValue(7),
		control:      fastControlRevisionFromSwitches(fastSwitchValue{}, fastSwitchValue{}, fastSwitchValue{}, newFastSwitchValue(testSwitchPlugin{value: "realip"})),
	}) {
		t.Fatal("expected cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"cn_answer_mode":   testSwitchPlugin{value: "fakeip"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{revision: 7, calls: &calls},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected control switch mismatch to continue to full chain, got %d", action)
	}
	if calls.Load() == 0 {
		t.Fatal("expected mapper to run after control switch mismatch")
	}
}

func TestBuildFastBypassCachedRuleFlagsHonorSwitches(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "cached-block.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithRuleRevision(name, dns.TypeA, resp, "", false, false, 1<<1, fastRuleRevision{
		domainMapper: fastTextRevisionValue("rev1"),
		control:      fastControlRevisionFromSwitches(fastSwitchValue{}, newFastSwitchValue(testSwitchPlugin{value: "on"}), fastSwitchValue{}, fastSwitchValue{}),
	}) {
		t.Fatal("expected cache store")
	}

	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"block_response":   testSwitchPlugin{value: "on"},
		"unified_matcher1": testRevisionDomainMapperPlugin{revision: "rev1", calls: &calls},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, respLen, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected cached rule reject, got %d", action)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected cached rule flags to skip mapper, got %d calls", calls.Load())
	}
	if respLen != len(req) {
		t.Fatalf("unexpected reject length: got %d want %d", respLen, len(req))
	}
	if gotRcode := buf[3] & 0x0F; gotRcode != 3 {
		t.Fatalf("expected NXDOMAIN-style reject rcode=3, got %d", gotRcode)
	}
}

func TestBuildFastBypassSkipsCacheForBypassDomainSet(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:      time.Minute,
		ttlMax:           30,
		bypassDomainSets: []string{"DDNS域名"},
	}, stats)
	name := "ddns-fast-bypass.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.Store(name, dns.TypeA, resp, "旧名单", false) {
		t.Fatal("expected old non-bypass entry to be stored")
	}

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"unified_matcher1": testDomainMapperPlugin{tag: "DDNS域名", match: true},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, marks, dset, matched, staleRefresh := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected bypass domain set to continue, got %d", action)
	}
	if staleRefresh {
		t.Fatal("bypass domain set should not request stale refresh")
	}
	if marks != 0 {
		t.Fatalf("expected no marks for matched DDNS test plugin, got %064b", marks)
	}
	if dset != "DDNS域名" || !matched {
		t.Fatalf("unexpected mapper result: dset=%q matched=%v", dset, matched)
	}
	if stats.cacheLookup.Load() != 0 {
		t.Fatalf("expected bypass domain set to skip fast cache lookup, got %d", stats.cacheLookup.Load())
	}
}

func TestBuildFastBypassCachesRuleMetaForBypassDomainSet(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:      time.Minute,
		ttlMax:           30,
		bypassDomainSets: []string{"DDNS域名"},
	}, stats)
	name := "ddns-rule-cache.example."
	var calls atomic.Uint64
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{
			testDomainMapperPlugin: testDomainMapperPlugin{tag: "DDNS域名", match: true},
			revision:               7,
			calls:                  &calls,
		},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)
	addr := netip.MustParseAddrPort("127.0.0.1:5353")

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := append([]byte(nil), req...)
	action, _, _, dset, matched, _ := fastBypass(len(req), buf, addr)
	if action != server.FastActionContinue || dset != "DDNS域名" || !matched {
		t.Fatalf("first bypass result action=%d dset=%q matched=%v", action, dset, matched)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected first call to run mapper once, got %d", calls.Load())
	}
	if stats.cacheLookup.Load() != 1 {
		t.Fatalf("expected first call to try response cache once before mapper, got %d", stats.cacheLookup.Load())
	}

	buf = append([]byte(nil), req...)
	action, _, _, dset, matched, _ = fastBypass(len(req), buf, addr)
	if action != server.FastActionContinue || dset != "DDNS域名" || !matched {
		t.Fatalf("second bypass result action=%d dset=%q matched=%v", action, dset, matched)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected rule-meta hit to skip mapper, got %d calls", calls.Load())
	}
	if stats.cacheLookup.Load() != 2 {
		t.Fatalf("expected rule-meta hit to keep response cache miss cheap before skipping mapper, got %d", stats.cacheLookup.Load())
	}
}

func TestBuildFastBypassRuleMetaRefreshesWhenMapperRevisionChanges(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "rule-revision-refresh.example."
	addr := netip.MustParseAddrPort("127.0.0.1:5353")

	var calls1 atomic.Uint64
	m1 := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{
			testDomainMapperPlugin: testDomainMapperPlugin{marks: []uint8{17}, tag: "rev1", match: true},
			revision:               1,
			calls:                  &calls1,
		},
	})
	fastBypass1 := buildFastBypass(coremain.NewBP("udp_test_1", m1), fc, stats, 0)
	req := makeQuery(t, name, dns.TypeA, 0x9999)
	action, _, marks, dset, matched, _ := fastBypass1(len(req), append([]byte(nil), req...), addr)
	if action != server.FastActionContinue || dset != "rev1" || !matched || (marks&(1<<17)) == 0 {
		t.Fatalf("revision 1 result action=%d marks=%064b dset=%q matched=%v", action, marks, dset, matched)
	}
	if calls1.Load() != 1 {
		t.Fatalf("expected revision 1 mapper call, got %d", calls1.Load())
	}

	var calls2 atomic.Uint64
	m2 := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{
			testDomainMapperPlugin: testDomainMapperPlugin{marks: []uint8{18}, tag: "rev2", match: true},
			revision:               2,
			calls:                  &calls2,
		},
	})
	fastBypass2 := buildFastBypass(coremain.NewBP("udp_test_2", m2), fc, stats, 0)
	action, _, marks, dset, matched, _ = fastBypass2(len(req), append([]byte(nil), req...), addr)
	if action != server.FastActionContinue || dset != "rev2" || !matched || (marks&(1<<18)) == 0 {
		t.Fatalf("revision 2 result action=%d marks=%064b dset=%q matched=%v", action, marks, dset, matched)
	}
	if calls2.Load() != 1 {
		t.Fatalf("expected revision 2 mapper refresh, got %d", calls2.Load())
	}
}

func TestBuildFastBypassWarmupSkipsFastPath(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "warmup.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "warmup", false)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, time.Second)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, req)
	action, _, _, _, _, staleRefresh := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected warmup to skip fast path, got %d", action)
	}
	if staleRefresh {
		t.Fatal("warmup path should not request stale refresh")
	}
	if stats.bypassWarmupSkip.Load() == 0 {
		t.Fatal("expected bypassWarmupSkip metric to increase during warmup")
	}
}

func TestBuildFastBypassExpiredCacheRequestsStaleRefresh(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "expired.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "stale", false)

	ptr := mustFastCacheItem(t, fc, name, dns.TypeA)
	atomic.StoreInt64(&ptr.expire, fastNowUnix()-1)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)

	req := makeQuery(t, name, dns.TypeA, 0x9999)
	action, _, marks, dset, matched, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected continue action, got %d", action)
	}
	if marks != 0 {
		t.Fatalf("expected no pre marks, got %064b", marks)
	}
	if dset != "" || matched {
		t.Fatalf("unexpected pre fast match result: dset=%q matched=%v", dset, matched)
	}
	if !staleRefresh {
		t.Fatal("expected stale refresh flag on expired cached item")
	}
	if stats.refreshRequested.Load() == 0 {
		t.Fatal("expected refresh requested metric to increase")
	}
}

func TestFastHandlerServesStaleWhileRefreshingExpiredCache(t *testing.T) {
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, &fastStats{})
	name := "refresh.example."
	oldResp := makeAnswerWithIP(t, name, dns.TypeA, 0x1111, 30, "1.1.1.1")
	newResp := makeAnswerWithIP(t, name, dns.TypeA, 0x1111, 30, "8.8.8.8")
	fc.Store(name, dns.TypeA, oldResp, "stale", false)

	hash := fastQNameHashString(name, dns.TypeA)
	ptr := mustFastCacheItem(t, fc, name, dns.TypeA)
	atomic.StoreInt64(&ptr.expire, fastNowUnix()-1)
	atomic.StoreUint32(&ptr.updating, 1)

	called := make(chan struct{}, 1)
	handler := &fastHandler{
		next: pooledHandler{payload: newResp, called: called},
		fc:   fc,
		sw:   newFastSwitchValue(testSwitchPlugin{value: "on"}),
	}

	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = 0x9999

	payload := handler.Handle(context.Background(), q, server.QueryMeta{PreFastStaleRefresh: true}, nil)
	if payload == nil {
		t.Fatal("expected stale payload")
	}
	defer pool.ReleaseBuf(payload)

	var stale dns.Msg
	if err := stale.Unpack(*payload); err != nil {
		t.Fatalf("unpack stale payload: %v", err)
	}
	if got := stale.Answer[0].(*dns.A).A.String(); got != "1.1.1.1" {
		t.Fatalf("expected stale IP 1.1.1.1, got %s", got)
	}
	if fc.stats.bypassStaleReply.Load() == 0 {
		t.Fatal("expected stale reply metric to increase")
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background refresh")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		query := makeQuery(t, name, dns.TypeA, 0x8888)
		buf := make([]byte, len(newResp))
		copy(buf, query)
		action, respLen, _, _, _ := fc.GetOrUpdating(hash, buf, name, dns.TypeA, true)
		if action != server.FastActionReply {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		var fresh dns.Msg
		if err := fresh.Unpack(buf[:respLen]); err != nil {
			t.Fatalf("unpack refreshed payload: %v", err)
		}
		if got := fresh.Answer[0].(*dns.A).A.String(); got == "8.8.8.8" {
			if fc.stats.refreshStore.Load() == 0 {
				t.Fatal("expected refresh store metric to increase")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected refreshed cache to replace stale payload")
}

func TestFastHandlerSkipsStaleWhenControlSwitchChanges(t *testing.T) {
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, &fastStats{})
	name := "refresh-control-switch.example."
	oldResp := makeAnswerWithIP(t, name, dns.TypeA, 0x1111, 30, "1.1.1.1")
	newResp := makeAnswerWithIP(t, name, dns.TypeA, 0x1111, 30, "8.8.8.8")
	if !fc.storeWithRuleRevision(name, dns.TypeA, oldResp, "stale", false, false, 0, fastRuleRevision{
		control: fastControlRevisionFromSwitches(fastSwitchValue{}, fastSwitchValue{}, fastSwitchValue{}, newFastSwitchValue(testSwitchPlugin{value: "realip"})),
	}) {
		t.Fatal("expected cache store")
	}
	ptr := mustFastCacheItem(t, fc, name, dns.TypeA)
	atomic.StoreInt64(&ptr.expire, fastNowUnix()-1)
	atomic.StoreUint32(&ptr.updating, 1)

	called := make(chan struct{}, 1)
	handler := &fastHandler{
		next:           pooledHandler{payload: newResp, called: called},
		fc:             fc,
		sw:             newFastSwitchValue(testSwitchPlugin{value: "on"}),
		cnAnswerSwitch: newFastSwitchValue(testSwitchPlugin{value: "fakeip"}),
	}

	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = 0x9999

	payload := handler.Handle(context.Background(), q, server.QueryMeta{PreFastStaleRefresh: true}, nil)
	if payload == nil {
		t.Fatal("expected payload from next handler")
	}
	defer pool.ReleaseBuf(payload)

	select {
	case <-called:
	default:
		t.Fatal("expected next handler to run instead of stale reply")
	}
	var got dns.Msg
	if err := got.Unpack(*payload); err != nil {
		t.Fatalf("unpack payload: %v", err)
	}
	if ip := got.Answer[0].(*dns.A).A.String(); ip != "8.8.8.8" {
		t.Fatalf("expected fresh IP 8.8.8.8, got %s", ip)
	}
	if fc.stats.bypassStaleReply.Load() != 0 {
		t.Fatal("stale reply metric should not increase on control switch mismatch")
	}
	if fc.stats.refreshMiss.Load() == 0 {
		t.Fatal("expected refresh miss metric to increase")
	}
}

func BenchmarkBuildFastBypassColdMiss(b *testing.B) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), nil, 0)
	req := makeQueryNoTest("bench.example.", dns.TypeA, 0x1234)
	addr := netip.MustParseAddrPort("127.0.0.1:5353")
	buf := make([]byte, len(req))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, req)
		_, _, _, _, _, _ = fastBypass(len(buf), buf, addr)
	}
}

func BenchmarkBuildFastBypassCacheHit(b *testing.B) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "bench-cache.example."
	resp := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "bench", false)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)
	req := makeQueryNoTest(name, dns.TypeA, 0x1234)
	addr := netip.MustParseAddrPort("127.0.0.1:5353")
	buf := make([]byte, len(resp))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, req)
		_, _, _, _, _, _ = fastBypass(len(req), buf, addr)
	}
}

func BenchmarkBuildFastBypassCacheHitWithAudit(b *testing.B) {
	oldCollector := coremain.GlobalAuditCollector
	collector := coremain.NewAuditCollector(coremain.AuditSettings{Enabled: true}, b.TempDir())
	coremain.GlobalAuditCollector = collector
	b.Cleanup(func() {
		coremain.GlobalAuditCollector = oldCollector
	})

	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:  time.Minute,
		ttlMax:       30,
		auditEnabled: true,
	}, stats)
	name := "bench-cache-audit.example."
	resp := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "bench", false)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0, true)
	req := makeQueryNoTest(name, dns.TypeA, 0x1234)
	addr := netip.MustParseAddrPort("127.0.0.1:5353")
	buf := make([]byte, len(resp))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, req)
		_, _, _, _, _, _ = fastBypass(len(req), buf, addr)
	}
}

func BenchmarkBuildFastBypassCacheHitWithAuditParallel(b *testing.B) {
	oldCollector := coremain.GlobalAuditCollector
	collector := coremain.NewAuditCollector(coremain.AuditSettings{Enabled: true}, b.TempDir())
	coremain.GlobalAuditCollector = collector
	b.Cleanup(func() {
		coremain.GlobalAuditCollector = oldCollector
	})

	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:  time.Minute,
		ttlMax:       30,
		auditEnabled: true,
	}, stats)
	name := "bench-cache-audit-parallel.example."
	resp := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "记忆直连", false)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0, true)
	req := makeQueryNoTest(name, dns.TypeA, 0x1234)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		addr := netip.MustParseAddrPort("192.168.20.171:5353")
		buf := make([]byte, len(resp))
		txid := uint16(0)
		for pb.Next() {
			copy(buf, req)
			buf[0], buf[1] = byte(txid>>8), byte(txid)
			txid++
			_, _, _, _, _, _ = fastBypass(len(req), buf, addr)
		}
	})
}

func BenchmarkBuildFastBypassCacheHitWithAuditWorkerParallel(b *testing.B) {
	oldCollector := coremain.GlobalAuditCollector
	collector := coremain.NewAuditCollector(coremain.AuditSettings{
		Enabled:         true,
		FlushBatchSize:  4096,
		FlushIntervalMs: 5000,
	}, b.TempDir())
	collector.StartWorker()
	coremain.GlobalAuditCollector = collector
	b.Cleanup(func() {
		collector.StopWorker()
		coremain.GlobalAuditCollector = oldCollector
	})

	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:  time.Minute,
		ttlMax:       30,
		auditEnabled: true,
	}, stats)
	name := "bench-cache-audit-worker.example."
	resp := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
	fc.Store(name, dns.TypeA, resp, "记忆直连", false)

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testRevisionSwitchPlugin{value: "on", revision: 1},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0, true)
	req := makeQueryNoTest(name, dns.TypeA, 0x1234)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		addr := netip.MustParseAddrPort("192.168.20.171:5353")
		buf := make([]byte, len(resp))
		txid := uint16(0)
		for pb.Next() {
			copy(buf, req)
			buf[0], buf[1] = byte(txid>>8), byte(txid)
			txid++
			_, _, _, _, _, _ = fastBypass(len(req), buf, addr)
		}
	})
}

func BenchmarkBuildFastBypassCacheHitNumericRevision(b *testing.B) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "bench-numeric-revision.example."
	resp := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
	fc.storeWithRuleRevision(name, dns.TypeA, resp, "bench", false, false, 0, fastRuleRevision{
		domainMapper: fastNumericRevisionValue(7),
	})

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":    testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{revision: 7},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)
	req := makeQueryNoTest(name, dns.TypeA, 0x1234)
	addr := netip.MustParseAddrPort("127.0.0.1:5353")
	buf := make([]byte, len(resp))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, req)
		_, _, _, _, _, _ = fastBypass(len(req), buf, addr)
	}
}

func BenchmarkBuildFastBypassNoResponseCacheRuleMetaHit(b *testing.B) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:      time.Minute,
		ttlMax:           30,
		bypassDomainSets: []string{"DDNS域名"},
	}, stats)
	name := "bench-no-response-cache.example."
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path": testSwitchPlugin{value: "on"},
		"unified_matcher1": testNumericRevisionDomainMapperPlugin{
			testDomainMapperPlugin: testDomainMapperPlugin{tag: "DDNS域名", match: true},
			revision:               7,
		},
	})
	bp := coremain.NewBP("udp_bench", m)
	fastBypass := buildFastBypass(bp, fc, stats, 0)
	req := makeQueryNoTest(name, dns.TypeA, 0x1234)
	addr := netip.MustParseAddrPort("127.0.0.1:5353")
	buf := make([]byte, len(req))
	copy(buf, req)
	_, _, _, _, _, _ = fastBypass(len(req), buf, addr)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, req)
		_, _, _, _, _, _ = fastBypass(len(req), buf, addr)
	}
}

func TestFastClientPolicyMarksRefreshesWhenIPSetRevisionChanges(t *testing.T) {
	cache := &clientPolicyCache{}
	addr := netip.MustParseAddr("192.168.5.13")
	whitelist := &countingIPSetPlugin{match: true}

	marks := fastClientPolicyMarks(cache, addr, "whitelist", whitelist, nil)
	if (marks & (uint64(1) << 39)) != 0 {
		t.Fatalf("expected whitelisted client to keep proxy path, got %064b", marks)
	}
	if got := whitelist.calls.Load(); got != 1 {
		t.Fatalf("expected first lookup to check whitelist once, got %d", got)
	}

	marks = fastClientPolicyMarks(cache, addr, "whitelist", whitelist, nil)
	if (marks & (uint64(1) << 39)) != 0 {
		t.Fatalf("expected cached whitelisted client to keep proxy path, got %064b", marks)
	}
	if got := whitelist.calls.Load(); got != 1 {
		t.Fatalf("expected cached lookup to skip whitelist, got %d calls", got)
	}

	whitelist.match = false
	whitelist.revision.Add(1)
	marks = fastClientPolicyMarks(cache, addr, "whitelist", whitelist, nil)
	if (marks & (uint64(1) << 39)) == 0 {
		t.Fatalf("expected refreshed whitelist miss to use direct-path branch, got %064b", marks)
	}
	if got := whitelist.calls.Load(); got != 2 {
		t.Fatalf("expected revision change to recheck whitelist, got %d calls", got)
	}
}
