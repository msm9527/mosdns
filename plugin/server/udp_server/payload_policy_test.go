package udp_server

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestFastHandlerLeavesAuthenticatedResponsesToSharedCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ad, signed bool
	}{{name: "ad_without_rrsig", ad: true}, {name: "rrsig_without_ad", signed: true}, {name: "ad_with_rrsig", ad: true, signed: true}} {
		t.Run(tc.name, func(t *testing.T) {
			q := new(dns.Msg)
			q.SetQuestion("signed.example.", dns.TypeA)
			r := new(dns.Msg)
			if err := r.Unpack(makeAnswer(t, q.Question[0].Name, dns.TypeA, 42, 30)); err != nil {
				t.Fatal(err)
			}
			r.AuthenticatedData = tc.ad
			if tc.signed {
				r.Answer = append(r.Answer, &dns.RRSIG{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 30}, TypeCovered: dns.TypeA, OrigTtl: 30, SignerName: "example.", Signature: "AA=="})
			}
			payload := mustPack(t, r)
			fc := newNegativePolicyTestFastCache()
			h := &fastHandler{next: staticHandler{payload: payload}, fc: fc, sw: newFastSwitchValue(testSwitchPlugin{value: "on"})}
			returned := h.Handle(context.Background(), q, server.QueryMeta{}, nil)
			if returned == nil || ((*returned)[3]&0x20 != 0) != tc.ad {
				t.Fatal("cache policy changed the upstream response")
			}
			assertFastPolicyCacheState(t, fc, q, payload, false)
			if fc.Store(q.Question[0].Name, dns.TypeA, payload, "", false) {
				t.Fatal("direct store bypassed the authenticated-response policy")
			}
		})
	}
}

func TestFastPayloadClearsStaleAuthenticatedData(t *testing.T) {
	payload := makeAnswer(t, "authenticated.example.", dns.TypeA, 42, 30)
	payload[3] |= 0x20 // The client did not request DO; no RRSIG is present.
	now := time.Now().Unix()
	for _, state := range []string{"fresh", "ttl_governed", "stale"} {
		t.Run(state, func(t *testing.T) {
			// Exercise the defensive wire policy independently of admission:
			// authenticated responses are now rejected by every fast store.
			item := &fastCacheItem{authenticatedUntil: now + 30}
			expire := now + 120
			if state == "ttl_governed" {
				item.authenticatedUntil = now - 1
			}
			if state == "stale" {
				expire = now - 1
			}
			wire := append([]byte(nil), payload...)
			clearFastStaleAD(wire, item, expire, now)
			if (wire[3]&0x20 != 0) != (state == "fresh") {
				t.Fatal("payload retained AD outside its authentication lifetime")
			}
			if payload[3]&0x20 == 0 {
				t.Fatal("reply changed the original payload")
			}
		})
	}
}
