package udp_server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestFastHandlerLeavesSignedResponsesToSharedCache(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("signed.example.", dns.TypeA)
	r := new(dns.Msg)
	if err := r.Unpack(makeAnswer(t, q.Question[0].Name, dns.TypeA, 42, 30)); err != nil {
		t.Fatal(err)
	}
	r.AuthenticatedData = true
	r.Answer = append(r.Answer, &dns.RRSIG{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 30}, TypeCovered: dns.TypeA, OrigTtl: 30, SignerName: "example.", Signature: "AA=="})
	payload := mustPack(t, r)
	fc := newNegativePolicyTestFastCache()
	h := &fastHandler{next: staticHandler{payload: payload}, fc: fc, sw: newFastSwitchValue(testSwitchPlugin{value: "on"})}
	returned := h.Handle(context.Background(), q, server.QueryMeta{}, nil)
	if returned == nil || (*returned)[3]&0x20 == 0 {
		t.Fatal("cache policy changed the upstream response")
	}
	assertFastPolicyCacheState(t, fc, q, payload, false)
	if fc.Store(q.Question[0].Name, dns.TypeA, payload, "", false) {
		t.Fatal("direct store bypassed the signed-response policy")
	}
}

func TestFastCacheClearsStaleAuthenticatedData(t *testing.T) {
	name := "authenticated.example."
	qtype := uint16(dns.TypeA)
	payload := makeAnswer(t, name, qtype, 42, 30)
	payload[3] |= 0x20 // The client did not request DO; no RRSIG is present.
	for _, state := range []string{"fresh", "ttl_governed", "stale"} {
		t.Run(state, func(t *testing.T) {
			fc := newNegativePolicyTestFastCache()
			if !fc.Store(name, qtype, payload, "", false) {
				t.Fatal("unsigned positive response was not cached")
			}
			item := mustFastCacheItem(t, fc, name, qtype)
			if state != "fresh" {
				item.authenticatedUntil = time.Now().Add(-time.Second).Unix()
			}
			if state == "stale" {
				atomic.StoreInt64(&item.expire, time.Now().Add(-time.Second).Unix())
				atomic.StoreUint32(&item.updating, 1)
			}
			buf := make([]byte, len(payload))
			copy(buf, makeQuery(t, name, qtype, 84))
			action, length, _, _, _ := fc.GetOrUpdating(fastQNameHashString(name, qtype), buf, name, qtype, true)
			if action != server.FastActionReply || length == 0 || (buf[3]&0x20 != 0) != (state == "fresh") {
				t.Fatal("fast reply retained AD outside the original TTL or while stale")
			}
			copy := fc.CopyResponse(84, name, qtype, true)
			if copy == nil || ((*copy)[3]&0x20 != 0) != (state == "fresh") {
				t.Fatal("stale-copy reply has an incorrect AD bit")
			}
			pool.ReleaseBuf(copy)
			if item.resp[3]&0x20 == 0 {
				t.Fatal("reply mutated the stored AD bit")
			}
		})
	}
}
