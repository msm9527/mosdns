package domain_mapper

import (
	"context"
	"fmt"
	"strings"
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

func TestDomainMapperExecSkipsWhenFastBypassAlreadyMatched(t *testing.T) {
	dm := newTestMapper(17, "未命中", nil)
	qCtx := newTestQueryContext("unknown.example.")
	qCtx.ServerMeta.PreFastDomainMatched = true

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

func newBenchmarkMapper(ruleCount int) *DomainMapper {
	dm := &DomainMapper{
		logger:      zap.NewNop(),
		defaultMark: 17,
		defaultTag:  "未命中",
	}
	m := domain.NewMixMatcher[*MatchResult]()
	for i := 0; i < ruleCount; i++ {
		rule := fmt.Sprintf("domain:bench-%d.example.org", i)
		res := &MatchResult{
			Marks:      []uint8{11},
			JoinedTags: "订阅直连",
		}
		if err := m.Add(rule, res); err != nil {
			panic(err)
		}
	}
	dm.matcher = atomic.Value{}
	dm.matcher.Store(m)
	return dm
}

func BenchmarkDomainMapperFastMatchHit(b *testing.B) {
	dm := newBenchmarkMapper(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, ok := dm.FastMatch("bench-19999.example.org.")
		if !ok {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkDomainMapperFastMatchMiss(b *testing.B) {
	dm := newBenchmarkMapper(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, ok := dm.FastMatch("unknown-bench.example.org.")
		if ok {
			b.Fatal("expected miss")
		}
	}
}

func BenchmarkDomainMapperExecHit(b *testing.B) {
	dm := newBenchmarkMapper(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qCtx := newTestQueryContext("bench-19999.example.org.")
		if err := dm.Exec(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDomainMapperExecMiss(b *testing.B) {
	dm := newBenchmarkMapper(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qCtx := newTestQueryContext("unknown-bench.example.org.")
		if err := dm.Exec(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}

func TestValidateDomainMapperMarkRejectsReservedBits(t *testing.T) {
	if err := validateDomainMapperMark("default_mark", 39); err == nil {
		t.Fatal("expected reserved fast_mark 39 to be rejected")
	}
	if err := validateDomainMapperMark("default_mark", 48); err == nil {
		t.Fatal("expected reserved fast_mark 48 to be rejected")
	}
}

func TestValidateDomainMapperMarkAcceptsBusinessBits(t *testing.T) {
	for _, mark := range []uint8{0, 7, 17, 50, 63} {
		if err := validateDomainMapperMark("default_mark", mark); err != nil {
			t.Fatalf("mark %d should be accepted, got error: %v", mark, err)
		}
	}
	if err := validateDomainMapperMark("default_mark", 64); err == nil || !strings.Contains(err.Error(), "between 0 and 63") {
		t.Fatalf("expected out-of-range error for 64, got: %v", err)
	}
}
