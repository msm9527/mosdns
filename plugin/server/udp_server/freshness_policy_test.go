package udp_server

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func freshnessTestCache() *fastCache {
	return newFastCache(fastCacheConfig{
		internalTTL: 2 * time.Hour, staleMax: time.Minute, staleRetry: 10 * time.Second,
		ttlMin: 120, ttlMax: 900, responseSlots: 16, ruleSlots: 16,
	}, nil)
}

func freshnessTestResponse(t *testing.T, name string, ttl uint32) *dns.Msg {
	t.Helper()
	var response dns.Msg
	if err := response.Unpack(makeAnswer(t, name, dns.TypeA, 42, ttl)); err != nil {
		t.Fatal(err)
	}
	return &response
}

func freshnessTestReply(t *testing.T, fc *fastCache, item *fastCacheItem, at int64, withOPT bool) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(item.qname, item.qtype)
	q.Id = 84
	if withOPT {
		q.SetEdns0(1232, false)
	}
	buf := make([]byte, 4096)
	copy(buf, mustPack(t, q))
	action, length, _, _, refresh, _ := fc.replyFromItemAt(item, true, buf, true, false, fastRuleRevision{}, item.hash, at)
	if action != server.FastActionReply || refresh {
		t.Fatalf("expected hot reply: action=%d refresh=%v", action, refresh)
	}
	var response dns.Msg
	if err := response.Unpack(buf[:length]); err != nil {
		t.Fatal(err)
	}
	if response.Id != q.Id {
		t.Fatal("freshness policy lost the request transaction ID")
	}
	return &response
}

func TestFastFreshnessUsesUnclampedTTLAndRetentionCeiling(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ttl       uint32
		retention time.Duration
		want      int64
	}{
		{name: "short_wire_ttl", ttl: 30, retention: 2 * time.Hour, want: 30},
		{name: "short_retention", ttl: 300, retention: 20 * time.Second, want: 20},
		{name: "domain_override", ttl: 30, retention: 12 * time.Hour, want: 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := freshnessTestCache()
			fc.cfg.internalTTL = tc.retention
			set := ""
			if tc.name == "domain_override" {
				fc.cfg.internalTTL = time.Second
				fc.cfg.domainSetTTL = []fastDomainSetTTLPolicy{{Sets: []string{"volatile"}, InternalTTL: 43200}}
				set = "volatile"
			}
			name := "freshness.example."
			before := fastNowUnix()
			item := fc.newResponseItem(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 42, tc.ttl), set, false, false, 0, fastRuleRevision{})
			after := fastNowUnix()
			if item == nil {
				t.Fatal("positive response was rejected")
			}
			if got := item.freshnessDeadline(); got < before+tc.want || got > after+tc.want {
				t.Fatalf("fresh deadline=%d, want %d..%d", got, before+tc.want, after+tc.want)
			}
			if item.expire < before+int64(tc.retention/time.Second) || item.expire > after+int64(tc.retention/time.Second) {
				t.Fatal("DNS freshness changed configured retention")
			}
			response := freshnessTestReply(t, fc, item, item.freshnessDeadline()-1, false)
			if got := response.Answer[0].Header().Ttl; got != 1 {
				t.Fatalf("near-expiry reply extended client caching: ttl=%d", got)
			}
		})
	}
}

