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
			WebInfo: []WebInfoConfig{{
				Name: "webinfo_client",
				File: "webinfo/clientname.json",
			}},
			Requery: []RequeryConfig{{
				Name: "requery_main",
				File: "webinfo/requeryconfig.json",
			}},
			Switches: []SwitchConfig{{
				Name:      "core_mode",
				StateFile: "switches.json",
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
	if len(compiled.Plugins) != 6 {
		t.Fatalf("unexpected plugin count: %+v", compiled.Plugins)
	}
	if compiled.Plugins[0].Tag != "domestic" || compiled.Plugins[1].Tag != "sequence_main" || compiled.Plugins[5].Tag != "udp_all" {
		t.Fatalf("unexpected plugin order: %+v", compiled.Plugins)
	}
	if compiled.Plugins[2].Type != "webinfo" || compiled.Plugins[3].Type != "requery" || compiled.Plugins[4].Type != "switch" {
		t.Fatalf("unexpected runtime plugin types: %+v", compiled.Plugins)
	}
	webinfoArgs, ok := compiled.Plugins[2].Args.(map[string]any)
	if !ok || webinfoArgs["file"] != "config/webinfo/clientname.json" {
		t.Fatalf("unexpected webinfo args: %#v", compiled.Plugins[2].Args)
	}
	requeryArgs, ok := compiled.Plugins[3].Args.(map[string]any)
	if !ok || requeryArgs["file"] != "config/webinfo/requeryconfig.json" {
		t.Fatalf("unexpected requery args: %#v", compiled.Plugins[3].Args)
	}
	switchArgs, ok := compiled.Plugins[4].Args.(map[string]any)
	if !ok || switchArgs["name"] != "core_mode" || switchArgs["state_file_path"] != "config/switches.json" {
		t.Fatalf("unexpected switch args: %#v", compiled.Plugins[4].Args)
	}
	args, ok := compiled.Plugins[5].Args.(map[string]any)
	if !ok || args["entry"] != "sequence_main" || args["listen"] != ":53" || args["enable_audit"] != true {
		t.Fatalf("unexpected listener args: %#v", compiled.Plugins[5].Args)
	}
}
