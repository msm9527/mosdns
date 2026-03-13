package configv2

import "github.com/IrineSistiana/mosdns/v5/mlog"

const CurrentVersion = "v2"

type Config struct {
	Version       string           `yaml:"version"`
	Log           mlog.LogConfig   `yaml:"log,omitempty"`
	API           APIConfig        `yaml:"api,omitempty"`
	Server        ServerConfig     `yaml:"server,omitempty"`
	Listeners     []ListenerConfig `yaml:"listeners,omitempty"`
	Upstreams     []UpstreamGroup  `yaml:"upstreams,omitempty"`
	Policies      []PolicyConfig   `yaml:"policies,omitempty"`
	RuleProviders []RuleProvider   `yaml:"rule_providers,omitempty"`
	Features      map[string]any   `yaml:"features,omitempty"`
	Storage       StorageConfig    `yaml:"storage,omitempty"`
	Exports       []ExportConfig   `yaml:"exports,omitempty"`
	Legacy        LegacyConfig     `yaml:"legacy,omitempty"`
}

type APIConfig struct {
	HTTP string `yaml:"http,omitempty"`
}

type ServerConfig struct {
	Name string `yaml:"name,omitempty"`
	Mode string `yaml:"mode,omitempty"`
}

type ListenerConfig struct {
	Name     string `yaml:"name,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Listen   string `yaml:"listen,omitempty"`
	Entry    string `yaml:"entry,omitempty"`
	Audit    bool   `yaml:"audit,omitempty"`
}

type UpstreamGroup struct {
	Name       string   `yaml:"name,omitempty"`
	PluginType string   `yaml:"plugin_type,omitempty"`
	Endpoints  []string `yaml:"endpoints,omitempty"`
}

type PolicyConfig struct {
	Name    string `yaml:"name,omitempty"`
	Summary string `yaml:"summary,omitempty"`
}

type RuleProvider struct {
	Name   string `yaml:"name,omitempty"`
	Source string `yaml:"source,omitempty"`
	Type   string `yaml:"type,omitempty"`
}

type StorageConfig struct {
	RuntimeDB string `yaml:"runtime_db,omitempty"`
}

type ExportConfig struct {
	Name   string `yaml:"name,omitempty"`
	Type   string `yaml:"type,omitempty"`
	Target string `yaml:"target,omitempty"`
}

type LegacyConfig struct {
	Include []string       `yaml:"include,omitempty"`
	Plugins []PluginConfig `yaml:"plugins,omitempty"`
}

type PluginConfig struct {
	Tag  string `yaml:"tag,omitempty"`
	Type string `yaml:"type"`
	Args any    `yaml:"args,omitempty"`
}

type CompiledConfig struct {
	Log     mlog.LogConfig
	API     APIConfig
	Include []string
	Plugins []PluginConfig
}
