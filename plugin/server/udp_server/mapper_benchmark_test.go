package udp_server

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_mapper"
	"github.com/miekg/dns"
)

// Exercise the source-aware DomainMapper used in production. The older UDP
// benchmarks intentionally use a legacy mapper and cannot measure validation.
func BenchmarkBuildFastBypassRealMapper(b *testing.B) {
	for _, scenario := range []string{"static", "dynamic_valid", "dynamic_expired", "dynamic_compiled_and_hot", "static_regex", "dynamic_valid_regex", "negative_regex"} {
		b.Run(scenario, func(b *testing.B) {
			const name = "mapper-cache.example."
			const domain = "mapper-cache.example"
			rules := []string{"full:" + domain}
			if scenario == "negative_regex" {
				rules = nil
			}
			if strings.HasSuffix(scenario, "_regex") {
				for i := 0; i < 2048; i++ {
					rules = append(rules, fmt.Sprintf("regexp:^noise-%04d[.]example$", i))
				}
			}
			var provider data_provider.RuleExporter = &udpStaticRules{rules: rules}
			var dynamic *udpExpiringHotRules
			if strings.HasPrefix(scenario, "dynamic_") {
				dynamic = &udpExpiringHotRules{domain: domain}
				dynamic.verified.Store(time.Now().UnixMilli())
				if scenario == "dynamic_compiled_and_hot" || strings.HasSuffix(scenario, "_regex") {
					dynamic.rules = rules
				}
				provider = dynamic
			}
			manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"memory": provider})
			v, err := domain_mapper.NewMapper(coremain.NewBP("unified_matcher1", manager), &domain_mapper.Args{
				Rules: []domain_mapper.RuleConfig{{Tag: "memory", Mark: 51}},
			})
			if err != nil {
				b.Fatal(err)
			}
			mapper := v.(*domain_mapper.DomainMapper)
			fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, ttlMax: 30, responseSlots: 16, ruleSlots: 16}, nil)
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{
				"unified_matcher1": mapper,
				"udp_fast_path":    testSwitchPlugin{value: "on"},
			})
			bypass := buildFastBypassWithSourceRuleMatch(coremain.NewBP("udp_mapper_bench", m), fc, nil, 0, server.RequestSourceUser)
			req := makeQueryNoTest(name, dns.TypeA, 0x1234)
			wire := makeAnswerNoTest(name, dns.TypeA, 0x2222, 30)
			buf := make([]byte, len(wire))
			addr := netip.MustParseAddrPort("127.0.0.1:5353")
			copy(buf, req)
			_, _, flags, dset, _, _, snapshot := bypass(len(req), buf, addr)
			q := new(dns.Msg)
			q.SetQuestion(name, dns.TypeA)
			h := &fastHandler{fc: fc, dm: mapper}
			if !h.storeFastResponse(q, server.QueryMeta{PreFastFlags: flags, PreFastDomainSet: dset, PreFastRuleMatch: snapshot}, &wire) {
				b.Fatal("failed to warm cache")
			}
			want := server.FastActionReply
			if scenario == "dynamic_expired" {
				dynamic.verified.Store(time.Now().Add(-2 * time.Hour).UnixMilli())
				want = server.FastActionContinue
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(buf, req)
				action, _, _, _, _, _, _ := bypass(len(req), buf, addr)
				if action != want {
					b.Fatalf("action=%d want=%d", action, want)
				}
			}
		})
	}
}
