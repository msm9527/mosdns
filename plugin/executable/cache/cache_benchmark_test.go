package cache

import (
	"bytes"
	"net"
	"strconv"
	"testing"
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
