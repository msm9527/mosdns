package domain_mapper

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

func newTestMapper(defaultMark uint8, defaultTag string, result *MatchResult, names ...string) *DomainMapper {
	dm := &DomainMapper{
		logger:      zap.NewNop(),
		defaultMark: defaultMark,
		defaultTag:  defaultTag,
		runBit:      33,
	}
	m := domain.NewMixMatcher[*MatchResult]()
	for _, name := range names {
		if err := m.Add("domain:"+name, result); err != nil {
			panic(err)
		}
	}
	dm.matcher = atomic.Value{}
	dm.matcher.Store(m)
	return dm
}

func newTestQueryContext(name string) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	return query_context.NewContext(q)
}

func TestDomainMapperExecSetsDefaultMarkAndTagOnMiss(t *testing.T) {
	dm := newTestMapper(17, "未命中", nil)
	qCtx := newTestQueryContext("unknown.example.")

	if err := dm.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	if !qCtx.HasFastFlag(17) {
		t.Fatal("expected default fast mark 17 to be set")
	}
	v, ok := qCtx.GetValue(query_context.KeyDomainSet)
	if !ok {
		t.Fatal("expected default domain set tag to be stored")
	}
	if got := v.(string); got != "未命中" {
		t.Fatalf("unexpected default domain set tag: %q", got)
	}
}

func TestDomainMapperExecAppliesMatchedMarksAndTag(t *testing.T) {
	dm := newTestMapper(17, "未命中", &MatchResult{
		Marks:      []uint8{7, 8},
		JoinedTags: "灰名单|白名单",
	}, "example.org.")
	qCtx := newTestQueryContext("example.org.")

	if err := dm.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	if !qCtx.HasFastFlag(7) || !qCtx.HasFastFlag(8) {
		t.Fatalf("expected matched fast marks to be set")
	}
	if qCtx.HasFastFlag(17) {
		t.Fatalf("default fast mark should not be set on match")
	}
	v, ok := qCtx.GetValue(query_context.KeyDomainSet)
	if !ok {
		t.Fatal("expected matched domain set tag to be stored")
	}
	if got := v.(string); got != "灰名单|白名单" {
		t.Fatalf("unexpected matched domain set tag: %q", got)
	}
}

func TestDomainMapperExecSkipsWhenRunBitAlreadySet(t *testing.T) {
	dm := newTestMapper(17, "未命中", nil)
	qCtx := newTestQueryContext("unknown.example.")
	qCtx.SetFastFlag(dm.runBit)

	if err := dm.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	if qCtx.HasFastFlag(17) {
		t.Fatal("default fast mark should not be set when run bit already exists")
	}
	if _, ok := qCtx.GetValue(query_context.KeyDomainSet); ok {
		t.Fatal("domain set tag should not be stored when run bit already exists")
	}
}
