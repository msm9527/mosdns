package query_context

import (
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestCopyPreservesRequestSource(t *testing.T) {
	for _, source := range []server.RequestSource{server.RequestSourceUser, server.RequestSourcePrewarm, server.RequestSourceRefresh} {
		q := new(dns.Msg)
		q.SetQuestion("copy.example.", dns.TypeA)
		ctx := NewContext(q)
		ctx.ServerMeta.RequestSource = source
		ctx.ServerMeta.PreFastRuleMatch = server.FastRuleMatchMeta{Known: true, Flags: 1 << 51, DomainSet: "initial-mapper", Matched: true}
		var dst Context
		ctx.CopyTo(&dst)
		if dst.ServerMeta.RequestSource != source || ctx.Copy().ServerMeta.RequestSource != source {
			t.Fatalf("copy lost source %v", source)
		}
		if dst.ServerMeta.PreFastRuleMatch != ctx.ServerMeta.PreFastRuleMatch || ctx.Copy().ServerMeta.PreFastRuleMatch != ctx.ServerMeta.PreFastRuleMatch {
			t.Fatal("copy lost original mapper evidence")
		}
		dst.ServerMeta.PreFastRuleMatch = server.FastRuleMatchMeta{}
		if !ctx.ServerMeta.PreFastRuleMatch.Known {
			t.Fatal("clearing copy changed original evidence")
		}
		dst.ServerMeta.RequestSource = server.RequestSourceUser
		if ctx.ServerMeta.RequestSource != source {
			t.Fatal("copy changed original provenance")
		}
	}
}
