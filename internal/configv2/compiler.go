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

	if len(cfg.Legacy.Include) > 0 {
		compiled.Include = append(compiled.Include, cfg.Legacy.Include...)
	}
	if len(cfg.Legacy.Plugins) > 0 {
		compiled.Plugins = append(compiled.Plugins, cfg.Legacy.Plugins...)
	}

	if len(compiled.Include) == 0 && len(compiled.Plugins) == 0 {
		if len(cfg.Listeners) == 0 && len(cfg.Upstreams) == 0 && len(cfg.Policies) == 0 && len(cfg.RuleProviders) == 0 {
			return compiled, nil
		}
		return nil, errors.New("config v2 declarative compiler is not implemented yet; migrate with a legacy block first")
	}

	return compiled, nil
}
