package cache_route_tag

import (
	"context"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

func TestCacheRouteTagAppendsDependencyWithoutChangingDisplayTag(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("route-tag.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	qCtx.StoreValue(query_context.KeyDomainSet, "记忆直连")

	p := &CacheRouteTag{tag: "chain:domestic-real"}
	if err := p.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	display, ok := qCtx.GetValue(query_context.KeyDomainSet)
	if !ok || display != "记忆直连" {
		t.Fatalf("display domain set changed: %v %v", display, ok)
	}
	deps, ok := qCtx.GetValue(query_context.KeyCacheDependencySet)
	if !ok || deps != "chain:domestic-real" {
		t.Fatalf("dependency tag = %v, ok=%v", deps, ok)
	}
}

func TestCacheRouteTagDedupeDependency(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("route-tag-dedupe.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)

	p := &CacheRouteTag{tag: "chain:foreign-real"}
	if err := p.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	if err := p.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("second Exec: %v", err)
	}

	deps, ok := qCtx.GetValue(query_context.KeyCacheDependencySet)
	if !ok || deps != "chain:foreign-real" {
		t.Fatalf("dependency tag = %v, ok=%v", deps, ok)
	}
}

func TestCacheRouteTagReplacesPreviousRouteScope(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("route-tag-replace.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	query_context.AppendDependencyTag(qCtx, "policy:rev1")

	first := &CacheRouteTag{tag: "chain:foreign-real"}
	second := &CacheRouteTag{tag: "chain:proxy-fakeip"}
	if err := first.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	if err := second.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("second Exec: %v", err)
	}

	deps, ok := qCtx.GetValue(query_context.KeyCacheDependencySet)
	if !ok || deps != "policy:rev1|chain:proxy-fakeip" {
		t.Fatalf("dependency tag = %v, ok=%v", deps, ok)
	}
}
