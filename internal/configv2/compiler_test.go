package configv2

import "testing"

func TestMigrateV1ToV2AndCompile(t *testing.T) {
	v1 := &V1Config{
		API: APIConfig{HTTP: "127.0.0.1:9099"},
		Include: []string{
			"sub_config/cache.yaml",
			"sub_config/forward.yaml",
		},
		Plugins: []PluginConfig{
			{
				Tag:  "udp_all",
				Type: "udp_server",
				Args: map[string]any{
					"listen":       ":53",
					"entry":        "sequence_main",
					"enable_audit": true,
				},
			},
			{
				Tag:  "domestic",
				Type: "forward",
				Args: map[string]any{
					"upstreams": []any{"tls://1.1.1.1", "https://dns.google/dns-query"},
				},
			},
			{
				Tag:  "sequence_main",
				Type: "sequence",
			},
		},
	}

	cfg, err := MigrateV1ToV2(v1)
	if err != nil {
		t.Fatalf("MigrateV1ToV2() error = %v", err)
	}
	if cfg.Version != CurrentVersion {
		t.Fatalf("unexpected version %q", cfg.Version)
	}
	if len(cfg.Legacy.Include) != 2 || len(cfg.Legacy.Plugins) != 3 {
		t.Fatalf("legacy block not preserved: %+v", cfg.Legacy)
	}
	if len(cfg.Listeners) != 1 || cfg.Listeners[0].Listen != ":53" {
		t.Fatalf("unexpected listeners: %+v", cfg.Listeners)
	}
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Endpoints) != 2 {
		t.Fatalf("unexpected upstream summary: %+v", cfg.Upstreams)
	}

	compiled, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if compiled.API.HTTP != "127.0.0.1:9099" {
		t.Fatalf("unexpected api http %q", compiled.API.HTTP)
	}
	if len(compiled.Include) != 2 || len(compiled.Plugins) != 3 {
		t.Fatalf("compiled legacy graph mismatch: %+v", compiled)
	}
}

func TestCompileDeclarativeWithoutLegacy(t *testing.T) {
	cfg := &Config{
		Version: CurrentVersion,
		RuleProviders: []RuleProvider{
			{Name: "cache", Source: "sub_config/cache.yaml", Type: "include"},
		},
		Upstreams: []UpstreamGroup{
			{
				Name:       "domestic",
				PluginType: "forward",
				Endpoints:  []string{"tls://1.1.1.1"},
				Options: map[string]any{
					"concurrent": 2,
				},
			},
		},
		Policies: []PolicyConfig{
			{
				Name: "sequence_main",
				Type: "sequence",
				Args: []map[string]any{
					{"exec": "$domestic"},
				},
			},
		},
		Listeners: []ListenerConfig{{
			Name:     "udp_all",
			Protocol: "udp",
			Listen:   ":53",
			Entry:    "sequence_main",
			Audit:    true,
		}},
	}

	compiled, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(compiled.Include) != 1 {
		t.Fatalf("unexpected include count: %+v", compiled.Include)
	}
	if len(compiled.Plugins) != 3 {
		t.Fatalf("unexpected plugin count: %+v", compiled.Plugins)
	}
	if compiled.Plugins[0].Tag != "domestic" || compiled.Plugins[1].Tag != "sequence_main" || compiled.Plugins[2].Tag != "udp_all" {
		t.Fatalf("unexpected plugin order: %+v", compiled.Plugins)
	}
	args, ok := compiled.Plugins[2].Args.(map[string]any)
	if !ok || args["entry"] != "sequence_main" || args["listen"] != ":53" || args["enable_audit"] != true {
		t.Fatalf("unexpected listener args: %#v", compiled.Plugins[2].Args)
	}
}
