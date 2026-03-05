package udp_server

import (
	"fmt"
	"hash/maphash"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

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

	fc.Store(collisionName, qtype, makeAnswer(t, collisionName, qtype, 0x2222, 30), "")

	buf := make([]byte, 512)
	query := makeQuery(t, baseName, qtype, 0x9999)
	copy(buf, query)

	action, _, _, _ := fc.GetOrUpdating(baseHash, buf, baseName, qtype)
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
	fc.Store(name, qtype, resp, "dset")

	query := makeQuery(t, name, qtype, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)

	h := maphash.String(maphashSeed, name) ^ uint64(qtype)
	action, respLen, _, _ := fc.GetOrUpdating(h, buf, name, qtype)
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
	fc.Store(name, qtype, respLow, "dset")
	buf = make([]byte, len(respLow))
	copy(buf, query)
	action, respLen, _, _ = fc.GetOrUpdating(h, buf, name, qtype)
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
