package fastforward

import (
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

func TestForwardReloadControlConfigDoesNotApplyGlobalSocks5ToDNSUpstreams(t *testing.T) {
	f := &Forward{
		baseArgs: &Args{
			Upstreams: []UpstreamConfig{
				{Tag: "udp", Addr: "udp://223.5.5.5"},
				{Tag: "doh", Addr: "https://dns.alidns.com/dns-query", Socks5: "127.0.0.1:7891"},
			},
			Concurrent: 2,
		},
		logger:     zap.NewNop(),
		pluginTag:  "forward",
		metricsTag: "forward",
	}

	if err := f.ReloadControlConfig(&coremain.GlobalOverrides{Socks5: "127.0.0.1:1080"}, nil); err != nil {
		t.Fatalf("ReloadControlConfig: %v", err)
	}

	args, us := f.snapshotRuntime()
	if args.Socks5 != "" {
		t.Fatalf("expected plugin-level socks5 to stay empty, got %q", args.Socks5)
	}
	if len(us) != 2 {
		t.Fatalf("expected 2 runtime upstreams, got %d", len(us))
	}
	if got := us[0].cfg.Socks5; got != "" {
		t.Fatalf("expected upstream without explicit socks5 to stay direct, got %q", got)
	}
	if got := us[1].cfg.Socks5; got != "127.0.0.1:7891" {
		t.Fatalf("expected per-upstream socks5 to be preserved, got %q", got)
	}
}
