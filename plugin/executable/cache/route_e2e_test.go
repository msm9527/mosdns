package cache

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	domainmapper "github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_mapper"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/executable/cache_route_tag"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/executable/tag_setter"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/has_resp"
	"github.com/miekg/dns"
)

type e2eRuleExporter struct {
	rules []string
	allow atomic.Bool
}

func (e *e2eRuleExporter) GetRules() ([]string, error) {
	return append([]string(nil), e.rules...), nil
}

func (e *e2eRuleExporter) Subscribe(func()) {}

func (e *e2eRuleExporter) AllowHotRule(string, time.Time) bool {
	return e.allow.Load()
}

type e2eAnswerExec struct {
	ip atomic.Value
}

func newE2EAnswerExec(initial net.IP) *e2eAnswerExec {
	exec := new(e2eAnswerExec)
	exec.ip.Store(append(net.IP(nil), initial...))
	return exec
}

func (e *e2eAnswerExec) setIP(ip net.IP) {
	e.ip.Store(append(net.IP(nil), ip...))
}

func (e *e2eAnswerExec) Exec(_ context.Context, qCtx *query_context.Context) error {
	q := qCtx.Q()
	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   q.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: e.ip.Load().(net.IP).To4(),
	})
	qCtx.SetResponse(resp)
	return nil
}

func TestRouteChangeEndToEndBypassesCachedResponse(t *testing.T) {
	provider := &e2eRuleExporter{rules: []string{"full:route-e2e.example"}}
	provider.allow.Store(true)
	target := newE2EAnswerExec(net.IPv4(1, 1, 1, 1))
	cachePlugin := NewCache(&Args{Size: 64}, Opts{})
	defer cachePlugin.Close()

	plugins := map[string]any{
		"my_realiprule": provider,
		"cache_main":    cachePlugin,
		"target":        sequence.ExecutableFunc(target.Exec),
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)

	mapperAny, err := domainmapper.NewMapper(coremain.NewBP("unified_matcher1", m), &domainmapper.Args{
		Rules: []domainmapper.RuleConfig{{
			Tag:       "my_realiprule",
			Mark:      11,
			OutputTag: "记忆直连",
		}},
		DefaultTag: "未命中",
	})
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}
	plugins["unified_matcher1"] = mapperAny

	seq, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test_seq", m)), []sequence.RuleArgs{
		{Exec: "$unified_matcher1"},
		{Exec: "$cache_main"},
		{Exec: "$target"},
	})
	if err != nil {
		t.Fatalf("NewSequence: %v", err)
	}

	firstCtx := newRouteE2EContext("route-e2e.example.")
	if err := seq.Exec(context.Background(), firstCtx); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	assertRouteE2EAnswer(t, firstCtx, net.IPv4(1, 1, 1, 1))
	assertRouteE2EDomainSet(t, firstCtx, "记忆直连")

	provider.allow.Store(false)
	target.setIP(net.IPv4(2, 2, 2, 2))

	secondCtx := newRouteE2EContext("route-e2e.example.")
	if err := seq.Exec(context.Background(), secondCtx); err != nil {
		t.Fatalf("second Exec: %v", err)
	}
	assertRouteE2EAnswer(t, secondCtx, net.IPv4(2, 2, 2, 2))
	assertRouteE2EDomainSet(t, secondCtx, "未命中")
}

func TestRouteDependencyEndToEndIsolatesSharedMainCache(t *testing.T) {
	cachePlugin := NewCache(&Args{Size: 64}, Opts{})
	defer cachePlugin.Close()

	domestic := newE2EAnswerExec(net.IPv4(223, 5, 5, 5))
	foreign := newE2EAnswerExec(net.IPv4(8, 8, 8, 8))
	plugins := map[string]any{
		"cache_main": cachePlugin,
		"domestic":   sequence.ExecutableFunc(domestic.Exec),
		"foreign":    sequence.ExecutableFunc(foreign.Exec),
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)

	domesticSeq, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("domestic_seq", m)), []sequence.RuleArgs{
		{Exec: "cache_route_tag chain:domestic-real"},
		{Exec: "tag_setter 未命中"},
		{Exec: "$cache_main"},
		{Matches: []string{"has_resp"}, Exec: "exit"},
		{Exec: "$domestic"},
	})
	if err != nil {
		t.Fatalf("NewSequence(domestic): %v", err)
	}
	foreignSeq, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("foreign_seq", m)), []sequence.RuleArgs{
		{Exec: "cache_route_tag chain:foreign-real"},
		{Exec: "tag_setter 未命中"},
		{Exec: "$cache_main"},
		{Matches: []string{"has_resp"}, Exec: "exit"},
		{Exec: "$foreign"},
	})
	if err != nil {
		t.Fatalf("NewSequence(foreign): %v", err)
	}

	firstDomestic := queryRouteE2EThroughSequence(t, domesticSeq, "shared-cache.example.")
	assertRouteE2EAnswer(t, firstDomestic, net.IPv4(223, 5, 5, 5))
	assertRouteE2EDomainSet(t, firstDomestic, "未命中")

	firstForeign := queryRouteE2EThroughSequence(t, foreignSeq, "shared-cache.example.")
	assertRouteE2EAnswer(t, firstForeign, net.IPv4(8, 8, 8, 8))
	assertRouteE2EDomainSet(t, firstForeign, "未命中")

	domestic.setIP(net.IPv4(223, 5, 5, 6))
	foreign.setIP(net.IPv4(8, 8, 4, 4))

	secondDomestic := queryRouteE2EThroughSequence(t, domesticSeq, "shared-cache.example.")
	assertRouteE2EAnswer(t, secondDomestic, net.IPv4(223, 5, 5, 5))
	secondForeign := queryRouteE2EThroughSequence(t, foreignSeq, "shared-cache.example.")
	assertRouteE2EAnswer(t, secondForeign, net.IPv4(8, 8, 8, 8))
}

func newRouteE2EContext(name string) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = 1
	return query_context.NewContext(q)
}

func queryRouteE2EThroughSequence(t *testing.T, s *sequence.Sequence, name string) *query_context.Context {
	t.Helper()
	qCtx := newRouteE2EContext(name)
	if err := s.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	return qCtx
}

func assertRouteE2EAnswer(t *testing.T, qCtx *query_context.Context, want net.IP) {
	t.Helper()
	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A answer, got %T", resp.Answer[0])
	}
	if !a.A.Equal(want) {
		t.Fatalf("unexpected answer ip: got %s want %s", a.A.String(), want.String())
	}
}

func assertRouteE2EDomainSet(t *testing.T, qCtx *query_context.Context, want string) {
	t.Helper()
	value, ok := qCtx.GetValue(query_context.KeyDomainSet)
	if !ok {
		t.Fatal("expected domain set on query context")
	}
	got, ok := value.(string)
	if !ok {
		t.Fatalf("expected string domain set, got %T", value)
	}
	if got != want {
		t.Fatalf("unexpected domain set: got %q want %q", got, want)
	}
}
