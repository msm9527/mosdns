package sequence

import (
	"context"
	"strings"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

func TestChainWalker_ReturnsError_WhenNodeHasNoExecutable(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	qCtx := query_context.NewContext(q)

	w := NewChainWalker([]*ChainNode{
		{PluginName: "invalid_node"},
	}, nil, nil)

	err := w.ExecNext(context.Background(), qCtx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "has no executable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
