package udp_server

import (
	"fmt"
	"hash/maphash"
	"net"
	"net/netip"
	"testing"
	"time"
	"unsafe"

	"github.com/IrineSistiana/mosdns/v5/coremain"
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
	baseHash := maphash.String(maphashSeed, baseName) ^ uint64(qtype)
	baseSlot := baseHash & cacheMask

	collisionName := ""
	for i := 0; i < 1_000_000; i++ {
		candidate := fmt.Sprintf("c-%d.example.org.", i)
		h := maphash.String(maphashSeed, candidate) ^ uint64(qtype)
		if h != baseHash && (h&cacheMask) == baseSlot {
			collisionName = candidate
			break
		}
	}
	if collisionName == "" {
		t.Fatal("failed to find collision candidate")
	}

	fc.Store(collisionName, qtype, makeAnswer(t, collisionName, qtype, 0x2222, 30), "", false)

	buf := make([]byte, 512)
	query := makeQuery(t, baseName, qtype, 0x9999)
	copy(buf, query)

	action, _, _, _ := fc.GetOrUpdating(baseHash, buf, baseName, qtype, true)
	if action != server.FastActionContinue {
		t.Fatalf("expected cache miss due to collision protection, got action=%d", action)
	}
	if stats.cacheCollision.Load() == 0 {
		t.Fatal("expected cache collision metric to increase")
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

	h := maphash.String(maphashSeed, name) ^ uint64(qtype)
	action, respLen, _, _ := fc.GetOrUpdating(h, buf, name, qtype, true)
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
	action, respLen, _, _ = fc.GetOrUpdating(h, buf, name, qtype, true)
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
	h := maphash.String(maphashSeed, name) ^ uint64(qtype)

	action, _, _, _ := fc.GetOrUpdating(h, buf, name, qtype, false)
	if action != server.FastActionContinue {
		t.Fatalf("expected fakeip cache to be bypassed when disabled, got %d", action)
	}

	copy(buf, query)
	action, respLen, _, _ := fc.GetOrUpdating(h, buf, name, qtype, true)
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

type testIPSetPlugin struct {
	match bool
}

func (p testIPSetPlugin) Match(addr netip.Addr) bool {
	return p.match
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
			action, _, marks, _, matched := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
			if action != server.FastActionContinue {
				t.Fatalf("expected continue action, got %d", action)
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
	action, respLen, marks, _, _ := fastBypass(len(buf), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected fast reject reply, got %d", action)
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

func TestBuildFastBypassClientIPFastMarks(t *testing.T) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"udp_fast_path":     testSwitchPlugin{value: "on"},
		"client_proxy_mode": testSwitchPlugin{value: "whitelist"},
		"client_ip":         testIPSetPlugin{match: false},
	})
	bp := coremain.NewBP("udp_test", m)
	fastBypass := buildFastBypass(bp, newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
	}, &fastStats{}), &fastStats{}, 0)

	req := makeQuery(t, "example.org.", dns.TypeA, 0x1234)
	action, _, marks, _, _ := fastBypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected continue action, got %d", action)
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
	action, respLen, marks, dset, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionReply {
		t.Fatalf("expected cache reply, got %d", action)
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
	action, _, _, _, _ := fastBypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
	if action != server.FastActionContinue {
		t.Fatalf("expected warmup to skip fast path, got %d", action)
	}
	if stats.bypassWarmupSkip.Load() == 0 {
		t.Fatal("expected bypassWarmupSkip metric to increase during warmup")
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

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := append([]byte(nil), req...)
		_, _, _, _, _ = fastBypass(len(buf), buf, addr)
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

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := make([]byte, len(resp))
		copy(buf, req)
		_, _, _, _, _ = fastBypass(len(req), buf, addr)
	}
}
