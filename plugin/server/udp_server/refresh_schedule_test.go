package udp_server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestExpiredFastCacheRefreshRetryUsesLastAttempt(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%v", concurrent), func(t *testing.T) {
			name := "refresh-throttle.example."
			fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, staleMax: time.Minute * 5,
				staleRetry: time.Second * 10, responseSlots: 16, ruleSlots: 16}, nil)
			if !fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 1, 60), "", false) {
				t.Fatal("cannot seed cached answer")
			}
			item := mustFastCacheItem(t, fc, name, dns.TypeA)
			now := fastNowUnix()
			atomic.StoreInt64(&item.expire, now-60)
			query := makeQuery(t, name, dns.TypeA, 2)
			wave := func(at int64) int64 {
				var requested atomic.Int64
				var wg sync.WaitGroup
				lookup := func() {
					defer wg.Done()
					buf := make([]byte, 512)
					copy(buf, query)
					_, _, _, _, refresh, _ := fc.replyFromItemAt(item, true, buf, true, false, fastRuleRevision{}, item.hash, at)
					if refresh {
						requested.Add(1)
					}
				}
				for range 64 {
					wg.Add(1)
					if concurrent {
						go lookup()
					} else {
						lookup()
					}
				}
				wg.Wait()
				return requested.Load()
			}
			for _, test := range []struct {
				at   int64
				want int64
			}{{now, 1}, {now + 9, 0}, {now + 10, 1}, {now + 19, 0}, {now + 20, 1}} {
				if got := wave(test.at); got != test.want {
					t.Errorf("at +%ds: requested %d refreshes, want %d", test.at-now, got, test.want)
				}
			}
		})
	}
}

func TestStaleResponseCopyRechecksMaximumAge(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprintf("override=%v", override), func(t *testing.T) {
			name := "stale-window.example."
			fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, staleMax: time.Second * 5,
				domainSetTTL: []fastDomainSetTTLPolicy{{Sets: []string{"longer"}, StaleMax: 60}}, responseSlots: 16, ruleSlots: 16}, nil)
			domainSet := ""
			if override {
				domainSet = "longer"
			}
			fc.Store(name, dns.TypeA, makeAnswer(t, name, dns.TypeA, 1, 60), domainSet, false)
			item := mustFastCacheItem(t, fc, name, dns.TypeA)
			atomic.StoreInt64(&item.expire, fastNowUnix()-30)
			wire := fc.CopyResponse(2, name, dns.TypeA, true)
			if wire != nil {
				defer pool.ReleaseBuf(wire)
			}
			if (wire != nil) != override {
				t.Fatalf("stale copy available=%v, want %v", wire != nil, override)
			}
		})
	}
}

func TestStaleHandlerFallsThroughWhenCapturedWindowHasExpired(t *testing.T) {
	q := refreshTestQuestion()
	h := &fastHandler{fc: refreshTestCache()}
	item := refreshTestSeed(t, h, q, server.QueryMeta{}, "1.1.1.1")
	// The packet worker selected a stale reply before the maximum window
	// elapsed, but the full handler was scheduled after that deadline.
	atomic.StoreInt64(&item.expire, fastNowUnix()-7200)
	var calls atomic.Int64
	h.next = refreshTestHandler{resolve: func(q *dns.Msg, _ server.QueryMeta) (*[]byte, bool) {
		calls.Add(1)
		return refreshTestPack(t, refreshTestResponse(q, "ordinary")), false
	}}
	wire := h.Handle(context.Background(), q, server.QueryMeta{PreFastStaleRefresh: true}, pool.PackBuffer)
	if wire == nil {
		t.Fatal("missing full-chain answer")
	}
	defer pool.ReleaseBuf(wire)
	var response dns.Msg
	if err := response.Unpack(*wire); err != nil {
		t.Fatal(err)
	}
	if got := response.Answer[0].(*dns.A).A.String(); got != "2.2.2.2" || calls.Load() != 1 {
		t.Fatalf("served old address outside stale window: ip=%s calls=%d", got, calls.Load())
	}
}
