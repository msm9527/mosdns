package sequence

import (
	"context"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

func TestChainWalker_NoOp_WhenNodeHasNoExecutable(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	qCtx := query_context.NewContext(q)

	n := &ChainNode{PluginName: "invalid_node"}
	w := NewChainWalker([]instruction{
		{node: n},
	}, []*ChainNode{n}, nil, nil)

	if err := w.ExecNext(context.Background(), qCtx); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}
