package cache

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestNonRecursiveAndOtherClassesBypassOrdinaryCache(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()
	calls := 0
	upstream := sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
		calls++
		r := new(dns.Msg)
		r.SetReply(qCtx.Q())
		r.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: qCtx.QQuestion().Name, Rrtype: dns.TypeA, Class: qCtx.QQuestion().Qclass, Ttl: 60},
			A:   net.IPv4(192, 0, 2, byte(calls)),
		}}
		qCtx.SetResponse(r)
		return nil
	})
	manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "upstream": upstream})
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("request-semantics", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$upstream"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, kind := range []string{"ordinary", "chaos", "no-rd", "ordinary"} {
		q := new(dns.Msg)
		q.SetQuestion("request-semantics.example.", dns.TypeA)
		if kind == "chaos" {
			q.Question[0].Qclass = dns.ClassCHAOS
		} else if kind == "no-rd" {
			q.RecursionDesired = false
		}
		qCtx := query_context.NewContext(q)
		if err := s.Exec(context.Background(), qCtx); err != nil {
			t.Fatal(err)
		}
		wantLastByte := byte(calls)
		if kind == "ordinary" {
			wantLastByte = 1
		}
		r := qCtx.R()
		if r == nil || r.Question[0].Qclass != q.Question[0].Qclass || r.RecursionDesired != q.RecursionDesired || r.Answer[0].(*dns.A).A.To4()[3] != wantLastByte {
			t.Fatalf("kind=%s returned incompatible response: %v", kind, r)
		}
	}
	if calls != 3 {
		t.Fatalf("upstream calls=%d, want 3", calls)
	}
}

func TestEntryHandlerSharedCacheIsolatesClientDNSSECDemand(t *testing.T) {
	for _, udp := range []bool{false, true} {
		for _, firstDO := range []bool{false, true} {
			name := "tcp"
			if udp {
				name = "udp"
			}
			if firstDO {
				name += "/dnssec-first"
			} else {
				name += "/ordinary-first"
			}
			t.Run(name, func(t *testing.T) {
				// Both variants must fit even when the randomized hashes share a
				// backend shard; eviction is independent of DNSSEC isolation.
				c := NewCache(&Args{Size: 2 * concurrent_map.MapShardSize}, Opts{})
				defer c.Close()
				calls := 0
				upstream := sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
					calls++
					clientDO := qCtx.ClientOpt() != nil && qCtx.ClientOpt().Do()
					if qCtx.QOpt().Do() != clientDO {
						t.Error("client DNSSEC demand was lost before upstream")
					}
					r := new(dns.Msg)
					r.SetReply(qCtx.Q())
					r.Answer = []dns.RR{&dns.A{
						Hdr: dns.RR_Header{Name: qCtx.QQuestion().Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
						A:   net.IPv4(192, 0, 2, 1),
					}}
					if clientDO {
						now := time.Now()
						r.Answer = append(r.Answer, &dns.RRSIG{
							Hdr:         dns.RR_Header{Name: qCtx.QQuestion().Name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 60},
							TypeCovered: dns.TypeA, Algorithm: 8, Labels: 2, OrigTtl: 60,
							Expiration: uint32(now.Add(time.Hour).Unix()), Inception: uint32(now.Add(-time.Hour).Unix()),
							KeyTag: 1234, SignerName: "example.", Signature: "AQID",
						})
					}
					qCtx.SetResponse(r)
					return nil
				})
				manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "upstream": upstream})
				chain, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("entry-dnssec-cache", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$upstream"}})
				if err != nil {
					t.Fatal(err)
				}
				defer chain.Close()
				handler := server_handler.NewEntryHandler(server_handler.EntryHandlerOpts{Entry: chain})
				for _, do := range []bool{firstDO, !firstDO, firstDO, !firstDO} {
					q := new(dns.Msg)
					q.SetQuestion("client-dnssec.example.", dns.TypeA)
					if do {
						q.SetEdns0(1232, true)
					}
					payload := handler.Handle(context.Background(), q, server.QueryMeta{FromUDP: udp}, pool.PackBuffer)
					if payload == nil {
						t.Fatal("missing response")
					}
					var r dns.Msg
					err := r.Unpack(*payload)
					pool.ReleaseBuf(payload)
					if err != nil {
						t.Fatal(err)
					}
					hasSignature := false
					for _, rr := range r.Answer {
						hasSignature = hasSignature || rr.Header().Rrtype == dns.TypeRRSIG
					}
					if hasSignature != do {
						t.Fatalf("client DO=%v reused incompatible signature variant: %v", do, hasSignature)
					}
				}
				if calls != 2 {
					t.Fatalf("upstream calls=%d, want 2 separate cached variants", calls)
				}
			})
		}
	}
}

func TestCacheKeyPreservesClientAndPluginDNSSECDemand(t *testing.T) {
	for _, clientDO := range []bool{false, true} {
		for _, pluginDO := range []bool{false, true} {
			q := new(dns.Msg)
			q.SetQuestion("cache-key-dnssec.example.", dns.TypeA)
			q.SetEdns0(1232, clientDO)
			qCtx := query_context.NewContext(q)
			qCtx.QOpt().SetDo(pluginDO)
			wire, ptr := getMsgKeyBytes(qCtx.Q(), qCtx, false, nil)
			got := len(wire) > 0 && wire[0]&doBit != 0
			releaseKeyBuffer(ptr)
			if got != (clientDO || pluginDO) {
				t.Fatalf("client DO=%v plugin DO=%v key DO=%v", clientDO, pluginDO, got)
			}
		}
	}
}
