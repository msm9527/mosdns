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
	if args.FastListenerWorkers < 1 {
		t.Fatalf("listener workers = %d, want at least 1", args.FastListenerWorkers)
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
		internalTTL: time.Minute,
		ttlMax:      30,
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
	if got := fc.Len(); got != total {
		t.Fatalf("cache len = %d, want %d", got, total)
	}
	if evictions := stats.cacheEviction.Load(); evictions != 0 {
		t.Fatalf("unexpected evictions for sequential domain set: %d", evictions)
	}

	for i := 0; i < total; i++ {
		name := fmt.Sprintf("rr-%06d.%s", i, suffix)
		query := makeQuery(t, name, qtype, 0x9999)
		buf := make([]byte, 512)
		copy(buf, query)
		hash := fastQNameHashString(name, qtype)
		action, _, _, _, _ := fc.GetOrUpdating(hash, buf, name, qtype, true)
		if action != server.FastActionReply {
			t.Fatalf("expected cache hit for %s, got action=%d", name, action)
		}
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
	item, _ := fcSlow.findItem(hash, name, qtype)
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
	item, _ = fcFast.findItem(hash, name, qtype)
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
	item, _ := fc.findItem(hash, name, qtype)
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
	action, _, marks, _, _, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
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
	action, _, marks, _, _, staleRefresh := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
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

func TestBuildFastBypassCacheHitSkipsMapperWhenRevisionMatches(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "revision-hit.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, 0, "rev1") {
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
	if !fc.storeWithRuleRevision(name, dns.TypeA, resp, "", false, 0, fastRuleRevision{
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
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, 0, "rev1") {
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

func TestBuildFastBypassCacheHitRefreshesWhenRewriteRevisionChanges(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "rewrite-revision-miss.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, 0, "dm1|rewrite1") {
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

func TestBuildFastBypassCachedRuleFlagsHonorSwitches(t *testing.T) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "cached-block.example."
	resp := makeAnswer(t, name, dns.TypeA, 0x2222, 30)
	if !fc.storeWithMeta(name, dns.TypeA, resp, "", false, 1<<1, "rev1") {
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

	hash := fastQNameHashString(name, dns.TypeA)
	ptr, _ := fc.findItem(hash, name, dns.TypeA)
	atomic.StoreInt64(&ptr.expire, time.Now().Add(-time.Second).Unix())

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
	ptr, _ := fc.findItem(hash, name, dns.TypeA)
	atomic.StoreInt64(&ptr.expire, time.Now().Add(-time.Second).Unix())
	atomic.StoreUint32(&ptr.updating, 1)

	called := make(chan struct{}, 1)
	handler := &fastHandler{
		next: pooledHandler{payload: newResp, called: called},
		fc:   fc,
		sw:   testSwitchPlugin{value: "on"},
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

func BenchmarkBuildFastBypassCacheHitNumericRevision(b *testing.B) {
	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, stats)
	name := "bench-numeric-revision.example."
	resp := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
	fc.storeWithRuleRevision(name, dns.TypeA, resp, "bench", false, 0, fastRuleRevision{
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
