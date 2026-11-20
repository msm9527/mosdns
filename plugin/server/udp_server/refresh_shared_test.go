package udp_server

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	responsecache "github.com/IrineSistiana/mosdns/v5/plugin/executable/cache"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestSharedRefreshBecomesVisibleWithoutFastRetryDelay(t *testing.T) {
	for _, fromUDP := range []bool{false, true} {
		name := "parsed"
		if fromUDP {
			name = "wire"
		}
		t.Run(name, func(t *testing.T) {
			cache := responsecache.NewCache(&responsecache.Args{
				Size: 256, LazyCacheTTL: 43200, LazyStaleTTL: 43200, ClientTTLMin: 120, ClientTTLMax: 900,
			}, responsecache.Opts{})
			defer cache.Close()
			upstream := &stalePolicyUpstream{ready: make(chan struct{}), release: make(chan struct{})}
			defer upstream.releaseOnce()
			manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": cache, "upstream": upstream})
			seq, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("shared-refresh", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$upstream"}})
			if err != nil {
				t.Fatal(err)
			}
			defer seq.Close()
			entry := server_handler.NewEntryHandler(server_handler.EntryHandlerOpts{Entry: seq})
			q := refreshTestQuestion()
			meta := server.QueryMeta{FromUDP: fromUDP}
			seed := entry.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer)
			pool.ReleaseBuf(seed)
			ageSharedPolicyCache(t, cache)
			h := &fastHandler{next: entry, fc: refreshTestCache()}
			old := refreshTestSeed(t, h, q, meta, "1.1.1.1")
			h.refreshExpiredCache(context.Background(), q.Copy(), meta, pool.PackBuffer, old)
			if got, _ := h.fc.findItem(old.hash, old.qname, old.qtype); got != nil {
				t.Fatal("fast retry deadline masks the shared cache's pending refresh")
			}
			select {
			case <-upstream.ready:
			case <-time.After(time.Second):
				t.Fatal("shared cache did not start refreshing")
			}
			// While the upstream is blocked, the shared layer still serves short
			// stale answers and coalesces repeated requests into the same refresh.
			for range 20 {
				response := readStalePolicyResponse(t, h.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer))
				if response.Answer[0].(*dns.A).A.String() != "1.1.1.1" || response.Answer[0].Header().Ttl > 5 || h.fc.Len() != 0 {
					t.Fatal("pending shared refresh lost stale availability or renewed fast cache")
				}
			}
			if calls := upstream.calls.Load(); calls != 2 {
				t.Fatalf("shared refresh fanout: upstream calls=%d, want seed+one refresh", calls)
			}
			upstream.releaseOnce()
			deadline := time.Now().Add(time.Second)
			for {
				response := readStalePolicyResponse(t, h.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer))
				if response.Answer[0].(*dns.A).A.String() == "2.2.2.2" {
					if h.fc.Len() != 1 {
						t.Fatal("fresh shared result did not restore fast-cache hits")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("new IP waited for the old fast cache's retry window")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
