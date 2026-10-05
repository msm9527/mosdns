package query_context

import (
	"testing"

	"github.com/miekg/dns"
)

func TestCacheResponseStaleIsNilSafe(t *testing.T) {
	var ctx *Context
	ctx.SetCacheResponseStale(true)
	ctx.SetCacheResponseStale(false)
	if ctx.CacheResponseStale() {
		t.Fatal("nil context has stale response")
	}
	ctx = &Context{}
	if ctx.CacheResponseStale() {
		t.Fatal("new context has stale response")
	}
	ctx.SetCacheResponseStale(true)
	if !ctx.CacheResponseStale() {
		t.Fatal("stale metadata missing")
	}
	ctx.SetCacheResponseStale(false)
	if ctx.CacheResponseStale() || len(ctx.kv) != 0 {
		t.Fatal("clear retained stale metadata")
	}
}

func TestCacheResponseStaleCopyAndResponseReplacement(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("stale-copy.example.", dns.TypeA)
	ctx := NewContext(q)
	ctx.SetCacheResponseStale(true)
	var dst Context
	ctx.CopyTo(&dst)
	copy := ctx.Copy()
	for _, c := range []*Context{&dst, copy} {
		if !c.CacheResponseStale() {
			t.Fatal("copy lost cache provenance")
		}
		c.SetCacheResponseStale(false)
		if !ctx.CacheResponseStale() {
			t.Fatal("copy clear changed original metadata")
		}
	}
	ctx.SetResponse(new(dns.Msg))
	ctx.SetResponse(nil)
	if !ctx.CacheResponseStale() {
		t.Fatal("response replacement changed cache-owned metadata")
	}
}
