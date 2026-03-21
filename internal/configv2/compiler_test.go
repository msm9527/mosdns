package configv2

import "testing"

func TestLoadRejectsLegacyKeys(t *testing.T) {
	raw := []byte(`
version: v2
legacy:
  include:
    - sub_config/cache.yaml
`)

	if _, err := Load(raw); err == nil {
		t.Fatalf("expected legacy keys to be rejected")
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
		Control: ControlConfig{
			BaseDir: "config",
			Requery: []RequeryConfig{{
				Name: "requery_main",
				Key:  "runtime/requery_main",
			}},
			Switches: []SwitchConfig{{
				Name: "branch_cache",
			}},
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
	if len(compiled.Plugins) != 5 {
		t.Fatalf("unexpected plugin count: %+v", compiled.Plugins)
	}
	if compiled.Plugins[0].Tag != "domestic" || compiled.Plugins[1].Tag != "sequence_main" || compiled.Plugins[4].Tag != "udp_all" {
		t.Fatalf("unexpected plugin order: %+v", compiled.Plugins)
	}
	if compiled.Plugins[2].Type != "requery" || compiled.Plugins[3].Type != "switch" {
		t.Fatalf("unexpected runtime plugin types: %+v", compiled.Plugins)
	}
	requeryArgs, ok := compiled.Plugins[2].Args.(map[string]any)
	if !ok || requeryArgs["key"] != "config/runtime/requery_main" {
		t.Fatalf("unexpected requery args: %#v", compiled.Plugins[2].Args)
	}
	switchArgs, ok := compiled.Plugins[3].Args.(map[string]any)
	if !ok || switchArgs["name"] != "branch_cache" {
		t.Fatalf("unexpected switch args: %#v", compiled.Plugins[3].Args)
	}
	args, ok := compiled.Plugins[4].Args.(map[string]any)
	if !ok || args["entry"] != "sequence_main" || args["listen"] != ":53" || args["enable_audit"] != true {
		t.Fatalf("unexpected listener args: %#v", compiled.Plugins[4].Args)
	}
}

func TestCompileIgnoresUnknownControlSwitches(t *testing.T) {
	cfg := &Config{
		Version: CurrentVersion,
		Control: ControlConfig{
			Switches: []SwitchConfig{
				{Name: "branch_cache"},
				{Name: "prefer_ipv6"},
			},
		},
	}

	compiled, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(compiled.Plugins) != 1 {
		t.Fatalf("expected only known switch plugin to remain, got %+v", compiled.Plugins)
	}
	if compiled.Plugins[0].Tag != "branch_cache" || compiled.Plugins[0].Type != "switch" {
		t.Fatalf("unexpected compiled plugins: %+v", compiled.Plugins)
	}
}

func TestCompileOrdersPluginsByDependency(t *testing.T) {
	cfg := &Config{
		Version: CurrentVersion,
		Policies: []PolicyConfig{
			{
				Name: "sequence_main",
				Type: "sequence",
				Args: []map[string]any{
					{"exec": "$matcher"},
				},
			},
			{
				Name: "matcher",
				Type: "domain_mapper",
				Args: map[string]any{
					"default_mark": 0,
					"rules": []map[string]any{
						{"tag": "list_a", "mark": 1},
					},
				},
			},
			{
				Name: "list_a",
				Type: "domain_set",
				Args: map[string]any{
					"files": []string{"rule/a.txt"},
				},
			},
		},
		Listeners: []ListenerConfig{{
			Name:     "udp_all",
			Protocol: "udp",
			Listen:   ":5353",
			Entry:    "sequence_main",
		}},
	}

	compiled, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(compiled.Plugins) != 4 {
		t.Fatalf("unexpected plugin count: %+v", compiled.Plugins)
	}
	if compiled.Plugins[0].Tag != "list_a" ||
		compiled.Plugins[1].Tag != "matcher" ||
		compiled.Plugins[2].Tag != "sequence_main" ||
		compiled.Plugins[3].Tag != "udp_all" {
		t.Fatalf("unexpected ordered plugins: %+v", compiled.Plugins)
	}
}
