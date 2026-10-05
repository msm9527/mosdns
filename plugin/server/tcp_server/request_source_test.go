package tcp_server

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestTCPListenerPropagatesRequestSource(t *testing.T) {
	seen := make(chan server.RequestSource, 1)
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"renamed": sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
			seen <- qCtx.ServerMeta.RequestSource
			r := new(dns.Msg)
			r.SetReply(qCtx.Q())
			qCtx.SetResponse(r)
			return nil
		}),
	})
	var args Args
	if err := utils.WeakDecode(map[string]any{"entry": "renamed", "listen": "127.0.0.1:0", "request_source": "prewarm", "idle_timeout": 720}, &args); err != nil {
		t.Fatal(err)
	}
	plugin, err := Init(coremain.NewBP("source_test", m), &args)
	if err != nil {
		t.Fatal(err)
	}
	s := plugin.(*TcpServer)
	defer s.Close()
	q := new(dns.Msg)
	q.SetQuestion("source.example.", dns.TypeA)
	client := &dns.Client{Net: "tcp", Timeout: time.Second}
	if _, _, err := client.Exchange(q, s.l.Addr().String()); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if got != server.RequestSourcePrewarm {
			t.Fatalf("source = %v, want prewarm", got)
		}
	default:
		t.Fatal("entry did not run")
	}
	if args.IdleTimeout != 720 {
		t.Fatalf("idle_timeout = %d, want 720", args.IdleTimeout)
	}
}

func TestTCPRejectsUnknownRequestSourceBeforeListen(t *testing.T) {
	if _, err := StartServer(nil, &Args{RequestSource: "refresh "}); err == nil {
		t.Fatal("accepted unknown request_source")
	}
}
