package aliapi

import (
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

func TestAliAPIReloadControlConfigDoesNotApplyGlobalSocks5ToDNSUpstreams(t *testing.T) {
	f := &AliAPI{
		baseArgs:   &Args{},
		logger:     zap.NewNop(),
		pluginTag:  "domestic",
		metricsTag: "domestic",
	}

	err := f.ReloadControlConfig(&coremain.GlobalOverrides{Socks5: "127.0.0.1:1080"}, []coremain.UpstreamOverrideConfig{
		{Tag: "udp", Enabled: true, Protocol: "udp", Addr: "223.5.5.5"},
		{Tag: "doh", Enabled: true, Protocol: "https", Addr: "https://dns.alidns.com/dns-query", Socks5: "127.0.0.1:7891"},
	})
	if err != nil {
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
		t.Fatalf("expected domestic upstream without explicit socks5 to stay direct, got %q", got)
	}
	if got := us[1].cfg.Socks5; got != "127.0.0.1:7891" {
		t.Fatalf("expected per-upstream socks5 to be preserved, got %q", got)
	}
}
