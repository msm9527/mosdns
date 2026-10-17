package cache

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestSignedResponseCacheLifetime(t *testing.T) {
	for _, tc := range []struct {
		name       string
		original   uint32
		expiration time.Duration
		inception  time.Duration
		wantCache  bool
	}{
		{name: "original_ttl", original: 5, expiration: time.Hour, inception: -time.Hour, wantCache: true},
		{name: "signature_expiration", original: 300, expiration: 10 * time.Second, inception: -time.Hour, wantCache: true},
		{name: "expired", original: 30, expiration: -time.Second, inception: -time.Hour},
		{name: "not_yet_valid", original: 30, expiration: time.Hour, inception: time.Minute},
		{name: "zero_original_ttl", expiration: time.Hour, inception: -time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newResponseTTLTestCache()
			defer c.Close()
			qCtx := signedPolicyQuery(t)
			sig := qCtx.R().Answer[1].(*dns.RRSIG)
			sig.OrigTtl = tc.original
			sig.Expiration = uint32(time.Now().Add(tc.expiration).Unix())
			sig.Inception = uint32(time.Now().Add(tc.inception).Unix())
			v, ok := c.saveRespToCache("signed-policy", qCtx)
			if ok != tc.wantCache {
				t.Fatalf("signed response cached = %v, want %v", ok, tc.wantCache)
			}
			if !ok {
				if qCtx.R().Rcode != dns.RcodeSuccess || !qCtx.R().AuthenticatedData {
					t.Fatal("cache policy changed the upstream validation result")
				}
				return
			}
			lifetime := time.Duration(v.expireUnixNano - v.storedUnixNano)
			if lifetime > min(30*time.Second, min(time.Duration(tc.original)*time.Second, tc.expiration)) {
				t.Fatalf("signed lifetime %s exceeds original TTL or signature expiration", lifetime)
			}
			_, expiration, _ := c.backend.Get("signed-policy")
			if expiration.UnixNano() != v.expireUnixNano {
				t.Fatal("signed response entered a lazy backend retention window")
			}
			msg, lazy, _, _ := c.respFromCacheItem(v, 43200, expiredMsgTtl)
			if msg == nil || lazy || msg.Answer[0].Header().Ttl > uint32(lifetime/time.Second) {
				t.Fatal("signed message TTL exceeds original TTL or remaining signature lifetime")
			}
			wire := buildCachedWirePayload(v, 42, false, expiredMsgTtl, v.storedUnixNano, 120, 900, dns.MinMsgSize)
			var udp dns.Msg
			if err := udp.Unpack(wire); err != nil || udp.Answer[0].Header().Ttl > uint32(lifetime/time.Second) {
				t.Fatal("signed UDP TTL exceeds original TTL or remaining signature lifetime")
			}
			stale := *v
			stale.expireUnixNano = time.Now().Add(-time.Second).UnixNano()
			if msg, _, _, _ := c.respFromCacheItem(&stale, 43200, expiredMsgTtl); msg != nil {
				t.Fatal("signed response was returned stale")
			}
			if buildCachedWirePayload(&stale, 42, true, expiredMsgTtl, time.Now().UnixNano(), 120, 900, dns.MinMsgSize) != nil {
				t.Fatal("signed stale response produced a UDP payload")
			}
		})
	}
}

func TestSignedColdResponseHonorsSignatureLifetime(t *testing.T) {
	c := newResponseTTLTestCache()
	defer c.Close()
	upstream := signedPolicyQuery(t).R().Copy()
	upstream.Answer[1].(*dns.RRSIG).Expiration = uint32(time.Now().Add(10 * time.Second).Unix())
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "upstream": &signedPolicyResponse{msg: upstream}})
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("signed-cold", m)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$upstream"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := queryThroughSequence(t, s, "signed.example.")
	if q.R() == nil || q.R().Answer[0].Header().Ttl > 10 || !q.R().AuthenticatedData || q.R().Rcode != dns.RcodeSuccess {
		t.Fatal("cold signed response exceeds signature lifetime or changed the upstream validation result")
	}
	if q.R().Answer[1].(*dns.RRSIG).Signature != upstream.Answer[1].(*dns.RRSIG).Signature {
		t.Fatal("signature data changed")
	}
}

func TestCachedADExpiresWithOriginalTTLAndStale(t *testing.T) {
	c := newResponseTTLTestCache()
	defer c.Close()
	q := testQueryContextWithTTL(t, "authenticated.example.", net.IPv4(1, 2, 3, 4), 30)
	q.R().AuthenticatedData = true
	v, ok := c.saveRespToCache("authenticated-policy", q)
	if !ok {
		t.Fatal("response was not cached")
	}
	for _, state := range []string{"fresh", "ttl_governed", "stale"} {
		t.Run(state, func(t *testing.T) {
			entry := *v
			lazy := state == "stale"
			if state != "fresh" {
				entry.authenticatedUntilUnixNano = time.Now().Add(-time.Second).UnixNano()
			}
			if lazy {
				entry.expireUnixNano = time.Now().Add(-time.Second).UnixNano()
			}
			msg, _, _, _ := c.respFromCacheItem(&entry, 43200, expiredMsgTtl)
			if msg == nil || msg.AuthenticatedData != (state == "fresh") {
				t.Fatal("message AD does not reflect original TTL and stale state")
			}
			wire := buildCachedWirePayload(&entry, 42, lazy, expiredMsgTtl, time.Now().UnixNano(), 120, 900, dns.MinMsgSize)
			var udp dns.Msg
			if err := udp.Unpack(wire); err != nil || udp.AuthenticatedData != (state == "fresh") {
				t.Fatal("wire AD differs from message AD")
			}
			if v.resp[3]&0x20 == 0 {
				t.Fatal("clearing a response AD mutated the stored payload")
			}
		})
	}
}

func signedPolicyQuery(t *testing.T) *query_context.Context {
	t.Helper()
	q := testQueryContextWithTTL(t, "signed.example.", net.IPv4(1, 2, 3, 4), 30)
	q.R().AuthenticatedData = true
	q.R().Answer = append(q.R().Answer, &dns.RRSIG{
		Hdr:         dns.RR_Header{Name: "signed.example.", Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 30},
		TypeCovered: dns.TypeA, Algorithm: 8, Labels: 2, OrigTtl: 30,
		Inception: uint32(time.Now().Add(-time.Hour).Unix()), Expiration: uint32(time.Now().Add(time.Hour).Unix()),
		SignerName: "example.", Signature: "AA==",
	})
	return q
}

type signedPolicyResponse struct{ msg *dns.Msg }

func (r *signedPolicyResponse) Exec(_ context.Context, q *query_context.Context) error {
	q.SetResponse(r.msg.Copy())
	return nil
}
