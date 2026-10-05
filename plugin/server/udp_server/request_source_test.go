package udp_server

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

type sourceCaptureHandler struct {
	meta server.QueryMeta
}

type sourceCaptureMapper struct {
	sources     []server.RequestSource
	legacyCalls int
}

func (m *sourceCaptureMapper) FastMatch(string) ([]uint8, string, bool) {
	m.legacyCalls++
	return nil, "legacy", true
}

func (m *sourceCaptureMapper) FastMatchWithSource(_ string, source server.RequestSource) ([]uint8, string, bool) {
	m.sources = append(m.sources, source)
	return []uint8{39}, "current", true
}

func TestFastBypassPropagatesListenerSource(t *testing.T) {
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		t.Run(source.String(), func(t *testing.T) {
			mapper := &sourceCaptureMapper{}
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{
				"unified_matcher1": mapper,
				"udp_fast_path":    testSwitchPlugin{value: "on"},
			})
			fc := newFastCache(fastCacheConfig{internalTTL: time.Minute, responseSlots: 16, ruleSlots: 16}, nil)
			bypass := buildFastBypassWithSource(coremain.NewBP("source_test", m), fc, nil, 0, source)
			req := makeQuery(t, "source.example.", dns.TypeA, 1)
			_, _, flags, dset, matched, _ := bypass(len(req), req, netip.MustParseAddrPort("127.0.0.1:7766"))
			if mapper.legacyCalls != 0 || len(mapper.sources) != 1 || mapper.sources[0] != source {
				t.Fatalf("mapper calls legacy=%d sources=%v, want %v", mapper.legacyCalls, mapper.sources, source)
			}
			if !matched || dset != "current" || flags&(1<<39) == 0 {
				t.Fatalf("lost fast match flags=%d set=%q matched=%v", flags, dset, matched)
			}
		})
	}
}

func TestStoreFastResponseLookupAvoidsUserDemand(t *testing.T) {
	mapper := &sourceCaptureMapper{}
	fc := newFastCache(fastCacheConfig{internalTTL: time.Minute, responseSlots: 16, ruleSlots: 16}, nil)
	h := &fastHandler{dm: mapper, fc: fc}
	q := new(dns.Msg)
	q.SetQuestion("store.example.", dns.TypeA)
	wire := makeAnswerWithIP(t, q.Question[0].Name, dns.TypeA, q.Id, 30, "1.1.1.1")
	if !h.storeFastResponse(q, server.QueryMeta{RequestSource: server.RequestSourceUser}, &wire) {
		t.Fatal("expected successful store")
	}
	if mapper.legacyCalls != 0 || len(mapper.sources) != 1 || mapper.sources[0] != server.RequestSourcePrewarm {
		t.Fatalf("cache ownership lookup invoked user demand legacy=%d sources=%v", mapper.legacyCalls, mapper.sources)
	}
	if got := mustFastCacheItem(t, fc, q.Question[0].Name, dns.TypeA).domainSet; got != "current" {
		t.Fatalf("stored domain set = %q, want current", got)
	}
}

func TestBackgroundFastMatchDoesNotCallLegacyMapper(t *testing.T) {
	m := testDomainMapperPlugin{tag: "legacy", match: true}
	for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		if _, _, matched := fastMatchWithSource(m, "source.example.", source); matched {
			t.Fatalf("background %v used legacy mapper", source)
		}
	}
	if _, dset, matched := fastMatchWithSource(m, "source.example.", server.RequestSourceUser); !matched || dset != "legacy" {
		t.Fatal("user legacy mapper compatibility lost")
	}
}

func TestUDPListenerRequestSource(t *testing.T) {
	for _, tc := range []struct {
		entry, option string
		want          server.RequestSource
	}{
		{"sequence_6666", "", server.RequestSourceUser},
		{"sequence_requery", "", server.RequestSourcePrewarm},
		{"sequence_requery_refresh", "user", server.RequestSourceUser},
		{"renamed", "refresh", server.RequestSourceRefresh},
	} {
		t.Run(tc.entry+"_"+tc.option, func(t *testing.T) {
			seen := make(chan server.RequestSource, 1)
			m := coremain.NewTestMosdnsWithPlugins(map[string]any{
				tc.entry: sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
					seen <- qCtx.ServerMeta.RequestSource
					r := new(dns.Msg)
					r.SetReply(qCtx.Q())
					qCtx.SetResponse(r)
					return nil
				}),
			})
			var args Args
			if err := utils.WeakDecode(map[string]any{"entry": tc.entry, "listen": "127.0.0.1:0", "request_source": tc.option, "fast_metrics_log_interval": -1, "fast_cache_slots": 16, "fast_rule_cache_slots": 16}, &args); err != nil {
				t.Fatal(err)
			}
			plugin, err := Init(coremain.NewBP("source_test", m), &args)
			if err != nil {
				t.Fatal(err)
			}
			s := plugin.(*UdpServer)
			defer s.Close()
			q := new(dns.Msg)
			q.SetQuestion("source.example.", dns.TypeA)
			client := &dns.Client{Net: "udp", Timeout: time.Second}
			if _, _, err := client.Exchange(q, s.conns[0].LocalAddr().String()); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-seen:
				if got != tc.want {
					t.Fatalf("source = %v, want %v", got, tc.want)
				}
			default:
				t.Fatal("entry did not run")
			}
		})
	}
}

func TestUDPRejectsUnknownRequestSourceBeforeListen(t *testing.T) {
	if _, err := StartServer(nil, &Args{RequestSource: "USER"}); err == nil {
		t.Fatal("accepted unknown request_source")
	}
}

func (h *sourceCaptureHandler) Handle(_ context.Context, _ *dns.Msg, meta server.QueryMeta, _ func(*dns.Msg) (*[]byte, error)) *[]byte {
	h.meta = meta
	return nil
}

func TestInternalRefreshReplacesSourceAndClearsFastMatch(t *testing.T) {
	next := &sourceCaptureHandler{}
	h := &fastHandler{next: next}
	q := new(dns.Msg)
	q.SetQuestion("refresh.example.", dns.TypeA)
	meta := server.QueryMeta{
		RequestSource:        server.RequestSourceUser,
		ClientAddr:           netip.MustParseAddr("127.0.0.1"),
		FromUDP:              true,
		ServerName:           "main",
		PreFastStaleRefresh:  true,
		PreFastDomainMatched: true,
		PreFastDomainSet:     "expired-hot-rule",
		PreFastFlags:         1 << 39,
		PreFastRuleMatch:     server.FastRuleMatchMeta{Known: true, Flags: 1 << 51, DomainSet: "expired-hot-rule", Matched: true},
	}
	h.refreshExpiredCache(context.Background(), q, meta, nil)
	got := next.meta
	if got.RequestSource != server.RequestSourceRefresh {
		t.Fatalf("source = %v, want refresh", got.RequestSource)
	}
	if got.PreFastStaleRefresh || got.PreFastDomainMatched || got.PreFastDomainSet != "" || got.PreFastFlags != 0 || got.PreFastRuleMatch != (server.FastRuleMatchMeta{}) {
		t.Fatalf("refresh retained old fast metadata %+v", got)
	}
	if got.ClientAddr != meta.ClientAddr || got.FromUDP != meta.FromUDP || got.ServerName != meta.ServerName {
		t.Fatalf("refresh lost client metadata %+v", got)
	}
}
