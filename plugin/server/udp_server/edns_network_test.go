package udp_server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestFastEDNSResponsePresenceOverUDP(t *testing.T) {
	for _, primeEDNS := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			name := "plain-prime"
			if primeEDNS {
				name = "edns-prime"
			}
			if stale {
				name += "/stale"
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Uint64
				refreshStarted, release := make(chan struct{}), make(chan struct{})
				defer close(release)
				entry := server_handler.NewEntryHandler(server_handler.EntryHandlerOpts{Entry: sequence.ExecutableFunc(func(ctx context.Context, qc *query_context.Context) error {
					if calls.Add(1) == 2 && stale {
						close(refreshStarted)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					r := new(dns.Msg)
					r.SetReply(qc.Q())
					r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: qc.QQuestion().Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 1)}}
					qc.SetResponse(r)
					return nil
				})})
				fc := refreshTestCache()
				h := &fastHandler{fc: fc, next: entry}
				manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"udp_fast_path": testSwitchPlugin{value: "on"}})
				bypass := buildFastBypassWithSourceRuleMatch(coremain.NewBP("edns-network", manager), fc, nil, 0, server.RequestSourceUser)
				conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- server.ServeUDP(conn, h, server.UDPServerOpts{FastBypassWithRuleMatch: bypass}) }()
				defer func() {
					_ = conn.Close()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				client := &dns.Client{Net: "udp", Timeout: time.Second, UDPSize: 4096}
				exchange := func(edns bool, wantStale bool) {
					t.Helper()
					kind := "ordinary"
					if edns {
						kind = "empty-edns"
					}
					q := requestPolicyQuery(kind, "edns-network.example.")
					r, _, err := client.Exchange(q, conn.LocalAddr().String())
					if err != nil {
						t.Fatal(err)
					}
					if (r.IsEdns0() != nil) != edns || len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "192.0.2.1" {
						t.Fatalf("EDNS=%v response has wrong OPT/answer: %v", edns, r)
					}
					if opt := r.IsEdns0(); opt != nil && (opt.UDPSize() != 1200 || opt.Hdr.Ttl != 0 || len(opt.Option) != 0) {
						t.Fatalf("fast OPT differs from the full handler: %v", opt)
					}
					if wantStale && (r.Answer[0].Header().Ttl == 0 || r.Answer[0].Header().Ttl > 5) {
						t.Fatalf("stale reply extended its TTL: %d", r.Answer[0].Header().Ttl)
					}
				}
				exchange(primeEDNS, false)
				if stale {
					item := mustFastCacheItem(t, fc, "edns-network.example.", dns.TypeA)
					atomic.StoreInt64(&item.expire, fastNowUnix()-1)
				}
				// Both the first stale copy and later wire replies adapt to each client.
				exchange(!primeEDNS, stale)
				if stale {
					select {
					case <-refreshStarted:
					case <-time.After(time.Second):
						t.Fatal("stale response did not initiate background refresh")
					}
				}
				exchange(primeEDNS, stale)
				exchange(!primeEDNS, stale)
				wantCalls := uint64(1)
				if stale {
					wantCalls = 2
				}
				if got := calls.Load(); got != wantCalls {
					t.Fatalf("ordinary/empty EDNS interop lost cache hits: calls=%d want=%d", got, wantCalls)
				}
			})
		}
	}
}
