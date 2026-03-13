package configv2

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type V1Config struct {
	Log     any            `yaml:"log,omitempty"`
	API     APIConfig      `yaml:"api,omitempty"`
	Include []string       `yaml:"include,omitempty"`
	Plugins []PluginConfig `yaml:"plugins,omitempty"`
}

func MigrateV1ToV2(v1 *V1Config) (*Config, error) {
	if v1 == nil {
		return nil, fmt.Errorf("config v1 is nil")
	}

	cfg := &Config{
		Version: CurrentVersion,
		API:     v1.API,
		Server: ServerConfig{
			Mode: "legacy-plugin-graph",
		},
		Storage: StorageConfig{
			RuntimeDB: "runtime.db",
		},
		Legacy: LegacyConfig{
			Include: append([]string(nil), v1.Include...),
			Plugins: append([]PluginConfig(nil), v1.Plugins...),
		},
	}

	if v1.Log != nil {
		data, err := yaml.Marshal(v1.Log)
		if err != nil {
			return nil, fmt.Errorf("marshal v1 log config: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg.Log); err != nil {
			return nil, fmt.Errorf("decode v1 log config: %w", err)
		}
	}

	cfg.Listeners = summarizeListeners(v1.Plugins)
	cfg.Upstreams = summarizeUpstreams(v1.Plugins)
	cfg.RuleProviders = summarizeRuleProviders(v1.Include)
	cfg.Policies = summarizePolicies(v1.Plugins)
	cfg.Runtime = summarizeRuntime(v1.Plugins)

	return cfg, nil
}

func Marshal(cfg *Config) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config v2 is nil")
	}
	if cfg.Version == "" {
		cfg.Version = CurrentVersion
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config v2: %w", err)
	}
	return data, nil
}

func summarizeListeners(plugins []PluginConfig) []ListenerConfig {
	var listeners []ListenerConfig
	for _, plugin := range plugins {
		if !strings.HasSuffix(plugin.Type, "_server") {
			continue
		}
		listener := ListenerConfig{
			Name:     plugin.Tag,
			Protocol: strings.TrimSuffix(plugin.Type, "_server"),
		}
		if args, ok := plugin.Args.(map[string]any); ok {
			if listen, ok := args["listen"].(string); ok {
				listener.Listen = listen
			}
			if entry, ok := args["entry"].(string); ok {
				listener.Entry = entry
			}
			if audit, ok := args["enable_audit"].(bool); ok {
				listener.Audit = audit
			}
		}
		listeners = append(listeners, listener)
	}
	return listeners
}

func summarizeUpstreams(plugins []PluginConfig) []UpstreamGroup {
	var groups []UpstreamGroup
	for _, plugin := range plugins {
		if !strings.Contains(plugin.Type, "forward") && !strings.Contains(plugin.Type, "upstream") {
			continue
		}
		group := UpstreamGroup{
			Name:       plugin.Tag,
			PluginType: plugin.Type,
		}
		if args, ok := plugin.Args.(map[string]any); ok {
			switch v := args["upstreams"].(type) {
			case []any:
				for _, item := range v {
					if s, ok := item.(string); ok {
						group.Endpoints = append(group.Endpoints, s)
					}
				}
			case []string:
				group.Endpoints = append(group.Endpoints, v...)
			}
			if addr, ok := args["addr"].(string); ok && !slices.Contains(group.Endpoints, addr) {
				group.Endpoints = append(group.Endpoints, addr)
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func summarizeRuleProviders(includes []string) []RuleProvider {
	providers := make([]RuleProvider, 0, len(includes))
	for _, include := range includes {
		providers = append(providers, RuleProvider{
			Name:   strings.TrimSuffix(filepathBase(include), filepathExt(include)),
			Source: include,
			Type:   "include",
		})
	}
	return providers
}

func summarizePolicies(plugins []PluginConfig) []PolicyConfig {
	policies := make([]PolicyConfig, 0, len(plugins))
	for _, plugin := range plugins {
		if plugin.Type != "sequence" {
			continue
		}
		policies = append(policies, PolicyConfig{
			Name:    plugin.Tag,
			Summary: "legacy sequence policy",
		})
	}
	return policies
}

func summarizeRuntime(plugins []PluginConfig) RuntimeConfig {
	var runtime RuntimeConfig
	for _, plugin := range plugins {
		switch plugin.Type {
		case "webinfo":
			runtime.WebInfo = append(runtime.WebInfo, summarizeWebInfo(plugin))
		case "requery":
			runtime.Requery = append(runtime.Requery, summarizeRequery(plugin))
		case "switch":
			runtime.Switches = append(runtime.Switches, summarizeSwitch(plugin))
		}
	}
	return runtime
}

func summarizeWebInfo(plugin PluginConfig) WebInfoConfig {
	item := WebInfoConfig{Name: plugin.Tag}
	if args, ok := plugin.Args.(map[string]any); ok {
		if file, ok := args["file"].(string); ok {
			item.File = file
		}
	}
	return item
}

func summarizeRequery(plugin PluginConfig) RequeryConfig {
	item := RequeryConfig{Name: plugin.Tag}
	if args, ok := plugin.Args.(map[string]any); ok {
		if file, ok := args["file"].(string); ok {
			item.File = file
		}
	}
	return item
}

func summarizeSwitch(plugin PluginConfig) SwitchConfig {
	item := SwitchConfig{Name: plugin.Tag}
	if args, ok := plugin.Args.(map[string]any); ok {
		if name, ok := args["name"].(string); ok && strings.TrimSpace(name) != "" {
			item.Name = name
		}
		if stateFile, ok := args["state_file_path"].(string); ok {
			item.StateFile = stateFile
		}
	}
	return item
}

func filepathBase(path string) string {
	path = strings.TrimSpace(path)
	if idx := strings.LastIndexAny(path, "/\\"); idx >= 0 && idx < len(path)-1 {
		return path[idx+1:]
	}
	return path
}

func filepathExt(path string) string {
	base := filepathBase(path)
	if idx := strings.LastIndex(base, "."); idx >= 0 {
		return base[idx:]
	}
	return ""
}
