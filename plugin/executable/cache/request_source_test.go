package cache

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

type cacheReplayObservation struct {
	meta         server.QueryMeta
	stale        bool
	preFastFlag  bool
	sequenceFlag bool
	domainSet    any
}

type cacheReplayObserver struct {
	entered chan cacheReplayObservation
	release chan struct{}
}

func (e *cacheReplayObserver) Exec(ctx context.Context, qCtx *query_context.Context) error {
	domainSet, _ := qCtx.GetValue(query_context.KeyDomainSet)
	e.entered <- cacheReplayObservation{qCtx.ServerMeta, responseFromStaleCache(qCtx), qCtx.HasFastFlag(5), qCtx.HasFastFlag(7), domainSet}
	select {
	case <-e.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return (&testResponseExec{ip: net.IPv4(2, 2, 2, 2)}).Exec(ctx, qCtx)
}

func TestCacheReplayClearsInheritedStaleAndKeepsRouting(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer c.Close()
	parent := testQueryContext(t, "copied-stale.example.", net.IPv4(1, 1, 1, 1))
	parent.ServerMeta.RequestSource = server.RequestSourceUser
	parent.SetCacheResponseStale(true)
	parent.StoreValue(query_context.KeyDomainSet, "selected-route")
	parent.SetFastFlag(5)
	parent.SetFastFlag(7)
	observer := &cacheReplayObserver{entered: make(chan cacheReplayObservation, 1), release: make(chan struct{})}
	defer close(observer.release)
	var state *lazyRefreshState
	var started bool
	replay := sequence.RecursiveExecutableFunc(func(_ context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
		state, started = c.ensureLazyUpdate("copied-stale-key", newCacheRouteSnapshot(qCtx, nil), qCtx, next)
		return nil
	})
	manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"replay": replay, "observer": observer})
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", manager)), []sequence.RuleArgs{{Exec: "$replay"}, {Exec: "$observer"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Exec(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("replay did not start")
	}
	var observed cacheReplayObservation
	select {
	case observed = <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("replay did not reach continuation")
	}
	if observed.stale || observed.meta.RequestSource != server.RequestSourceRefresh || observed.domainSet != "selected-route" || !observed.preFastFlag || !observed.sequenceFlag {
		t.Fatalf("replay did not isolate provenance while retaining routing %+v", observed)
	}
	if !parent.CacheResponseStale() || parent.ServerMeta.RequestSource != server.RequestSourceUser {
		t.Fatal("replay changed parent provenance")
	}
	observer.release <- struct{}{}
	select {
	case <-state.done:
	case <-time.After(time.Second):
		t.Fatal("replay did not finish")
	}
	stored, _, _ := c.backend.Get(key("copied-stale-key"))
	if stored == nil || storedDomainSet(stored.domainSet) != "selected-route" {
		t.Fatal("replay lost route when saving fresh response")
	}
}

func TestCacheColdMissKeepsUserProvenance(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()
	observer := &cacheReplayObserver{entered: make(chan cacheReplayObservation, 1), release: make(chan struct{})}
	close(observer.release)
	parent := testQueryContext(t, "cold-user.example.", net.IPv4(1, 1, 1, 1))
	parent.SetResponse(nil)
	parent.ServerMeta.RequestSource = server.RequestSourceUser
	parent.ServerMeta.PreFastFlags = 1 << 5
	parent.SetFastFlag(5)
	manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "observer": observer})
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$observer"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Exec(context.Background(), parent); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	select {
	case observed := <-observer.entered:
		if observed.meta.RequestSource != server.RequestSourceUser || observed.meta.PreFastFlags != 1<<5 || !observed.preFastFlag {
			t.Fatalf("synchronous cold miss changed user provenance %+v", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("cold miss did not reach upstream")
	}
	if !responseHasA(parent.R(), net.IPv4(2, 2, 2, 2)) {
		t.Fatal("cold user response missing")
	}
}

