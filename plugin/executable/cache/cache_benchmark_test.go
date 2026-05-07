package cache

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

func BenchmarkCacheGetRespFromCache(b *testing.B) {
	c := NewCache(&Args{Size: 1024}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(nilSafeTB{b}, "bench.example.", net.IPv4(1, 1, 1, 1))
	if _, ok := c.saveRespToCache("bench-key", qCtx); !ok {
		b.Fatal("expected response to be cached")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, lazy, _ := getRespFromCache("bench-key", c.backend, 0, expiredMsgTtl)
		if resp == nil || lazy {
			b.Fatal("unexpected cache miss")
		}
	}
}

func BenchmarkCacheExecL1Hit(b *testing.B) {
	c := NewCache(&Args{Size: 1024, ExitOnHit: true}, Opts{})
	defer c.Close()

	name := "bench-exec-l1.example."
	msgKey := cacheKeyForQuery(nilSafeTB{b}, name)
	seedCtx := testQueryContext(nilSafeTB{b}, name, net.IPv4(1, 1, 1, 1))
	cachedItem, ok := c.saveRespToCache(msgKey, seedCtx)
	if !ok {
		b.Fatal("expected response to be cached")
	}
	k := key(msgKey)
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qCtx := testQueryContext(nilSafeTB{b}, name, net.IPv4(127, 0, 0, 1))
		qCtx.ServerMeta = server.QueryMeta{FromUDP: true, ClientAddr: netip.MustParseAddr("127.0.0.1")}
		err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{})
		if err != nil && err != sequence.ErrExit {
			b.Fatal(err)
		}
	}
}

func BenchmarkCacheExecL1HitParallel(b *testing.B) {
	c := NewCache(&Args{Size: 1024, ExitOnHit: true}, Opts{})
	defer c.Close()

	name := "bench-exec-l1-parallel.example."
	msgKey := cacheKeyForQuery(nilSafeTB{b}, name)
	seedCtx := testQueryContext(nilSafeTB{b}, name, net.IPv4(1, 1, 1, 1))
	cachedItem, ok := c.saveRespToCache(msgKey, seedCtx)
	if !ok {
		b.Fatal("expected response to be cached")
	}
	k := key(msgKey)
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			qCtx := testQueryContext(nilSafeTB{b}, name, net.IPv4(127, 0, 0, 1))
			qCtx.ServerMeta = server.QueryMeta{FromUDP: true, ClientAddr: netip.MustParseAddr("127.0.0.1")}
			err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{})
			if err != nil && err != sequence.ErrExit {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkCacheWriteDump(b *testing.B) {
	c := NewCache(&Args{Size: 16 * dumpBlockSize}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(nilSafeTB{b}, "dump.example.", net.IPv4(8, 8, 4, 4))
	for i := 0; i < 1024; i++ {
		_, _ = c.saveRespToCache(strconv.Itoa(i)+"-dump-key", qCtx)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := new(bytes.Buffer)
		if _, err := c.writeDump(buf); err != nil {
			b.Fatal(err)
		}
	}
}

type nilSafeTB struct {
	b *testing.B
}

func (tb nilSafeTB) Helper() {
	tb.b.Helper()
}

func (tb nilSafeTB) Fatal(args ...interface{}) {
	tb.b.Fatal(args...)
}
