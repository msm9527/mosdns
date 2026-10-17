package domain_mapper_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/netlist"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_mapper"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/executable/drop_resp"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/executable/tag_setter"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/client_ip"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/fast_mark"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/has_resp"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/qtype"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/rcode"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/resp_ip"
	"github.com/miekg/dns"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type appleStaticRules struct{ rules []string }

func (r *appleStaticRules) GetRules() ([]string, error) { return r.rules, nil }
func (*appleStaticRules) Subscribe(func())              {}

type appleIPRules struct{ prefix netip.Prefix }

func (p *appleIPRules) Match(a netip.Addr) bool       { return p.prefix.IsValid() && p.prefix.Contains(a) }
func (p *appleIPRules) GetIPMatcher() netlist.Matcher { return p }

type appleRouteRef struct{ target *sequence.Sequence }

func (r *appleRouteRef) Exec(ctx context.Context, q *query_context.Context) error {
	return r.target.Exec(ctx, q)
}

type appleSwitchMatch struct {
	values         map[string]string
	name, expected string
}

func (s *appleSwitchMatch) Match(context.Context, *query_context.Context) (bool, error) {
	return s.values[s.name] == s.expected, nil
}

type appleRouteFixture struct {
	t        *testing.T
	plugins  map[string]any
	switches map[string]string
	routes   map[string]*appleRouteRef
	calls    []string
}

