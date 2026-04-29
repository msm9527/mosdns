package ecs_handler

import (
	"context"
	"net/netip"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func TestQuickSetupOldECSAutoUsesPublicClientAddress(t *testing.T) {
	handler, err := QuickSetupOldECS(testBQ{logger: zap.NewNop()}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	ecsHandler, ok := handler.(*ECSHandler)
	if !ok {
		t.Fatalf("unexpected handler type %T", handler)
	}

	qCtx := testQueryContext("auto.example.")
	qCtx.ServerMeta.ClientAddr = netip.MustParseAddr("223.5.5.5")
	if err := ecsHandler.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatal(err)
	}

	ecs := findECS(t, qCtx.QOpt())
	if ecs.Family != 1 {
		t.Fatalf("expected IPv4 ECS family, got %d", ecs.Family)
	}
	if got := ecs.Address.String(); got != "223.5.5.5" {
		t.Fatalf("unexpected ECS address %q", got)
	}
	if ecs.SourceNetmask != 24 {
		t.Fatalf("unexpected ECS mask %d", ecs.SourceNetmask)
	}
}

func TestQuickSetupOldECSAutoSkipsPrivateClientAddress(t *testing.T) {
	handler, err := QuickSetupOldECS(testBQ{logger: zap.NewNop()}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	ecsHandler := handler.(*ECSHandler)

	qCtx := testQueryContext("auto-private.example.")
	qCtx.ServerMeta.ClientAddr = netip.MustParseAddr("192.168.1.20")
	if err := ecsHandler.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatal(err)
	}

	for _, option := range qCtx.QOpt().Option {
		if option.Option() == dns.EDNS0SUBNET {
			t.Fatal("expected private client address not to be used as ECS")
		}
	}
}

func TestQuickSetupOldECSPresetStillWorks(t *testing.T) {
	handler, err := QuickSetupOldECS(testBQ{logger: zap.NewNop()}, "2408:8888::8")
	if err != nil {
		t.Fatal(err)
	}
	ecsHandler := handler.(*ECSHandler)

	qCtx := testQueryContext("preset.example.")
	if err := ecsHandler.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatal(err)
	}

	ecs := findECS(t, qCtx.QOpt())
	if ecs.Family != 2 {
		t.Fatalf("expected IPv6 ECS family, got %d", ecs.Family)
	}
	if got := ecs.Address.String(); got != "2408:8888::8" {
		t.Fatalf("unexpected ECS address %q", got)
	}
	if ecs.SourceNetmask != 48 {
		t.Fatalf("unexpected ECS mask %d", ecs.SourceNetmask)
	}
}

type testBQ struct {
	logger *zap.Logger
}

func (b testBQ) L() *zap.Logger {
	return b.logger
}

func (b testBQ) Plugin(string) any {
	return nil
}

func (b testBQ) MetricsRegisterer() prometheus.Registerer {
	return nil
}

func (b testBQ) Named(string) sequence.BQ {
	return b
}

func testQueryContext(name string) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	return query_context.NewContext(q)
}

func findECS(t *testing.T, opt *dns.OPT) *dns.EDNS0_SUBNET {
	t.Helper()
	for _, option := range opt.Option {
		if ecs, ok := option.(*dns.EDNS0_SUBNET); ok {
			return ecs
		}
	}
	t.Fatal("expected ECS option")
	return nil
}
