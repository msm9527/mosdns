package domain_memory_pool

import (
	"context"
	"os"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_mapper"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/fast_mark"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/qtype"
	"github.com/miekg/dns"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Run the real mapper and the default main/refresh negative prechecks in memory.
// The upstream has recovered to a valid address; no listeners or network run.
func assertNegativeRouteAnswer(t *testing.T, pool *domainMemoryPool, qtype uint16, source server.RequestSource, wantAddress bool) {
	t.Helper()
	mark := uint8(2)
	if qtype == dns.TypeAAAA {
		mark = 3
	}
	plugins := map[string]any{pool.pluginTag: pool}
	mapper, err := domain_mapper.NewMapper(coremain.NewBP("negative_test_mapper", coremain.NewTestMosdnsWithPlugins(plugins)), &domain_mapper.Args{
		Rules: []domain_mapper.RuleConfig{{Tag: pool.pluginTag, Mark: mark, OutputTag: "negative"}}, DefaultMark: 17, DefaultTag: "unmatched",
	})
	if err != nil {
		t.Fatal(err)
	}
	plugins["unified_matcher1"] = mapper
	calls := 0
	plugins["recovered_upstream"] = sequence.ExecutableFunc(func(_ context.Context, q *query_context.Context) error {
		calls++
		q.SetResponse(sourceQuery("audiences.me", qtype, source, dns.RcodeSuccess, true).R())
		return nil
	})
	file, name := "34-main-entry.yaml", "sequence_common_precheck"
	if source == server.RequestSourceRefresh {
		file, name = "42-refresh-ipv4v6.yaml", "sequence_requery_refresh"
	}
	b, err := os.ReadFile("../../../config/sub_config/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Policies []struct {
			Name string    `yaml:"name"`
			Args yaml.Node `yaml:"args"`
		} `yaml:"policies"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var args []sequence.RuleArgs
	for _, policy := range doc.Policies {
		if policy.Name != name {
			continue
		}
		var rules []struct {
			Matches []string  `yaml:"matches"`
			Exec    yaml.Node `yaml:"exec"`
		}
		// Decode one rule at a time so later scalar matches and compound execs
		// stay independent of this precheck fixture.
		var raw []yaml.Node
		if err := policy.Args.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) < 6 {
			t.Fatal("negative precheck rules missing")
		}
		for _, node := range raw[:6] {
			rule := struct {
				Matches []string  `yaml:"matches"`
				Exec    yaml.Node `yaml:"exec"`
			}{}
			if err := node.Decode(&rule); err != nil {
				t.Fatal(err)
			}
			rules = append(rules, rule)
		}
		for _, rule := range rules {
			var exec string
			if err := rule.Exec.Decode(&exec); err != nil {
				t.Fatal(err)
			}
			var matches []string
			for _, match := range rule.Matches {
				switch match {
				case "switch 'block_response:on'":
					match = "_true"
				case "switch 'block_query_type:on'", "switch 'block_ipv6:on'":
					match = "_false"
				}
				matches = append(matches, match)
			}
			args = append(args, sequence.RuleArgs{Exec: exec, Matches: matches})
		}
	}
	if len(args) != 6 {
		t.Fatal("negative precheck was not loaded")
	}
	args = append(args, sequence.RuleArgs{Exec: "$recovered_upstream"})
	seq, err := sequence.NewSequence(sequence.NewBQ(func(tag string) any { return plugins[tag] }, nil, zap.NewNop()), args)
	if err != nil {
		t.Fatal(err)
	}
	defer seq.Close()
	q := sourceQuery("audiences.me", qtype, source, -1, false)
	if err := seq.Exec(context.Background(), q); err != nil && err != sequence.ErrExit {
		t.Fatal(err)
	}
	resp := q.ResponseMsg()
	if resp == nil || resp.Rcode != dns.RcodeSuccess || (len(resp.Answer) > 0) != wantAddress || (calls == 1) != wantAddress {
		t.Fatalf("negative route source=%s wantAddress=%v upstream_calls=%d response=%v", source.String(), wantAddress, calls, resp)
	}
}
