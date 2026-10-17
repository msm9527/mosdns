package domain_memory_pool

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/domain_set"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/qname"
	"github.com/miekg/dns"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Exercise the actual node negative prechecks and generated domain_set, rather
// than calling the pool's validator directly. The fallback returns a recovered
// address without using listeners or the external network.
func TestNodeQNameRejectsOnlyValidConfirmedNegative(t *testing.T) {
	for _, kind := range []string{"nodenov4", "nodenov6"} {
		for _, state := range []struct {
			name  string
			count int
			age   time.Duration
			dirty bool
		}{
			{"fresh", 2, time.Minute, false},
			{"unconfirmed", 0, time.Minute, false},
			{"dirty", 2, time.Minute, true},
			{"expired", 2, 7 * time.Hour, false},
			{"decayed", 2, 15 * 24 * time.Hour, false},
		} {
			t.Run(kind+"/"+state.name, func(t *testing.T) {
				pool, _, enqueuer := newSourceTestPool(t, kind)
				isolateNegativeTestStore(t, pool)
				qtype, name := negativePoolQType(kind), "audiences.me"
				stamp := time.Now().Add(-state.age).UnixMilli()
				entry := &statEntry{Count: state.count, Score: state.count, Promoted: true, QTypeMask: qtypeToMask(qtype),
					LastSeenAtUnixMS: stamp, LastVerifiedAtUnixMS: stamp, LastDirtyAtUnixMS: stamp, RefreshState: "clean"}
				if state.dirty {
					entry.RefreshState = "dirty"
				}
				pool.stats[buildEntryKey(name, 0)] = entry
				pool.trackEntryCreatedLocked(name)
				pool.replaceActiveHotRules([]string{"full:" + name})
				seq, calls := newNodeQNameTestSequence(t, pool, qtype)
				before := *entry
				for _, source := range []server.RequestSource{server.RequestSourcePrewarm, server.RequestSourceRefresh, server.RequestSourceUser} {
					q := sourceQuery(name, qtype, source, -1, false)
					beforeCalls := *calls
					if err := seq.Exec(context.Background(), q); err != nil && err != sequence.ErrExit {
						t.Fatal(err)
					}
					wantAddress := state.name != "fresh" || source == server.RequestSourceRefresh
					r := q.ResponseMsg()
					if r == nil || r.Rcode != dns.RcodeSuccess || (len(r.Answer) > 0) != wantAddress || (*calls-beforeCalls == 1) != wantAddress {
						t.Fatalf("source=%s wantAddress=%v upstream_calls=%d response=%v", source, wantAddress, *calls-beforeCalls, r)
					}
					if source.IsBackground() && *entry != before {
						t.Fatal("background qname validation changed user activity or demand")
					}
					if entry.Count != state.count || entry.LastSeenAtUnixMS != stamp {
						t.Fatal("matching changed user observation count")
					}
					t.Logf("source=%s state=%s answers=%d upstream_calls=%d", source, state.name, len(r.Answer), *calls-beforeCalls)
				}
				if state.name == "fresh" {
					assertNoSourceRefresh(t, enqueuer)
				} else {
					select {
					case job := <-enqueuer.jobs:
						if job.Domain != name || job.VerifyTag != pool.pluginTag {
							t.Fatalf("wrong node refresh target %+v", job)
						}
					case <-time.After(time.Second):
						t.Fatal("invalid user match did not request node refresh")
					}
				}
			})
		}
	}
}

func newNodeQNameTestSequence(t *testing.T, pool *domainMemoryPool, qtype uint16) (*sequence.Sequence, *int) {
	t.Helper()
	plugins := map[string]any{pool.pluginTag: pool}
	provider, err := domain_set.Init(coremain.NewBP("node_generated", coremain.NewTestMosdnsWithPlugins(plugins)), &domain_set.Args{GeneratedFrom: pool.pluginTag})
	if err != nil {
		t.Fatal(err)
	}
	plugins["my_nodenov4rule"], plugins["my_nodenov6rule"] = provider, provider
	calls := 0
	plugins["recovered_node_upstream"] = sequence.ExecutableFunc(func(_ context.Context, q *query_context.Context) error {
		calls++
		q.SetResponse(sourceQuery("audiences.me", qtype, server.RequestSourceUser, dns.RcodeSuccess, true).R())
		return nil
	})
	b, err := os.ReadFile("../../../config/sub_config/33-main-ipv4v6.yaml")
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
		if policy.Name != "sequence_sbnode" {
			continue
		}
		var rules []yaml.Node
		if err := policy.Args.Decode(&rules); err != nil || len(rules) < 2 {
			t.Fatalf("node prechecks missing: %v", err)
		}
		for _, node := range rules[:2] {
			var rule sequence.RuleArgs
			if err := node.Decode(&rule); err != nil {
				t.Fatal(err)
			}
			args = append(args, rule)
		}
	}
	if len(args) != 2 {
		t.Fatal("node prechecks not loaded")
	}
	args = append(args, sequence.RuleArgs{Exec: "$recovered_node_upstream"})
	seq, err := sequence.NewSequence(sequence.NewBQ(func(tag string) any { return plugins[tag] }, nil, zap.NewNop()), args)
	if err != nil {
		t.Fatalf("load node sequence: %v", err)
	}
	t.Cleanup(func() { _ = seq.Close() })
	return seq, &calls
}
