package udp_server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	responsecache "github.com/IrineSistiana/mosdns/v5/plugin/executable/cache"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestAuthenticatedColdSequenceCannotExtendFastCacheLifetime(t *testing.T) {
	for _, authenticated := range []bool{true, false} {
		name := "ordinary"
		if authenticated {
			name = "authenticated"
		}
		t.Run(name, func(t *testing.T) {
			c := responsecache.NewCache(&responsecache.Args{
				Size: 256, LazyCacheTTL: 43200, LazyStaleTTL: 43200,
				ClientTTLMin: 120, ClientTTLMax: 900,
			}, responsecache.Opts{})
			defer c.Close()
			upstream := &authenticatedPolicyUpstream{authenticated: authenticated}
			manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "upstream": upstream})
			s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("authenticated-path", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$upstream"}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			fc := newNegativePolicyTestFastCache()
			h := &fastHandler{
				next: server_handler.NewEntryHandler(server_handler.EntryHandlerOpts{Entry: s}),
				fc:   fc, sw: newFastSwitchValue(testSwitchPlugin{value: "on"}),
			}
			q := new(dns.Msg)
			q.SetQuestion("authenticated-path.example.", dns.TypeA) // No DO: upstream returns AD without RRSIG.
			for i := 0; i < 2; i++ {
				payload := h.Handle(context.Background(), q.Copy(), server.QueryMeta{FromUDP: true}, packAuthenticatedPolicyResponse)
				if payload == nil {
					t.Fatal("sequence returned no response")
				}
				var response dns.Msg
				err := response.Unpack(*payload)
				pool.ReleaseBuf(payload)
				if err != nil || len(response.Answer) != 1 {
					t.Fatalf("invalid response: %v", err)
				}
				wantTTL := uint32(120)
				if authenticated {
					wantTTL = 30
				}
				if ttl := response.Answer[0].Header().Ttl; ttl > wantTTL || ttl == 0 || (!authenticated && ttl != wantTTL) {
					t.Errorf("response %d TTL = %d, want <= %d (ordinary response keeps client minimum)", i, ttl, wantTTL)
				}
				if response.AuthenticatedData != authenticated || response.Rcode != dns.RcodeSuccess {
					t.Fatal("cache changed the upstream validation result")
				}
			}
			if got := upstream.calls.Load(); got != 1 {
				t.Fatalf("hot response did not use the shared cache: upstream calls = %d", got)
			}
			wantFast := 1
			if authenticated {
				wantFast = 0
			}
			if fc.Len() != wantFast {
				t.Errorf("fast cache size = %d, want %d", fc.Len(), wantFast)
			}
		})
	}
}

type authenticatedPolicyUpstream struct {
	authenticated bool
	calls         atomic.Uint32
}

func (u *authenticatedPolicyUpstream) Exec(_ context.Context, q *query_context.Context) error {
	u.calls.Add(1)
	r := new(dns.Msg)
	r.SetReply(q.Q())
	r.AuthenticatedData = u.authenticated
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Q().Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: []byte{1, 2, 3, 4}}}
	q.SetResponse(r)
	return nil
}

func packAuthenticatedPolicyResponse(msg *dns.Msg) (*[]byte, error) {
	wire, err := msg.Pack()
	return &wire, err
}
