package cache

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

func TestResponseTTLOverridesPreserveNegativeLifetime(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rcode    int
		answer   bool
		soa      bool
		soaTTL   uint32
		minimum  uint32
		lifetime time.Duration
		cacheTTL time.Duration
	}{
		{name: "positive", answer: true, lifetime: 120 * time.Second, cacheTTL: (43200 + 120) * time.Second},
		{name: "servfail", rcode: dns.RcodeServerFailure, lifetime: time.Second, cacheTTL: time.Second},
		{name: "nxdomain", rcode: dns.RcodeNameError, soa: true, soaTTL: 600, minimum: 30, lifetime: 30 * time.Second, cacheTTL: 30 * time.Second},
		{name: "nxdomain_soa_ttl", rcode: dns.RcodeNameError, soa: true, soaTTL: 20, minimum: 600, lifetime: 20 * time.Second, cacheTTL: 20 * time.Second},
		{name: "nxdomain_policy_cap", rcode: dns.RcodeNameError, soa: true, soaTTL: 600, minimum: 600, lifetime: 300 * time.Second, cacheTTL: 300 * time.Second},
		{name: "nodata", soa: true, soaTTL: 600, minimum: 30, lifetime: 30 * time.Second, cacheTTL: 30 * time.Second},
		{name: "empty", lifetime: 5 * time.Second, cacheTTL: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newResponseTTLTestCache()
			defer c.Close()
			qCtx := responseTTLQuery(tc.rcode, tc.answer, tc.soa, tc.soaTTL, tc.minimum)
			v, ok := c.saveRespToCache("response-ttl", qCtx)
			if !ok {
				t.Fatal("response was not cached")
			}
			if got := time.Duration(v.expireUnixNano - v.storedUnixNano); got != tc.lifetime {
				t.Fatalf("fresh lifetime = %s, want %s", got, tc.lifetime)
			}
			_, expiration, ok := c.backend.Get("response-ttl")
			if !ok || expiration.UnixNano()-v.storedUnixNano != int64(tc.cacheTTL) {
				t.Fatalf("backend lifetime = %s, want %s", time.Duration(expiration.UnixNano()-v.storedUnixNano), tc.cacheTTL)
			}
			msg, lazy, _, corrupt := c.respFromCacheItem(v, c.args.LazyStaleTTL, expiredMsgTtl)
			if msg == nil || lazy || corrupt {
				t.Fatal("fresh response was not returned")
			}
			if tc.soa && msg.Ns[0].Header().Ttl > uint32(tc.lifetime/time.Second) {
				t.Fatalf("negative SOA TTL = %d, exceeds lifetime %s", msg.Ns[0].Header().Ttl, tc.lifetime)
			}
			wire := buildCachedWirePayload(v, 42, false, expiredMsgTtl, v.storedUnixNano, c.args.ClientTTLMin, c.args.ClientTTLMax, dns.MinMsgSize)
			var udp dns.Msg
			if err := udp.Unpack(wire); err != nil {
				t.Fatal(err)
			}
			if tc.soa && udp.Ns[0].Header().Ttl > uint32(tc.lifetime/time.Second) {
				t.Fatalf("UDP SOA TTL = %d, exceeds lifetime %s", udp.Ns[0].Header().Ttl, tc.lifetime)
			}

			// Also covers old dumps whose backend expiration was extended by the
			// domain policy: stale reads must inspect the response itself.
			stale := *v
			stale.storedUnixNano = time.Now().Add(-time.Minute).UnixNano()
			stale.expireUnixNano = time.Now().Add(-time.Second).UnixNano()
			msg, lazy, _, corrupt = c.respFromCacheItem(&stale, c.args.LazyStaleTTL, expiredMsgTtl)
			if corrupt {
				t.Fatal("valid response reported as corrupt")
			}
			if tc.answer {
				if msg == nil || !lazy {
					t.Fatal("positive stale response was lost")
				}
			} else {
				if msg != nil || lazy {
					t.Fatal("expired negative response was returned as stale")
				}
				if wire := buildCachedWirePayload(&stale, 42, true, expiredMsgTtl, time.Now().UnixNano(), 120, 900, dns.MinMsgSize); wire != nil {
					t.Fatal("expired negative response produced a stale UDP payload")
				}
			}
		})
	}
}

