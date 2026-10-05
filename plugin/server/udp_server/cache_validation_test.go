package udp_server

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_mapper"
	"github.com/miekg/dns"
)

type udpStaticRules struct{ rules []string }

func (p *udpStaticRules) GetRules() ([]string, error) { return p.rules, nil }
func (*udpStaticRules) Subscribe(func())              {}

type udpExpiringHotRules struct {
	udpStaticRules
	domain   string
	verified atomic.Int64
	dirty    atomic.Bool
	queued   atomic.Bool
	demand   atomic.Uint64
}

func (p *udpExpiringHotRules) SnapshotHotRules() ([]string, error) {
	return []string{"full:" + p.domain}, nil
}
func (*udpExpiringHotRules) HasRuntimeHotRuleValidation() bool   { return true }
func (*udpExpiringHotRules) AllowHotRule(string, time.Time) bool { panic("legacy validation") }
func (p *udpExpiringHotRules) AllowHotRuleWithSource(_ string, now time.Time, source server.RequestSource) bool {
	if !p.dirty.Load() && now.Sub(time.UnixMilli(p.verified.Load())) < time.Hour {
		return true
	}
	if !source.IsBackground() && p.queued.CompareAndSwap(false, true) {
		p.demand.Add(1)
		p.dirty.Store(true)
	}
	return false
}

type udpCountingMapper struct {
	*domain_mapper.DomainMapper
	userCalls atomic.Uint64
}

func (m *udpCountingMapper) FastMatchWithSource(domain string, source server.RequestSource) ([]uint8, string, bool) {
	if source == server.RequestSourceUser {
		m.userCalls.Add(1)
	}
	return m.DomainMapper.FastMatchWithSource(domain, source)
}

func newUDPValidationMapper(t *testing.T, provider data_provider.RuleExporter) *udpCountingMapper {
	t.Helper()
	manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"memory": provider})
	v, err := domain_mapper.NewMapper(coremain.NewBP("unified_matcher1", manager), &domain_mapper.Args{Rules: []domain_mapper.RuleConfig{{Tag: "memory", Mark: 51}}})
	if err != nil {
		t.Fatal(err)
	}
	return &udpCountingMapper{DomainMapper: v.(*domain_mapper.DomainMapper)}
}

func udpValidationBypass(mapper DomainMapperPlugin, fc *fastCache, source server.RequestSource) func(int, []byte, netip.AddrPort) (int, int, uint64, string, bool, bool, server.FastRuleMatchMeta) {
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"unified_matcher1": mapper, "udp_fast_path": testSwitchPlugin{value: "on"}})
	return buildFastBypassWithSourceRuleMatch(coremain.NewBP("udp_validation", m), fc, nil, 0, source)
}

func TestWarmFastCachesRevalidateDynamicRulesWithoutRevisionChange(t *testing.T) {
	for _, cachedResponse := range []bool{false, true} {
		for _, dirty := range []bool{false, true} {
			name := "rule-meta/expired"
			if cachedResponse {
				name = "early-response/expired"
			}
			if dirty {
				name += "/dirty"
			}
			t.Run(name, func(t *testing.T) {
				domain := "warm-validator.example."
				provider := &udpExpiringHotRules{domain: "warm-validator.example"}
				provider.verified.Store(time.Now().UnixMilli())
				mapper := newUDPValidationMapper(t, provider)
				fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
				addr := netip.MustParseAddrPort("127.0.0.1:7767")
				user := udpValidationBypass(mapper, fc, server.RequestSourceUser)
				req := makeQuery(t, domain, dns.TypeA, 1)
				_, _, flags, dset, matched, _, snapshot := user(len(req), append([]byte(nil), req...), addr)
				if flags != 1<<51 || dset != "memory" || !matched {
					t.Fatal("failed to warm valid dynamic metadata")
				}
				if cachedResponse {
					q := new(dns.Msg)
					q.SetQuestion(domain, dns.TypeA)
					wire := makeAnswerWithIP(t, domain, dns.TypeA, 1, 60, "1.1.1.1")
					h := &fastHandler{fc: fc, dm: mapper}
					if !h.storeFastResponse(q, server.QueryMeta{PreFastFlags: flags, PreFastDomainSet: dset, PreFastRuleMatch: snapshot}, &wire) {
						t.Fatal("failed to warm response cache")
					}
				} else {
					// Warm metadata for the other qtype without creating a response entry.
					req := makeQuery(t, domain, dns.TypeAAAA, 1)
					user(len(req), append([]byte(nil), req...), addr)
				}
				revision := mapper.CacheRevisionUint64()
				if dirty {
					provider.dirty.Store(true)
				} else {
					provider.verified.Store(time.Now().Add(-2 * time.Hour).UnixMilli())
				}
				qtype := uint16(dns.TypeAAAA)
				if cachedResponse {
					qtype = dns.TypeA
				}
				for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh, server.RequestSourceUser} {
					bypass := udpValidationBypass(mapper, fc, source)
					req := makeQuery(t, domain, qtype, 2)
					buf := make([]byte, 512)
					copy(buf, req)
					beforeCalls := mapper.userCalls.Load()
					action, _, flags, dset, matched, stale, _ := bypass(len(req), buf, addr)
					if action != server.FastActionContinue || flags&(1<<51) != 0 || dset != "" || matched || stale {
						t.Fatalf("source=%v reused invalid classification action=%d flags=%x dset=%q matched=%v stale=%v", source, action, flags, dset, matched, stale)
					}
					wantDemand := uint64(0)
					if source == server.RequestSourceUser {
						wantDemand = 1
						if mapper.userCalls.Load()-beforeCalls != 1 {
							t.Fatal("user request did not validate exactly once")
						}
					}
					if provider.demand.Load() != wantDemand {
						t.Fatalf("source=%v demand=%d want=%d", source, provider.demand.Load(), wantDemand)
					}
					if mapper.CacheRevisionUint64() != revision {
						t.Fatal("fixture unexpectedly changed matcher revision")
					}
				}
			})
		}
	}
}