func TestCacheInternalReplayDoesNotRecordUserDemand(t *testing.T) {
	for _, prefetch := range []bool{false, true} {
		name := "stale"
		if prefetch {
			name = "prefetch"
		}
		t.Run(name, func(t *testing.T) {
			c := NewCache(&Args{Size: 64, LazyCacheTTL: 3600, LazyStaleTTL: 3600}, Opts{})
			defer c.Close()
			domain := "cache-replay.example."
			seedStaleCacheEntry(t, c, domain, net.IPv4(1, 1, 1, 1))
			msgKey := cacheKeyForQuery(t, domain)
			if prefetch {
				item, _, _ := c.backend.Get(key(msgKey))
				item.storedUnixNano = time.Now().Add(-time.Minute).UnixNano()
				item.expireUnixNano = time.Now().Add(time.Second).UnixNano()
			}
			observer := &cacheReplayObserver{entered: make(chan cacheReplayObservation, 1), release: make(chan struct{})}
			defer close(observer.release)
			manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "observer": observer})
			s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$observer"}})
			if err != nil {
				t.Fatal(err)
			}
			q := new(dns.Msg)
			q.SetQuestion(domain, dns.TypeA)
			parent := query_context.NewContext(q)
			parent.ServerMeta = server.QueryMeta{RequestSource: server.RequestSourceUser, ClientAddr: netip.MustParseAddr("127.0.0.1"), PreFastFlags: 1 << 5, PreFastDomainSet: "old-hot-rule", PreFastDomainMatched: true, PreFastStaleRefresh: true}
			parent.ServerMeta.PreFastRuleMatch = server.FastRuleMatchMeta{Known: true, Flags: 1 << 5, DomainSet: "old-hot-rule", Matched: true}
			parent.ApplyFastFlags(1 << 5)
			parent.SetFastFlag(7)
			beforeMeta := parent.ServerMeta
			if err := s.Exec(context.Background(), parent); err != nil && !errors.Is(err, sequence.ErrExit) {
				t.Fatal(err)
			}
			var observed cacheReplayObservation
			select {
			case observed = <-observer.entered:
			case <-time.After(time.Second):
				t.Fatal("cache replay did not reach classifier")
			}
			wantSource := server.RequestSourceRefresh
			if prefetch {
				wantSource = server.RequestSourcePrewarm
			}
			if observed.meta.RequestSource != wantSource {
				t.Fatalf("cache replay inherited user demand source=%v want=%v", observed.meta.RequestSource, wantSource)
			}
			if observed.meta.PreFastFlags != 0 || observed.meta.PreFastDomainSet != "" || observed.meta.PreFastDomainMatched || observed.meta.PreFastStaleRefresh || observed.meta.PreFastRuleMatch != (server.FastRuleMatchMeta{}) || !observed.preFastFlag || observed.stale || !observed.sequenceFlag {
				t.Fatalf("cache replay retained expired bypass metadata %+v", observed)
			}
			if observed.meta.ClientAddr != beforeMeta.ClientAddr || parent.ServerMeta != beforeMeta || !parent.HasFastFlag(5) || !parent.HasFastFlag(7) {
				t.Fatal("cache replay changed real parent metadata")
			}
			if parent.ResponsePayload() != nil || parent.CacheResponseStale() == prefetch {
				t.Fatal("parsed TCP cache response lost stale provenance")
			}
			// Wait for the background save before closing its cache resources.
			c.lazyRefreshMu.Lock()
			state := c.lazyRefresh[msgKey]
			c.lazyRefreshMu.Unlock()
			observer.release <- struct{}{}
			select {
			case <-state.done:
			case <-time.After(time.Second):
				t.Fatal("cache replay did not finish")
			}
			stored, _, _ := c.backend.Get(key(msgKey))
			resp, lazy, _, corrupt := respFromCacheItem(stored, 0, expiredMsgTtl)
			if corrupt || lazy || !responseHasA(resp, net.IPv4(2, 2, 2, 2)) {
				t.Fatal("fresh replay response was not cached")
			}
		})
	}
}