func TestFastFreshnessIncludesCNAMEAndAdditionalButNotOPT(t *testing.T) {
	for _, additional := range []bool{false, true} {
		name := "cname"
		wantTTL := int64(20)
		if additional {
			name = "additional"
			wantTTL = 7
		}
		t.Run(name, func(t *testing.T) {
			fc := freshnessTestCache()
			response := freshnessTestResponse(t, "alias.example.", 300)
			response.Answer[0].Header().Name = "target.example."
			response.Answer = append([]dns.RR{&dns.CNAME{
				Hdr:    dns.RR_Header{Name: "alias.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 20},
				Target: "target.example.",
			}}, response.Answer...)
			if additional {
				response.Extra = append(response.Extra, &dns.A{
					Hdr: dns.RR_Header{Name: "glue.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 7},
					A:   net.IPv4(1, 2, 3, 4),
				})
			}
			// An empty OPT TTL is zero, but is not a zero-lifetime DNS record.
			response.SetEdns0(1232, false)
			before := fastNowUnix()
			item := fc.newResponseItem("alias.example.", dns.TypeA, mustPack(t, response), "", false, false, 0, fastRuleRevision{})
			after := fastNowUnix()
			if additional {
				// Additional glue is not currently admitted into the fast cache.
				// Its TTL parser must nevertheless account for every non-OPT RR.
				if item != nil {
					t.Fatal("freshness policy widened additional-record admission")
				}
				wire := mustPack(t, response)
				offsets := findTTLOffsets(wire)
				deadline := fastAuthenticatedUntil(wire, offsets, time.Unix(before, 0))
				if deadline != before+wantTTL {
					t.Fatal("shorter additional record was omitted from TTL parsing")
				}
				item = &fastCacheItem{freshUntil: deadline, ttlOffsets: offsets}
				capFastFreshTTL(wire, item, deadline, deadline-3)
				var got dns.Msg
				if err := got.Unpack(wire); err != nil {
					t.Fatal(err)
				}
				if got.Answer[0].Header().Ttl != 3 || got.Answer[1].Header().Ttl != 3 || got.Extra[0].Header().Ttl != 3 || got.IsEdns0().Hdr.Ttl != 0 {
					t.Fatal("TTL aging omitted a record or changed OPT flags")
				}
				return
			}
			if item == nil {
				t.Fatal("OPT flags were treated as a zero record TTL")
			}
			if item.freshUntil < before+wantTTL || item.freshUntil > after+wantTTL {
				t.Fatalf("fresh deadline ignored a shorter record: got=%d want=%d..%d", item.freshUntil, before+wantTTL, after+wantTTL)
			}
			for _, withOPT := range []bool{false, true} {
				got := freshnessTestReply(t, fc, item, item.freshnessDeadline()-3, withOPT)
				for _, records := range [][]dns.RR{got.Answer, got.Ns, got.Extra} {
					for _, rr := range records {
						if opt, ok := rr.(*dns.OPT); ok {
							if opt.Hdr.Ttl != response.IsEdns0().Hdr.Ttl || opt.UDPSize() != 1232 {
								t.Fatal("TTL aging changed EDNS flags or UDP size")
							}
							continue
						}
						if rr.Header().Ttl != 3 {
							t.Fatalf("record TTL escaped remaining lifetime: %s", rr)
						}
					}
				}
				if (got.IsEdns0() != nil) != withOPT {
					t.Fatal("TTL aging changed request OPT compatibility")
				}
			}
		})
	}
}

func TestFastFreshnessCopyAlsoAgesWithoutMutatingStoredPayload(t *testing.T) {
	fc := freshnessTestCache()
	name := "copy-freshness.example."
	fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 42, 30), "", false)
	item := mustFastCacheItem(t, fc, name, dns.TypeA)
	item.freshUntil = fastNowUnix() + 8
	response := readStalePolicyResponse(t, fc.CopyResponse(84, name, dns.TypeA, true))
	if ttl := response.Answer[0].Header().Ttl; ttl == 0 || ttl > 8 {
		t.Fatalf("copy returned unaged TTL=%d", ttl)
	}
	var stored dns.Msg
	if err := stored.Unpack(item.resp); err != nil {
		t.Fatal(err)
	}
	if stored.Answer[0].Header().Ttl != 120 {
		t.Fatal("reply aging mutated the cached wire payload")
	}
}