func TestSourceAwareStaticResponseCacheRemainsUsable(t *testing.T) {
	domain := "static-cache.example."
	mapper := newUDPValidationMapper(t, &udpStaticRules{rules: []string{"full:static-cache.example"}})
	fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
	q := new(dns.Msg)
	q.SetQuestion(domain, dns.TypeA)
	wire := makeAnswerWithIP(t, domain, dns.TypeA, 1, 60, "1.1.1.1")
	h := &fastHandler{fc: fc, dm: mapper}
	// Response classification and client-policy marks are separate from mapper-owned rules.
	snapshot := server.FastRuleMatchMeta{Known: true, Flags: 1 << 51, DomainSet: "memory", Matched: true}
	if !h.storeFastResponse(q, server.QueryMeta{PreFastFlags: 1<<51 | 1<<48, PreFastDomainSet: "resolved-classification", PreFastRuleMatch: snapshot}, &wire) {
		t.Fatal("failed to store static response")
	}
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh, server.RequestSourceUser} {
		bypass := udpValidationBypass(mapper, fc, source)
		req := makeQuery(t, domain, dns.TypeA, 2)
		buf := make([]byte, 512)
		copy(buf, req)
		beforeCalls := mapper.userCalls.Load()
		action, length, _, dset, _, _, _ := bypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
		if action != server.FastActionReply || length == 0 || dset != "resolved-classification" {
			t.Fatalf("source=%v lost static response cache action=%d set=%q", source, action, dset)
		}
		if source == server.RequestSourceUser && mapper.userCalls.Load()-beforeCalls != 1 {
			t.Fatal("user cache hit did not validate exactly once")
		}
	}
}

func TestResponseStoreKeepsInitialMatcherEvidenceAcrossResolution(t *testing.T) {
	for _, change := range []string{"verification-expired", "rules-replaced"} {
		t.Run(change, func(t *testing.T) {
			domain := "inflight-validator.example."
			provider := &udpExpiringHotRules{domain: "inflight-validator.example"}
			provider.verified.Store(time.Now().UnixMilli())
			mapper := newUDPValidationMapper(t, provider)
			fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
			bypass := udpValidationBypass(mapper, fc, server.RequestSourceRefresh)
			req := makeQuery(t, domain, dns.TypeA, 1)
			_, _, flags, dset, matched, _, snapshot := bypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:7767"))
			if !matched || !snapshot.Known || !snapshot.Matched {
				t.Fatal("initial matcher evidence missing")
			}
			// The route becomes invalid while the initial response is being resolved.
			if change == "verification-expired" {
				provider.verified.Store(time.Now().Add(-2 * time.Hour).UnixMilli())
			} else if err := mapper.ReplaceHotRules("memory", nil); err != nil {
				t.Fatal(err)
			}
			q := new(dns.Msg)
			q.SetQuestion(domain, dns.TypeA)
			wire := makeAnswerWithIP(t, domain, dns.TypeA, 1, 60, "1.1.1.1")
			h := &fastHandler{fc: fc, dm: mapper}
			if !h.storeFastResponse(q, server.QueryMeta{PreFastFlags: flags, PreFastDomainSet: dset, PreFastRuleMatch: snapshot}, &wire) {
				t.Fatal("store failed")
			}
			if got := mustFastCacheItem(t, fc, domain, dns.TypeA).ruleMatch; got != snapshot {
				t.Fatalf("store fabricated new evidence %+v", got)
			}
			buf := make([]byte, 512)
			copy(buf, req)
			action, _, flags, dset, matched, stale, _ := bypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:7767"))
			if action != server.FastActionContinue || flags != 0 || dset != "" || matched || stale || provider.demand.Load() != 0 {
				t.Fatal("accepted stale response origin after resolution")
			}
		})
	}
}

func TestKnownNegativeSnapshotDiffersFromUnknown(t *testing.T) {
	domain := "negative-cache.example."
	mapper := newUDPValidationMapper(t, &udpStaticRules{})
	fc := newFastCache(fastCacheConfig{internalTTL: time.Hour, responseSlots: 16, ruleSlots: 16}, nil)
	bypass := udpValidationBypass(mapper, fc, server.RequestSourceUser)
	req := makeQuery(t, domain, dns.TypeA, 1)
	_, _, _, _, _, _, negative := bypass(len(req), append([]byte(nil), req...), netip.MustParseAddrPort("127.0.0.1:5353"))
	if !negative.Known || negative.Matched {
		t.Fatal("negative was not distinguished from unknown")
	}
	q := new(dns.Msg)
	q.SetQuestion(domain, dns.TypeA)
	wire := makeAnswerWithIP(t, domain, dns.TypeA, 1, 60, "1.1.1.1")
	h := &fastHandler{fc: fc, dm: mapper}
	for _, snapshot := range []server.FastRuleMatchMeta{{}, negative} {
		if !h.storeFastResponse(q, server.QueryMeta{PreFastRuleMatch: snapshot}, &wire) {
			t.Fatal("store failed")
		}
		buf := make([]byte, 512)
		copy(buf, req)
		action, _, _, _, _, _, _ := bypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5353"))
		want := server.FastActionContinue
		if snapshot.Known {
			want = server.FastActionReply
		}
		if action != want {
			t.Fatalf("snapshot=%+v action=%d want=%d", snapshot, action, want)
		}
	}
}
