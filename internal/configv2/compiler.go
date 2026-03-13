package configv2

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func Load(raw []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config v2: %w", err)
	}
	if cfg.Version == "" {
		cfg.Version = CurrentVersion
	}
	if !IsV2Version(cfg.Version) {
		return nil, fmt.Errorf("unsupported config version %q", cfg.Version)
	}
	return &cfg, nil
}

func IsV2Version(version string) bool {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "2", "v2":
		return true
	default:
		return false
	}
}

func Compile(cfg *Config) (*CompiledConfig, error) {
	if cfg == nil {
		return nil, errors.New("config v2 is nil")
	}
	if cfg.Version == "" {
		cfg.Version = CurrentVersion
	}
	if !IsV2Version(cfg.Version) {
		return nil, fmt.Errorf("unsupported config version %q", cfg.Version)
	}

	compiled := &CompiledConfig{
		Log: cfg.Log,
		API: cfg.API,
	}

	hasLegacyCompat := len(cfg.Legacy.Include) > 0 || len(cfg.Legacy.Plugins) > 0
	if hasLegacyCompat {
		compiled.Include = append(compiled.Include, cfg.Legacy.Include...)
		compiled.Plugins = append(compiled.Plugins, cfg.Legacy.Plugins...)
	} else {
		for _, provider := range cfg.RuleProviders {
			if strings.TrimSpace(provider.Source) == "" {
				continue
			}
			if provider.Type == "" || provider.Type == "include" {
				compiled.Include = append(compiled.Include, provider.Source)
			}
		}
		if len(cfg.Upstreams) > 0 {
			plugins, err := compileUpstreams(cfg.Upstreams)
			if err != nil {
				return nil, err
			}
			compiled.Plugins = append(compiled.Plugins, plugins...)
		}
		if len(cfg.Policies) > 0 {
			plugins, err := compilePolicies(cfg.Policies)
			if err != nil {
				return nil, err
			}
			compiled.Plugins = append(compiled.Plugins, plugins...)
		}
		if len(cfg.Listeners) > 0 {
			plugins, err := compileListeners(cfg.Listeners)
			if err != nil {
				return nil, err
			}
			compiled.Plugins = append(compiled.Plugins, plugins...)
		}
	}

	if len(compiled.Include) == 0 && len(compiled.Plugins) == 0 &&
		(len(cfg.Listeners) > 0 || len(cfg.Upstreams) > 0 || len(cfg.Policies) > 0 || len(cfg.RuleProviders) > 0) {
		return nil, errors.New("config v2 did not produce any includes or plugins")
	}

	return compiled, nil
}

func compileListeners(listeners []ListenerConfig) ([]PluginConfig, error) {
	plugins := make([]PluginConfig, 0, len(listeners))
	for _, listener := range listeners {
		name := strings.TrimSpace(listener.Name)
		if name == "" {
			return nil, errors.New("listener name is required")
		}
		protocol := strings.ToLower(strings.TrimSpace(listener.Protocol))
		if protocol == "" {
			return nil, fmt.Errorf("listener %s protocol is required", name)
		}
		listen := strings.TrimSpace(listener.Listen)
		if listen == "" {
			return nil, fmt.Errorf("listener %s listen address is required", name)
		}
		entry := strings.TrimSpace(listener.Entry)
		if entry == "" {
			return nil, fmt.Errorf("listener %s entry is required", name)
		}

		args := cloneMap(listener.Options)
		args["listen"] = listen
		args["entry"] = entry
		if listener.Audit {
			args["enable_audit"] = true
		}

		plugins = append(plugins, PluginConfig{
			Tag:  name,
			Type: protocol + "_server",
			Args: args,
		})
	}
	return plugins, nil
}

func compileUpstreams(upstreams []UpstreamGroup) ([]PluginConfig, error) {
	plugins := make([]PluginConfig, 0, len(upstreams))
	for _, upstream := range upstreams {
		name := strings.TrimSpace(upstream.Name)
		if name == "" {
			return nil, errors.New("upstream name is required")
		}
		pluginType := strings.TrimSpace(upstream.PluginType)
		if pluginType == "" {
			pluginType = "forward"
		}
		args := cloneMap(upstream.Options)
		if len(upstream.Endpoints) > 0 {
			values := make([]any, 0, len(upstream.Endpoints))
			for _, endpoint := range upstream.Endpoints {
				if s := strings.TrimSpace(endpoint); s != "" {
					values = append(values, s)
				}
			}
			if len(values) > 0 {
				args["upstreams"] = values
			}
		}
		plugins = append(plugins, PluginConfig{
			Tag:  name,
			Type: pluginType,
			Args: args,
		})
	}
	return plugins, nil
}

func compilePolicies(policies []PolicyConfig) ([]PluginConfig, error) {
	plugins := make([]PluginConfig, 0, len(policies))
	for _, policy := range policies {
		name := strings.TrimSpace(policy.Name)
		if name == "" {
			return nil, errors.New("policy name is required")
		}
		policyType := strings.TrimSpace(policy.Type)
		if policyType == "" {
			policyType = "sequence"
		}
		plugins = append(plugins, PluginConfig{
			Tag:  name,
			Type: policyType,
			Args: policy.Args,
		})
	}
	return plugins, nil
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any)
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
