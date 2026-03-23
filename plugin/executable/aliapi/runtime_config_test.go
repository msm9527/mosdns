package aliapi

import (
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

func TestAliAPIReloadControlConfigPreservesPerUpstreamAliAPICredentials(t *testing.T) {
	f := &AliAPI{
		baseArgs:   &Args{},
		logger:     zap.NewNop(),
		pluginTag:  "domestic",
		metricsTag: "domestic",
	}

	err := f.ReloadControlConfig(&coremain.GlobalOverrides{}, []coremain.UpstreamOverrideConfig{
		{
			Tag:             "ali-one",
			Enabled:         true,
			Protocol:        "aliapi",
			AccountID:       "account-1",
			AccessKeyID:     "ak-1",
			AccessKeySecret: "secret-1",
			ServerAddr:      "223.5.5.5",
			EcsClientIP:     "1.1.1.1",
			EcsClientMask:   32,
		},
		{
			Tag:             "ali-two",
			Enabled:         true,
			Protocol:        "aliapi",
			AccountID:       "account-2",
			AccessKeyID:     "ak-2",
			AccessKeySecret: "secret-2",
			ServerAddr:      "223.6.6.6",
			EcsClientIP:     "2.2.2.2",
			EcsClientMask:   24,
		},
	})
	if err != nil {
		t.Fatalf("ReloadControlConfig: %v", err)
	}

	got := snapshotAliAPIArgsByTag(t, f)
	if got["ali-one"].AccountID != "account-1" || got["ali-one"].ServerAddr != "223.5.5.5" {
		t.Fatalf("unexpected runtime args for ali-one: %+v", got["ali-one"])
	}
	if got["ali-two"].AccountID != "account-2" || got["ali-two"].ServerAddr != "223.6.6.6" {
		t.Fatalf("unexpected runtime args for ali-two: %+v", got["ali-two"])
	}

	_, items := f.SnapshotControlUpstreams()
	snapshot := mapSnapshotItemsByTag(items)
	if snapshot["ali-one"].AccessKeySecret != "secret-1" || snapshot["ali-one"].EcsClientIP != "1.1.1.1" {
		t.Fatalf("unexpected snapshot item for ali-one: %+v", snapshot["ali-one"])
	}
	if snapshot["ali-two"].AccessKeySecret != "secret-2" || snapshot["ali-two"].EcsClientIP != "2.2.2.2" {
		t.Fatalf("unexpected snapshot item for ali-two: %+v", snapshot["ali-two"])
	}
}

func TestNewAliAPIExpandsLegacyGlobalAliAPICredentials(t *testing.T) {
	f, err := NewAliAPI(&Args{
		AccountID:       "legacy-account",
		AccessKeyID:     "legacy-ak",
		AccessKeySecret: "legacy-secret",
		ServerAddr:      "legacy.server",
		EcsClientIP:     "10.0.0.1",
		EcsClientMask:   32,
		Upstreams: []UpstreamConfig{
			{
				Tag:  "ali-legacy",
				Type: "aliapi",
			},
			{
				Tag:             "ali-custom",
				Type:            "aliapi",
				AccountID:       "custom-account",
				AccessKeyID:     "custom-ak",
				AccessKeySecret: "custom-secret",
				ServerAddr:      "custom.server",
				EcsClientIP:     "10.0.0.2",
				EcsClientMask:   24,
			},
		},
	}, Opts{Logger: zap.NewNop(), MetricsTag: "ali"})
	if err != nil {
		t.Fatalf("NewAliAPI: %v", err)
	}
	defer func() { _ = f.Close() }()

	got := snapshotAliAPIArgsByTag(t, f)
	if got["ali-legacy"].AccountID != "legacy-account" || got["ali-legacy"].AccessKeyID != "legacy-ak" {
		t.Fatalf("legacy config was not expanded: %+v", got["ali-legacy"])
	}
	if got["ali-legacy"].ServerAddr != "legacy.server" || got["ali-legacy"].EcsClientIP != "10.0.0.1" {
		t.Fatalf("legacy config server/ecs mismatch: %+v", got["ali-legacy"])
	}
	if got["ali-custom"].AccountID != "custom-account" || got["ali-custom"].AccessKeyID != "custom-ak" {
		t.Fatalf("custom per-upstream config was overwritten: %+v", got["ali-custom"])
	}
	if got["ali-custom"].ServerAddr != "custom.server" || got["ali-custom"].EcsClientIP != "10.0.0.2" {
		t.Fatalf("custom per-upstream server/ecs mismatch: %+v", got["ali-custom"])
	}
}

func TestNewAliAPIAppliesDefaultServerAddrToAliAPIUpstreams(t *testing.T) {
	f, err := NewAliAPI(&Args{
		Upstreams: []UpstreamConfig{
			{
				Tag:             "ali-default",
				Type:            "aliapi",
				AccountID:       "account-1",
				AccessKeyID:     "ak-1",
				AccessKeySecret: "secret-1",
			},
			{
				Tag:             "ali-explicit",
				Type:            "aliapi",
				AccountID:       "account-2",
				AccessKeyID:     "ak-2",
				AccessKeySecret: "secret-2",
				ServerAddr:      "223.6.6.6",
			},
		},
	}, Opts{Logger: zap.NewNop(), MetricsTag: "ali"})
	if err != nil {
		t.Fatalf("NewAliAPI: %v", err)
	}
	defer func() { _ = f.Close() }()

	got := snapshotAliAPIArgsByTag(t, f)
	if got["ali-default"].ServerAddr != defaultAliAPIServer {
		t.Fatalf("expected default server addr %q, got %+v", defaultAliAPIServer, got["ali-default"])
	}
	if got["ali-explicit"].ServerAddr != "223.6.6.6" {
		t.Fatalf("explicit server addr was overwritten: %+v", got["ali-explicit"])
	}
}

func snapshotAliAPIArgsByTag(t *testing.T, f *AliAPI) map[string]AliAPIUpstreamArgs {
	t.Helper()

	_, us := f.snapshotRuntime()
	got := make(map[string]AliAPIUpstreamArgs, len(us))
	for _, wrapper := range us {
		aliUpstream, ok := wrapper.u.(*AliAPIUpstream)
		if !ok {
			t.Fatalf("upstream %q is %T, want *AliAPIUpstream", wrapper.cfg.Tag, wrapper.u)
		}
		got[wrapper.cfg.Tag] = aliUpstream.args
	}
	return got
}

func mapSnapshotItemsByTag(items []coremain.UpstreamOverrideConfig) map[string]coremain.UpstreamOverrideConfig {
	got := make(map[string]coremain.UpstreamOverrideConfig, len(items))
	for _, item := range items {
		got[item.Tag] = item
	}
	return got
}