func TestFastFreshnessKeepsFakeIPMappingLifetime(t *testing.T) {
	fc := freshnessTestCache()
	name := "fake-freshness.example."
	item := fc.newResponseItem(name, dns.TypeA, makeAnswerWithIP(t, name, dns.TypeA, 42, 30, "30.2.3.4"), "", true, false, 0, fastRuleRevision{})
	if item == nil || item.freshUntil != 0 || item.freshnessDeadline() != item.expire {
		t.Fatal("real-IP refresh lifetime leaked into FakeIP mapping cache")
	}
	response := freshnessTestReply(t, fc, item, item.expire-1, false)
	if response.Answer[0].Header().Ttl != 120 {
		t.Fatal("real-IP TTL aging changed FakeIP mapping response")
	}
}

func TestFastFreshnessRejectsZeroTTLRealAddress(t *testing.T) {
	fc := freshnessTestCache()
	name := "zero-freshness.example."
	if fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 42, 0), "", false) || fc.Len() != 0 {
		t.Fatal("zero-TTL address became a reusable stale cache entry")
	}
}

func TestFastFreshnessRefreshUsesOneReservationPerWindow(t *testing.T) {
	fc := freshnessTestCache()
	name := "refresh-freshness.example."
	fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 42, 30), "", false)
	item := mustFastCacheItem(t, fc, name, dns.TypeA)
	deadline := item.freshnessDeadline()
	query := makeQuery(t, name, dns.TypeA, 84)
	for _, wave := range []struct {
		delay int64
		want  int64
	}{{0, 1}, {9, 0}, {10, 1}, {19, 0}, {20, 1}} {
		var reservations atomic.Int64
		var wg sync.WaitGroup
		for range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 512)
				copy(buf, query)
				action, length, _, _, refresh, _ := fc.replyFromItemAt(item, true, buf, true, false, fastRuleRevision{}, item.hash, deadline+wave.delay)
				if refresh {
					reservations.Add(1)
					if action != server.FastActionContinue {
						t.Error("refresh reservation did not enter the stale handler")
					}
					return
				}
				var response dns.Msg
				if action != server.FastActionReply || response.Unpack(buf[:length]) != nil || len(response.Answer) == 0 || response.Answer[0].Header().Ttl > 5 {
					t.Error("concurrent request did not receive immediate short-lived stale reply")
				}
			}()
		}
		wg.Wait()
		if got := reservations.Load(); got != wave.want {
			t.Fatalf("at +%ds reserved %d refreshes, want %d", wave.delay, got, wave.want)
		}
	}
	buf := make([]byte, 512)
	copy(buf, query)
	action, _, _, _, refresh, _ := fc.replyFromItemAt(item, true, buf, true, false, fastRuleRevision{}, item.hash, deadline+61)
	if action != server.FastActionContinue || refresh {
		t.Fatal("internal retention extended the maximum stale window")
	}
	item.freshUntil = fastNowUnix() - 61
	if wire := fc.CopyResponse(84, name, dns.TypeA, true); wire != nil {
		pool.ReleaseBuf(wire)
		t.Fatal("copied response escaped the freshness-based stale window")
	}
}

func TestFastFreshnessHotReplyDoesNotAllocate(t *testing.T) {
	fc := freshnessTestCache()
	name := "allocation-freshness.example."
	fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 42, 30), "", false)
	item := mustFastCacheItem(t, fc, name, dns.TypeA)
	query, buf := makeQuery(t, name, dns.TypeA, 84), make([]byte, 512)
	at := item.freshnessDeadline() - 3
	allocations := testing.AllocsPerRun(1000, func() {
		copy(buf, query)
		action, length, _, _, refresh, _ := fc.replyFromItemAt(item, true, buf, true, false, fastRuleRevision{}, item.hash, at)
		if action != server.FastActionReply || length == 0 || refresh || binary.BigEndian.Uint16(buf[:2]) != 84 {
			t.Fatal("ordinary TTL aging left the fast reply path")
		}
	})
	if allocations != 0 {
		t.Fatalf("fresh TTL aging allocated %g times per hit", allocations)
	}
}
