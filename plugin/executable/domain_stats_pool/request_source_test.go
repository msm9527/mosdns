package domain_stats_pool

import (
	"context"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestStatsPoolIgnoresBackgroundInBothExecutors(t *testing.T) {
	old := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() { coremain.MainConfigBaseDir = old })
	for _, fast := range []bool{false, true} {
		pool, err := newDomainStatsPool("top_domains", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		exec := pool.Exec
		if fast {
			exec = pool.GetFastExec()
		}
		for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh, server.RequestSourceUser, server.RequestSourceUnspecified} {
			q := new(dns.Msg)
			q.SetQuestion("rank.example.", dns.TypeA)
			qCtx := query_context.NewContext(q)
			qCtx.ServerMeta.RequestSource = source
			if err := exec(context.Background(), qCtx); err != nil {
				t.Fatal(err)
			}
			if source.IsBackground() && (len(pool.recordChan) != 0 || len(pool.stats) != 0 || pool.dirtyPending.Load()) {
				t.Fatalf("background recorded top traffic source=%v fast=%v", source, fast)
			}
			pool.drainPendingRecords()
		}
		entry := pool.stats[buildEntryKey("rank.example", 0)]
		if entry == nil || entry.Count != 2 || entry.Score != 2 || entry.LastSeenAtUnixMS <= 0 || pool.totalCount != 2 {
			t.Fatalf("user statistics changed fast=%v entry=%+v total=%d", fast, entry, pool.totalCount)
		}
		before := *entry
		pool.dirtyPending.Store(false)
		for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
			q := new(dns.Msg)
			q.SetQuestion("rank.example.", dns.TypeA)
			qCtx := query_context.NewContext(q)
			qCtx.ServerMeta.RequestSource = source
			if err := exec(context.Background(), qCtx); err != nil {
				t.Fatal(err)
			}
			pool.drainPendingRecords()
		}
		if *entry != before || pool.totalCount != 2 || pool.dirtyPending.Load() {
			t.Fatalf("background mutated existing rank fast=%v entry=%+v", fast, entry)
		}
	}
}