// Execute the real default whitelist, mapper and route YAML in memory. Only
// upstreams, geo-IP membership, pool writes and switches are deterministic;
// no listener, background worker, cache database or network is started.
func newAppleRouteFixture(t *testing.T, withoutAssetRule bool, conflictingRegionSets ...bool) *appleRouteFixture {
	t.Helper()
	f := &appleRouteFixture{t: t, plugins: make(map[string]any), routes: make(map[string]*appleRouteRef), switches: map[string]string{
		"core_mode": "secure", "cn_answer_mode": "realip", "client_proxy_mode": "all",
		"block_response": "on", "block_query_type": "on", "block_ipv6": "off", "ad_block": "off",
	}}
	rules := strings.Split(string(f.configFile("rule", "whitelist.txt")), "\n")
	if withoutAssetRule {
		filtered := make([]string, 0, len(rules))
		for _, rule := range rules {
			if strings.TrimSpace(rule) != "domain:mzstatic.com" {
				filtered = append(filtered, rule)
			}
		}
		rules = filtered
	}
	f.plugins["whitelist"] = &appleStaticRules{rules: rules}
	for _, tag := range []string{"direct_ip", "client_ip_whitelist", "client_ip_blacklist"} {
		f.plugins[tag] = &appleIPRules{}
	}
	f.plugins["geoip_cn"] = &appleIPRules{prefix: netip.MustParsePrefix("111.0.0.0/8")}
	mapperRules := []domain_mapper.RuleConfig{{Tag: "whitelist", Mark: 8, OutputTag: "whitelist"}}
	if len(conflictingRegionSets) > 0 && conflictingRegionSets[0] {
		for tag, mark := range map[string]uint8{"geosite_no_cn": 14, "geosite_cn": 16} {
			f.plugins[tag] = &appleStaticRules{rules: []string{"domain:mzstatic.com"}}
			mapperRules = append(mapperRules, domain_mapper.RuleConfig{Tag: tag, Mark: mark, OutputTag: tag})
		}
	}
	manager := coremain.NewTestMosdnsWithPlugins(f.plugins)
	mapper, err := domain_mapper.NewMapper(coremain.NewBP("unified_matcher1", manager), &domain_mapper.Args{
		DefaultMark: 17, DefaultTag: "unmatched",
		Rules: mapperRules,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.plugins["unified_matcher1"] = mapper
	for _, tag := range []string{"sequence_local", "sequence_local_divert", "domestic"} {
		f.plugins[tag] = f.answer("domestic", "111.132.47.193", "2409:8a20::1", false)
	}
	for _, tag := range []string{"sequence_google", "sequence_google_node_resilient", "sequence_not_in_list_leak_v4", "sequence_not_in_list_leak_v6"} {
		f.plugins[tag] = f.answer("foreign", "23.1.2.3", "2606:4700::1111", false)
	}
	for _, tag := range []string{"sequence_fakeip", "sequence_fakeip_addlist", "sequence_local_fake", "sequence_local_fake_exit"} {
		f.plugins[tag] = f.answer("fake", "28.0.0.20", "f2b0::20", true)
	}
	for _, tag := range []string{"my_realiplist", "my_nov4list", "my_nov6list", "top_domains", "rewrite"} {
		f.plugins[tag] = sequence.ExecutableFunc(func(context.Context, *query_context.Context) error { return nil })
	}
	for _, tag := range []string{
		"sequence_ipv4_known_domain", "sequence_ipv4_realip_finalize", "sequence_ipv4",
		"sequence_ipv6_known_domain", "sequence_ipv6_realip_finalize", "sequence_ipv6",
		"sequence_not_in_list_direct_finalize", "sequence_not_in_list_cn_finalize", "sequence_not_in_list_ipmatch",
		"sequence_not_in_list_noleak_v4", "sequence_not_in_list_noleak_v6", "sequence_other",
		"sequence_common_precheck", "sequence_6666",
	} {
		ref := &appleRouteRef{}
		f.routes[tag], f.plugins[tag] = ref, ref
	}
	for _, file := range []string{"32-main-not-in-list.yaml", "33-main-ipv4v6.yaml", "34-main-entry.yaml"} {
		f.loadRoutes(file)
	}
	for tag, route := range f.routes {
		if route.target == nil {
			t.Fatalf("route %s was not loaded", tag)
		}
	}
	return f
}

func (f *appleRouteFixture) configFile(parts ...string) []byte {
	f.t.Helper()
	path := filepath.Join(append([]string{"..", "..", "..", "config"}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *appleRouteFixture) answer(kind, v4, v6 string, exit bool) sequence.Executable {
	return sequence.ExecutableFunc(func(_ context.Context, q *query_context.Context) error {
		f.calls = append(f.calls, kind)
		r := new(dns.Msg)
		r.SetReply(q.Q())
		h := dns.RR_Header{Name: q.Q().Question[0].Name, Rrtype: q.Q().Question[0].Qtype, Class: dns.ClassINET, Ttl: 60}
		switch h.Rrtype {
		case dns.TypeA:
			r.Answer = []dns.RR{&dns.A{Hdr: h, A: net.ParseIP(v4).To4()}}
		case dns.TypeAAAA:
			r.Answer = []dns.RR{&dns.AAAA{Hdr: h, AAAA: net.ParseIP(v6)}}
		}
		q.SetResponse(r)
		if exit {
			return sequence.ErrExit
		}
		return nil
	})
}

func (f *appleRouteFixture) loadRoutes(file string) {
	f.t.Helper()
	var doc struct {
		Policies []struct {
			Name string    `yaml:"name"`
			Args yaml.Node `yaml:"args"`
		} `yaml:"policies"`
	}
	if err := yaml.Unmarshal(f.configFile("sub_config", file), &doc); err != nil {
		f.t.Fatal(err)
	}
	bq := sequence.NewBQ(func(tag string) any { return f.plugins[tag] }, nil, zap.NewNop())
	for _, policy := range doc.Policies {
		ref := f.routes[policy.Name]
		if ref == nil {
			continue
		}
		var raw []map[string]any
		if err := policy.Args.Decode(&raw); err != nil {
			f.t.Fatal(err)
		}
		var args []sequence.RuleArgs
		for _, rule := range raw {
			a := sequence.RuleArgs{Exec: rule["exec"]}
			switch matches := rule["matches"].(type) {
			case string:
				a.Matches = []string{matches}
			case []any:
				for _, match := range matches {
					a.Matches = append(a.Matches, match.(string))
				}
			}
			for i, match := range a.Matches {
				if !strings.HasPrefix(match, "switch ") {
					continue
				}
				parts := strings.SplitN(strings.Trim(strings.TrimPrefix(match, "switch "), "'\""), ":", 2)
				if len(parts) != 2 {
					f.t.Fatalf("invalid switch %s", match)
				}
				// Bind an ordinary matcher instead of registering a global switch
				// implementation or opening the switch persistence store.
				tag := fmt.Sprintf("apple_test_switch_%d", len(f.plugins))
				f.plugins[tag] = &appleSwitchMatch{values: f.switches, name: parts[0], expected: parts[1]}
				a.Matches[i] = "$" + tag
			}
			args = append(args, a)
		}
		seq, err := sequence.NewSequence(bq, args)
		if err != nil {
			f.t.Fatalf("%s: %v", policy.Name, err)
		}
		ref.target = seq
		f.t.Cleanup(func() { _ = seq.Close() })
	}
}

func (f *appleRouteFixture) query(t *testing.T, name string, qtype uint16) *dns.Msg {
	t.Helper()
	f.calls = nil
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	ctx := query_context.NewContext(q)
	ctx.ServerMeta.ClientAddr = netip.MustParseAddr("192.0.2.10")
	if err := f.routes["sequence_6666"].Exec(context.Background(), ctx); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	r := ctx.ResponseMsg()
	if r == nil || r.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected response %+v", r)
	}
	return r
}

func TestAppleAssetsUseDomesticRoute(t *testing.T) {
	f := newAppleRouteFixture(t, false)
	for _, test := range []struct {
		name string
		path string
	}{
		{"apps.apple.com.", "domestic"}, {"bag.itunes.apple.com.", "domestic"},
		{"is1-ssl.mzstatic.com.", "domestic"}, {"apps.mzstatic.com.", "domestic"},
		{"mzstatic.com.", "domestic"}, {"IS1-SSL.MZSTATIC.COM.", "domestic"},
		{"evil-mzstatic.com.", "foreign,fake"}, {"mzstatic.com.evil.example.", "foreign,fake"},
	} {
		for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
			t.Run(test.name+dns.TypeToString[qtype], func(t *testing.T) {
				r := f.query(t, test.name, qtype)
				if got := strings.Join(f.calls, ","); got != test.path {
					t.Fatalf("route=%s want=%s", got, test.path)
				}
				if len(r.Answer) != 1 || r.Answer[0].Header().Rrtype != qtype {
					t.Fatalf("unexpected answer %v", r.Answer)
				}
			})
		}
	}
}

func TestAppleMissingAssetRuleTakesForeignFirst(t *testing.T) {
	f := newAppleRouteFixture(t, true)
	for _, name := range []string{"is1-ssl.mzstatic.com.", "apps.mzstatic.com."} {
		for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
			t.Run(name+dns.TypeToString[qtype], func(t *testing.T) {
				f.query(t, name, qtype)
				if got := strings.Join(f.calls, ","); got != "foreign,fake" {
					t.Fatalf("missing asset rule took %s", got)
				}
			})
		}
	}
}

func TestAppleAssetsRespectTypeBlocks(t *testing.T) {
	f := newAppleRouteFixture(t, false)
	f.switches["block_ipv6"] = "on"
	for _, name := range []string{"apps.apple.com.", "is1-ssl.mzstatic.com."} {
		for _, qtype := range []uint16{dns.TypeAAAA, dns.TypeHTTPS} {
			t.Run(name+dns.TypeToString[qtype], func(t *testing.T) {
				r := f.query(t, name, qtype)
				if len(r.Answer) != 0 || len(f.calls) != 0 {
					t.Fatalf("blocked type reached upstream: calls=%v response=%v", f.calls, r)
				}
			})
		}
	}
}

// Online CN and non-CN sets can both contain Apple assets. The explicit
// whitelist must win over that conflict.
func TestAppleAssetsWhitelistWinsRegionConflict(t *testing.T) {
	for _, withoutRule := range []bool{false, true} {
		t.Run(fmt.Sprintf("without_asset_rule=%v", withoutRule), func(t *testing.T) {
			f := newAppleRouteFixture(t, withoutRule, true)
			for _, name := range []string{"is1-ssl.mzstatic.com.", "apps.mzstatic.com."} {
				for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
					t.Run(name+dns.TypeToString[qtype], func(t *testing.T) {
						f.query(t, name, qtype)
						want := "domestic"
						if withoutRule {
							want = "fake"
						}
						if got := strings.Join(f.calls, ","); got != want {
							t.Fatalf("conflicting region sets routed %s want %s", got, want)
						}
					})
				}
			}
		})
	}
}
