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

func TestAuthenticatedResponseCacheLifetime(t *testing.T) {
	for _, ttl := range []uint32{0, 30} {
		c := newResponseTTLTestCache()
		defer c.Close()
		q := testQueryContextWithTTL(t, "authenticated.example.", net.IPv4(1, 2, 3, 4), ttl)
		q.R().AuthenticatedData = true // No RRSIG: the client did not request DO.
		q.StoreValue(query_context.KeyDomainSet, "高变化域名")
		v, ok := c.saveRespToCache("authenticated-policy", q)
		if ok != (ttl != 0) {
			t.Fatalf("AD response with TTL %d cached = %v", ttl, ok)
		}
		if !ok {
			continue
		}
		_, expiration, _ := c.backend.Get("authenticated-policy")
		if !v.dnssec || v.expireUnixNano-v.storedUnixNano != int64(30*time.Second) || expiration.UnixNano() != v.expireUnixNano {
			t.Fatal("AD response entered a client-minimum or lazy retention window")
		}
		msg, lazy, _, _ := c.respFromCacheItem(v, 43200, expiredMsgTtl)
		if msg == nil || lazy || !msg.AuthenticatedData || msg.Answer[0].Header().Ttl > ttl {
			t.Fatal("AD message lost authentication or exceeded original TTL")
		}
		wire := buildCachedWirePayload(v, 42, false, expiredMsgTtl, v.storedUnixNano+int64(25*time.Second), 120, 900, dns.MinMsgSize)
		var udp dns.Msg
		if err := udp.Unpack(wire); err != nil || !udp.AuthenticatedData || udp.Answer[0].Header().Ttl > 5 {
			t.Fatal("AD wire TTL was raised above the remaining original TTL")
		}
		stale := *v
		stale.expireUnixNano = time.Now().Add(-time.Second).UnixNano()
		if msg, _, _, _ := c.respFromCacheItem(&stale, 43200, expiredMsgTtl); msg != nil {
			t.Fatal("expired AD response was returned stale")
		}
		if buildCachedWirePayload(&stale, 42, true, expiredMsgTtl, time.Now().UnixNano(), 120, 900, dns.MinMsgSize) != nil {
			t.Fatal("expired AD response produced a stale wire payload")
		}
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