func TestResponseTTLZeroSOADoesNotCache(t *testing.T) {
	for _, rcode := range []int{dns.RcodeSuccess, dns.RcodeNameError} {
		for _, soa := range [][2]uint32{{600, 0}, {0, 600}} {
			c := newResponseTTLTestCache()
			qCtx := responseTTLQuery(rcode, false, true, soa[0], soa[1])
			if _, ok := c.saveRespToCache("zero-ttl", qCtx); ok || c.backend.Len() != 0 {
				t.Fatalf("cached %s response with SOA TTL=%d MINIMUM=%d", dns.RcodeToString[rcode], soa[0], soa[1])
			}
			_ = c.Close()
		}
	}
}

func TestResponseTTLCNAMEWithNegativeSOA(t *testing.T) {
	for _, qtype := range []uint16{dns.TypeA, dns.TypeCNAME} {
		c := newResponseTTLTestCache()
		qCtx := responseTTLQuery(dns.RcodeSuccess, false, true, 600, 30)
		qCtx.Q().Question[0].Qtype = qtype
		qCtx.R().Question[0].Qtype = qtype
		qCtx.R().Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: "negative.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "target.example."}}
		v, ok := c.saveRespToCache("cname-soa", qCtx)
		if !ok {
			t.Fatal("response was not cached")
		}
		positive := qtype == dns.TypeCNAME
		if positiveCacheWire(v.resp) != positive {
			t.Fatalf("UDP response classification for %s differs from the DNS answer", dns.TypeToString[qtype])
		}
		if !positive && time.Duration(v.expireUnixNano-v.storedUnixNano) != 30*time.Second {
			t.Fatal("CNAME + SOA NODATA ignored its negative SOA lifetime")
		}
		stale := *v
		stale.expireUnixNano = time.Now().Add(-time.Second).UnixNano()
		msg, lazy, _, _ := c.respFromCacheItem(&stale, c.args.LazyStaleTTL, expiredMsgTtl)
		if (msg != nil && lazy) != positive {
			t.Fatalf("stale policy for %s did not match the positive/negative answer", dns.TypeToString[qtype])
		}
		wire := buildCachedWirePayload(&stale, 42, true, expiredMsgTtl, time.Now().UnixNano(), 120, 900, dns.MinMsgSize)
		if (wire != nil) != positive {
			t.Fatal("stale UDP policy differs from message policy")
		}
		_ = c.Close()
	}
}

func TestResponseTTLNegativeFreshWireIgnoresClientMinimum(t *testing.T) {
	c := newResponseTTLTestCache()
	defer c.Close()
	for _, rcode := range []int{dns.RcodeSuccess, dns.RcodeNameError, dns.RcodeServerFailure} {
		qCtx := responseTTLQuery(rcode, false, true, 30, 30)
		v, ok := c.saveRespToCache("wire-ttl", qCtx)
		if !ok {
			t.Fatal("response was not cached")
		}
		wire := buildCachedWirePayload(v, 42, false, expiredMsgTtl, v.storedUnixNano+int64(time.Second/2), 120, 900, dns.MinMsgSize)
		if binary.BigEndian.Uint16(wire[:2]) != 42 {
			t.Fatal("transaction ID was lost")
		}
		var msg dns.Msg
		if err := msg.Unpack(wire); err != nil {
			t.Fatal(err)
		}
		if msg.Ns[0].Header().Ttl > 30 {
			t.Fatalf("client TTL minimum extended %s SOA TTL to %d", dns.RcodeToString[rcode], msg.Ns[0].Header().Ttl)
		}
	}
}

func newResponseTTLTestCache() *Cache {
	return NewCache(&Args{
		Size: 256, LazyCacheTTL: 2592000, LazyStaleTTL: 2592000,
		ClientTTLMin: 120, ClientTTLMax: 900, NXDomainTTL: 300, ServfailTTL: 1,
		DomainSetTTL: []DomainSetTTLPolicy{{Sets: []string{"高变化域名"}, LazyCacheTTL: 43200, LazyStaleTTL: 43200}},
	}, Opts{})
}

func responseTTLQuery(rcode int, answer, soa bool, soaTTL, minimum uint32) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion("negative.example.", dns.TypeA)
	r := new(dns.Msg)
	r.SetRcode(q, rcode)
	if answer {
		r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: []byte{1, 2, 3, 4}}}
	}
	if soa {
		r.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: soaTTL}, Ns: "ns.example.", Mbox: "hostmaster.example.", Minttl: minimum}}
	}
	qCtx := query_context.NewContext(q)
	qCtx.StoreValue(query_context.KeyDomainSet, "高变化域名")
	qCtx.SetResponse(r)
	return qCtx
}
