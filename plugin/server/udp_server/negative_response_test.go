package udp_server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestFastCacheKeepsNegativeResponsesInSharedCache(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rcode     int
		answer    bool
		soa       bool
		cname     bool
		zeroSOA   bool
		truncated bool
		wantCache bool
	}{
		{name: "positive", answer: true, wantCache: true},
		{name: "positive_cname", answer: true, cname: true, wantCache: true},
		{name: "servfail", rcode: dns.RcodeServerFailure},
		{name: "nxdomain", rcode: dns.RcodeNameError, soa: true},
		{name: "empty_noerror"},
		{name: "nodata_soa", soa: true},
		{name: "cname_nodata_soa", cname: true, soa: true},
		{name: "nodata_zero_soa", soa: true, zeroSOA: true},
		{name: "truncated_answer", answer: true, truncated: true},
	} {
		for _, domainSet := range []string{"", "高变化域名"} {
			t.Run(tc.name+"/"+domainSet, func(t *testing.T) {
				q := new(dns.Msg)
				q.SetQuestion("negative-fast.example.", dns.TypeA)
				r := new(dns.Msg)
				r.SetRcode(q, tc.rcode)
				r.Truncated = tc.truncated
				if tc.cname {
					r.Answer = append(r.Answer, &dns.CNAME{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 30}, Target: "target.example."})
				}
				if tc.answer {
					r.Answer = append(r.Answer, &dns.A{Hdr: dns.RR_Header{Name: "target.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: []byte{1, 2, 3, 4}})
				}
				if tc.soa {
					var minimum uint32 = 30
					if tc.zeroSOA {
						minimum = 0
					}
					r.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 600}, Ns: "ns.example.", Mbox: "hostmaster.example.", Minttl: minimum}}
				}
				payload := mustPack(t, r)
				fc := newNegativePolicyTestFastCache()
				h := &fastHandler{next: staticHandler{payload: payload}, fc: fc, sw: newFastSwitchValue(testSwitchPlugin{value: "on"})}
				returned := h.Handle(context.Background(), q, server.QueryMeta{PreFastDomainSet: domainSet}, nil)
				if returned == nil || !bytes.Equal(*returned, payload) {
					t.Fatal("handler changed the original upstream response")
				}
				assertFastPolicyCacheState(t, fc, q, payload, tc.wantCache)

				// Other callers can store directly, bypassing fastHandler entirely.
				direct := newNegativePolicyTestFastCache()
				if got := direct.Store(q.Question[0].Name, dns.TypeA, payload, domainSet, false); got != tc.wantCache {
					t.Fatalf("direct store = %v, want %v", got, tc.wantCache)
				}
				assertFastPolicyCacheState(t, direct, q, payload, tc.wantCache)
			})
		}
	}
}

func assertFastPolicyCacheState(t *testing.T, fc *fastCache, q *dns.Msg, payload []byte, cached bool) {
	t.Helper()
	if (fc.Len() != 0) != cached {
		t.Fatalf("fast cache size = %d, want cached=%v", fc.Len(), cached)
	}
	question := q.Question[0]
	buf := make([]byte, len(payload))
	copy(buf, makeQuery(t, question.Name, question.Qtype, 42))
	hash := fastQNameHashString(question.Name, question.Qtype)
	action, _, _, _, _ := fc.GetOrUpdating(hash, buf, question.Name, question.Qtype, true)
	if cached && action != server.FastActionReply {
		t.Fatalf("positive response missed fast cache: action=%d", action)
	}
	if !cached && action != server.FastActionContinue {
		t.Fatalf("negative response bypassed shared cache: action=%d", action)
	}
}

func newNegativePolicyTestFastCache() *fastCache {
	return newFastCache(fastCacheConfig{
		internalTTL: 12 * time.Hour, staleMax: 12 * time.Hour, ttlMin: 120, ttlMax: 900,
		domainSetTTL:  []fastDomainSetTTLPolicy{{Sets: []string{"高变化域名"}, InternalTTL: 43200, StaleMax: 43200}},
		responseSlots: 64, ruleSlots: 64,
	}, &fastStats{})
}
