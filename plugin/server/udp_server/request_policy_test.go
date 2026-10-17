package udp_server

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestFastCacheIsolatesRequestSemantics(t *testing.T) {
	for _, kind := range []string{"ordinary", "empty-edns", "do", "cd", "ad", "ecs", "cookie", "chaos", "no-rd", "edns-version"} {
		t.Run(kind, func(t *testing.T) {
			name := "request-policy.example."
			fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
			fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 1, 60), "", false)
			manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"udp_fast_path": testSwitchPlugin{value: "on"}})
			bypass := buildFastBypass(coremain.NewBP("request-policy", manager), fc, nil, 0)
			q := requestPolicyQuery(kind, name)
			req := mustPack(t, q)
			buf := make([]byte, 4096)
			copy(buf, req)
			action, _, _, _, _, stale := bypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
			eligible := kind == "ordinary" || kind == "empty-edns"
			if (action == server.FastActionReply) != eligible || stale {
				t.Fatalf("query %s reused incompatible response: action=%d stale=%v", kind, action, stale)
			}
			// Special requests must not poison the ordinary fast-cache variant.
			h := &fastHandler{fc: fc}
			response := makeAnswerWithIP(t, name, dns.TypeA, 2, 60, "2.2.2.2")
			if stored := h.storeFastResponse(q, server.QueryMeta{}, &response); stored != eligible {
				t.Fatalf("query %s fast-store=%v, want %v", kind, stored, eligible)
			}
			if !eligible {
				var cached dns.Msg
				if err := cached.Unpack(mustFastCacheItem(t, fc, name, dns.TypeA).resp); err != nil {
					t.Fatal(err)
				}
				if got := cached.Answer[0].(*dns.A).A.String(); got != "1.1.1.1" {
					t.Fatalf("special query overwrote ordinary response with %s", got)
				}
			}
		})
	}
}

func requestPolicyQuery(kind, name string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	switch kind {
	case "empty-edns":
		q.SetEdns0(1232, false)
	case "do":
		q.SetEdns0(1232, true)
	case "cd":
		q.CheckingDisabled = true
	case "ad":
		q.AuthenticatedData = true
	case "ecs":
		q.SetEdns0(1232, false)
		q.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.IPv4(203, 0, 113, 0)}}
	case "cookie":
		q.SetEdns0(1232, false)
		q.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0102030405060708"}}
	case "chaos":
		q.Question[0].Qclass = dns.ClassCHAOS
	case "no-rd":
		q.RecursionDesired = false
	case "edns-version":
		q.SetEdns0(1232, false)
		q.IsEdns0().SetVersion(1)
	}
	return q
}

func TestFastAdmissionKeepsOriginalOptionsAcrossEntryNormalization(t *testing.T) {
	for _, kind := range []string{"ordinary", "empty-edns", "do", "ecs", "cookie", "edns-version"} {
		t.Run(kind, func(t *testing.T) {
			fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
			entry := server_handler.NewEntryHandler(server_handler.EntryHandlerOpts{Entry: sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
				r := new(dns.Msg)
				r.SetReply(qCtx.Q())
				r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: qCtx.QQuestion().Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 1)}}
				qCtx.SetResponse(r)
				return nil
			})})
			h := &fastHandler{fc: fc, next: entry}
			q := requestPolicyQuery(kind, "entry-options.example.")
			wire := h.Handle(context.Background(), q, server.QueryMeta{FromUDP: true}, pool.PackBuffer)
			if wire == nil {
				t.Fatal("entry did not return a response")
			}
			pool.ReleaseBuf(wire)
			if kind != "do" && !fastRequestCacheable(q) {
				t.Fatal("fixture did not exercise in-place OPT normalization")
			}
			eligible := kind == "ordinary" || kind == "empty-edns"
			if (fc.Len() != 0) != eligible {
				t.Fatalf("original %s request was reclassified after OPT normalization: fast entries=%d", kind, fc.Len())
			}
		})
	}
}

func TestFastCacheHonorsClientUDPSize(t *testing.T) {
	name := "large-response.example."
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	r := new(dns.Msg)
	r.SetReply(q)
	for i := 0; i < 40; i++ {
		r.Answer = append(r.Answer, &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, byte(i+1))})
	}
	r.Compress = true
	response := mustPack(t, r)
	if len(response) <= 512 || len(response) > 1232 {
		t.Fatalf("invalid size fixture: %d", len(response))
	}
	for _, udpSize := range []int{0, 128, 512, 1232} {
		fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
		fc.Store(name, dns.TypeA, response, "", false)
		manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"udp_fast_path": testSwitchPlugin{value: "on"}})
		bypass := buildFastBypass(coremain.NewBP("udp-size", manager), fc, nil, 0)
		query := q.Copy()
		if udpSize > 0 {
			query.SetEdns0(uint16(udpSize), false)
		}
		req := mustPack(t, query)
		buf := make([]byte, 4096)
		copy(buf, req)
		action, size, _, _, _, _ := bypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
		if (action == server.FastActionReply) != (udpSize == 1232) {
			t.Fatalf("udp_size=%d action=%d returned=%d", udpSize, action, size)
		}
	}
}

func TestFastRequestRejectsMalformedOrExtendedPackets(t *testing.T) {
	q := requestPolicyQuery("ordinary", "packet-policy.example.")
	ordinary := mustPack(t, q)
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"trailing-data", func(b []byte) []byte { return append(b, 0) }},
		{"unannounced-additional", func(b []byte) []byte { b[11] = 1; return b }},
		{"answer-count", func(b []byte) []byte { b[7] = 1; return b }},
		{"authority-count", func(b []byte) []byte { b[9] = 1; return b }},
		{"reserved-flag", func(b []byte) []byte { b[3] = 0x40; return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.edit(append([]byte(nil), ordinary...))
			if _, ok := parseFastQuestionMeta(len(wire), wire); ok {
				t.Fatal("extended packet accepted by simple fast-cache key")
			}
		})
	}
	// The full handler still receives every ineligible request.
	fc := newFastCache(fastCacheConfig{responseSlots: 16, ruleSlots: 16}, nil)
	called := false
	h := &fastHandler{fc: fc, next: requestPolicyHandler{call: func() { called = true }}}
	h.Handle(context.Background(), requestPolicyQuery("do", "packet-policy.example."), server.QueryMeta{}, nil)
	if !called || fc.Len() != 0 {
		t.Fatal("special request was not passed to full handler")
	}
}

type requestPolicyHandler struct{ call func() }

func (h requestPolicyHandler) Handle(context.Context, *dns.Msg, server.QueryMeta, func(*dns.Msg) (*[]byte, error)) *[]byte {
	h.call()
	return nil
}
