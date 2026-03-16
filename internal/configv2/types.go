package configv2

import "github.com/IrineSistiana/mosdns/v5/mlog"

const CurrentVersion = "v2"

type Config struct {
	Version       string           `yaml:"version"`
	Log           mlog.LogConfig   `yaml:"log,omitempty"`
	API           APIConfig        `yaml:"api,omitempty"`
	Audit         AuditConfig      `yaml:"audit,omitempty"`
	Server        ServerConfig     `yaml:"server,omitempty"`
	Listeners     []ListenerConfig `yaml:"listeners,omitempty"`
	Upstreams     []UpstreamGroup  `yaml:"upstreams,omitempty"`
	Policies      []PolicyConfig   `yaml:"policies,omitempty"`
	RuleProviders []RuleProvider   `yaml:"rule_providers,omitempty"`
	Control       ControlConfig    `yaml:"control,omitempty"`
	Features      map[string]any   `yaml:"features,omitempty"`
	Storage       StorageConfig    `yaml:"storage,omitempty"`
	Exports       []ExportConfig   `yaml:"exports,omitempty"`
}

type APIConfig struct {
	HTTP string `yaml:"http,omitempty"`
}

type AuditConfig struct {
	MemoryEntries int    `yaml:"memory_entries,omitempty"`
	RetentionDays int    `yaml:"retention_days,omitempty"`
	MaxDiskSizeMB int    `yaml:"max_disk_size_mb,omitempty"`
	MaxDBSizeMB   int    `yaml:"max_db_size_mb,omitempty"`
	StorageEngine string `yaml:"storage_engine,omitempty"`
	SQLitePath    string `yaml:"sqlite_path,omitempty"`
}

type ServerConfig struct {
	Name string `yaml:"name,omitempty"`
	Mode string `yaml:"mode,omitempty"`
}

type ListenerConfig struct {
	Name     string         `yaml:"name,omitempty"`
	Protocol string         `yaml:"protocol,omitempty"`
	Listen   string         `yaml:"listen,omitempty"`
	Entry    string         `yaml:"entry,omitempty"`
	Audit    bool           `yaml:"audit,omitempty"`
	Options  map[string]any `yaml:"options,omitempty"`
}

type UpstreamGroup struct {
	Name       string         `yaml:"name,omitempty"`
	PluginType string         `yaml:"plugin_type,omitempty"`
	Endpoints  []string       `yaml:"endpoints,omitempty"`
	Options    map[string]any `yaml:"options,omitempty"`
}

type PolicyConfig struct {
	Name    string `yaml:"name,omitempty"`
	Type    string `yaml:"type,omitempty"`
	Summary string `yaml:"summary,omitempty"`
	Args    any    `yaml:"args,omitempty"`
}

type RuleProvider struct {
	Name   string `yaml:"name,omitempty"`
	Source string `yaml:"source,omitempty"`
	Type   string `yaml:"type,omitempty"`
}

type ControlConfig struct {
	BaseDir  string          `yaml:"base_dir,omitempty"`
	WebInfo  []WebInfoConfig `yaml:"webinfo,omitempty"`
	Requery  []RequeryConfig `yaml:"requery,omitempty"`
	Switches []SwitchConfig  `yaml:"switches,omitempty"`
}

type WebInfoConfig struct {
	Name string `yaml:"name,omitempty"`
	File string `yaml:"file,omitempty"`
}

type RequeryConfig struct {
	Name string `yaml:"name,omitempty"`
	File string `yaml:"file,omitempty"`
}

type SwitchConfig struct {
	Name string `yaml:"name,omitempty"`
}

type StorageConfig struct {
	ControlDB string `yaml:"control_db,omitempty"`
}

type ExportConfig struct {
	Name   string `yaml:"name,omitempty"`
	Type   string `yaml:"type,omitempty"`
	Target string `yaml:"target,omitempty"`
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
