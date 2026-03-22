package e2e_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	"gopkg.in/yaml.v3"
)

const (
	serviceE2EConfigDirname        = "config"
	serviceE2EMainConfigRelPath    = "config.yaml"
	serviceE2EListenersRelPath     = "sub_config/50-listeners.yaml"
	serviceE2ECachePoliciesRelPath = "sub_config/cache_policies.yaml"
	serviceE2EMemoryPoolsRelPath   = "custom_config/memory_pools.yaml"
)

type serviceE2EMainConfig struct {
	Version       string                   `yaml:"version"`
	Log           serviceE2ELogConfig      `yaml:"log"`
	API           serviceE2EAPIConfig      `yaml:"api"`
	Audit         serviceE2EAuditConfig    `yaml:"audit"`
	Storage       serviceE2EStorageConfig  `yaml:"storage"`
	RuleProviders []serviceE2ERuleProvider `yaml:"rule_providers"`
}

type serviceE2ELogConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

type serviceE2EAPIConfig struct {
	HTTP string `yaml:"http"`
}

type serviceE2EAuditConfig struct {
	Enabled                    bool   `yaml:"enabled"`
	OverviewWindowSeconds      int    `yaml:"overview_window_seconds"`
	RawRetentionDays           int    `yaml:"raw_retention_days"`
	AggregateRetentionDays     int    `yaml:"aggregate_retention_days"`
	MaxStorageMB               int    `yaml:"max_storage_mb"`
	SQLitePath                 string `yaml:"sqlite_path"`
	FlushBatchSize             int    `yaml:"flush_batch_size"`
	FlushIntervalMS            int    `yaml:"flush_interval_ms"`
	MaintenanceIntervalSeconds int    `yaml:"maintenance_interval_seconds"`
}

type serviceE2EStorageConfig struct {
	ControlDB string `yaml:"control_db"`
}

type serviceE2ERuleProvider struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Source string `yaml:"source"`
}

type serviceE2EListenersFile struct {
	Version   string                     `yaml:"version"`
	Listeners []serviceE2EListenerConfig `yaml:"listeners"`
}

type serviceE2EListenerConfig struct {
	Name     string         `yaml:"name"`
	Protocol string         `yaml:"protocol"`
	Listen   string         `yaml:"listen"`
	Entry    string         `yaml:"entry"`
	Audit    bool           `yaml:"audit,omitempty"`
	Options  map[string]any `yaml:"options,omitempty"`
}

type serviceE2EGlobalOverrides struct {
	Socks5       string `yaml:"socks5"`
	ECS          string `yaml:"ecs"`
	Replacements []any  `yaml:"replacements"`
}

func writeServiceE2EFiles(baseDir string, ports serviceE2EPorts, stubs serviceE2EUpstreams) (string, error) {
	configDir := filepath.Join(baseDir, serviceE2EConfigDirname)
	if err := copyServiceE2EBaseConfig(configDir); err != nil {
		return "", err
	}
	if err := ensureServiceE2EDirectories(configDir); err != nil {
		return "", err
	}
	if err := patchServiceE2EMainConfig(configDir, ports); err != nil {
		return "", err
	}
	if err := patchServiceE2EListeners(configDir, ports); err != nil {
		return "", err
	}
	if err := patchServiceE2EMemoryPools(configDir); err != nil {
		return "", err
	}
	if err := writeServiceE2EControlFiles(configDir, stubs); err != nil {
		return "", err
	}
	if err := writeServiceE2ERuleFiles(configDir); err != nil {
		return "", err
	}
	if err := writeServiceE2ECachePolicies(configDir); err != nil {
		return "", err
	}
	return filepath.Join(configDir, serviceE2EMainConfigRelPath), nil
}

func copyServiceE2EBaseConfig(configDir string) error {
	src, err := serviceE2ERepoConfigDir()
	if err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipServiceE2EConfig(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		dst := filepath.Join(configDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeServiceE2EFile(dst, string(data))
	})
}

func serviceE2ERepoConfigDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "config")
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func shouldSkipServiceE2EConfig(rel string) bool {
	if filepath.Base(rel) == ".DS_Store" {
		return true
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 0 {
		return false
	}
	return parts[0] == "db" || parts[0] == "ui"
}

func ensureServiceE2EDirectories(configDir string) error {
	dirs := []string{
		filepath.Join(configDir, "db", "cache"),
		filepath.Join(configDir, "logs"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func patchServiceE2EMainConfig(configDir string, ports serviceE2EPorts) error {
	path := filepath.Join(configDir, serviceE2EMainConfigRelPath)
	var cfg serviceE2EMainConfig
	if err := readServiceE2EYAML(path, &cfg); err != nil {
		return err
	}
	cfg.Log.Level = "error"
	cfg.Log.File = filepath.Join(configDir, "logs", "mosdns.log")
	cfg.API.HTTP = fmt.Sprintf("127.0.0.1:%d", ports.api)
	cfg.Audit.SQLitePath = filepath.Join(configDir, "db", "audit.db")
	cfg.Storage.ControlDB = filepath.Join(configDir, "db", "control.db")
	return writeServiceE2EYAML(path, cfg)
}

func patchServiceE2EListeners(configDir string, ports serviceE2EPorts) error {
	path := filepath.Join(configDir, serviceE2EListenersRelPath)
	var cfg serviceE2EListenersFile
	if err := readServiceE2EYAML(path, &cfg); err != nil {
		return err
	}
	portsByName := map[string]int{
		"udp_all":             ports.dns,
		"tcp_all":             ports.dns,
		"udp_requery":         ports.requery,
		"tcp_requery":         ports.requery,
		"udp_clashmi":         ports.clashmi,
		"tcp_clashmi":         ports.clashmi,
		"udp_google":          ports.google,
		"tcp_google":          ports.google,
		"udp_google_ecs":      ports.googleECS,
		"tcp_google_ecs":      ports.googleECS,
		"udp_local":           ports.local,
		"tcp_local":           ports.local,
		"udp_requery_refresh": ports.requeryRefresh,
		"tcp_requery_refresh": ports.requeryRefresh,
		"sbnode_udp":          ports.sbnode,
		"sbnode_tcp":          ports.sbnode,
		"sb_udp":              ports.singbox,
		"sb_tcp":              ports.singbox,
	}
	for i := range cfg.Listeners {
		port, ok := portsByName[cfg.Listeners[i].Name]
		if !ok {
			continue
		}
		cfg.Listeners[i].Listen = fmt.Sprintf("127.0.0.1:%d", port)
	}
	return writeServiceE2EYAML(path, cfg)
}

func writeServiceE2EControlFiles(configDir string, stubs serviceE2EUpstreams) error {
	files := map[string]string{
		filepath.Join(configDir, "custom_config", "switches.yaml"):          serviceE2ETemplate("switches.yaml"),
		filepath.Join(configDir, "custom_config", "adguard_sources.yaml"):   serviceE2ETemplate("adguard_sources.yaml"),
		filepath.Join(configDir, "custom_config", "diversion_sources.yaml"): serviceE2ETemplate("diversion_sources.yaml"),
	}
	for path, content := range files {
		if err := writeServiceE2EFile(path, content); err != nil {
			return err
		}
	}
	if err := writeServiceE2EYAML(
		filepath.Join(configDir, "custom_config", "global_overrides.yaml"),
		serviceE2EGlobalOverrides{Replacements: []any{}},
	); err != nil {
		return err
	}
	return writeServiceE2EYAML(
		filepath.Join(configDir, "custom_config", "upstreams.yaml"),
		coremain.GlobalUpstreamOverrides{
			"cnfake": {
				{Tag: "cnfake", Enabled: true, Protocol: "udp", Addr: "udp://" + stubs.cnfake},
			},
			"domestic": {
				{Tag: "domestic", Enabled: true, Protocol: "udp", Addr: "udp://" + stubs.domestic},
			},
			"foreign": {
				{Tag: "foreign", Enabled: true, Protocol: "udp", Addr: "udp://" + stubs.foreign},
			},
			"foreignecs": {
				{Tag: "foreignecs", Enabled: true, Protocol: "udp", Addr: "udp://" + stubs.foreignecs},
			},
			"nocnfake": {
				{Tag: "nocnfake", Enabled: true, Protocol: "udp", Addr: "udp://" + stubs.nocnfake},
			},
		},
	)
}

func patchServiceE2EMemoryPools(configDir string) error {
	path := filepath.Join(configDir, serviceE2EMemoryPoolsRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := strings.ReplaceAll(string(data), "promote_after: 2", "promote_after: 99")
	return writeServiceE2EFile(path, content)
}

func writeServiceE2ERuleFiles(configDir string) error {
	files := map[string]string{
		filepath.Join(configDir, "adguard", "base.rules"):           "||ad.example^\n",
		filepath.Join(configDir, "diversion", "geoip-cn.list"):      "1.1.1.0/24\n",
		filepath.Join(configDir, "diversion", "geosite-cn.list"):    "full:cn.example\n",
		filepath.Join(configDir, "diversion", "geosite-no-cn.list"): "full:proxy.example\n",
		filepath.Join(configDir, "rule", "blocklist.txt"):           "full:blocked.example\n",
		filepath.Join(configDir, "rule", "client_ip.txt"):           "\n",
		filepath.Join(configDir, "rule", "cnfakeipfilter.txt"):      "\n",
		filepath.Join(configDir, "rule", "ddnslist.txt"):            "\n",
		filepath.Join(configDir, "rule", "direct_ip.txt"):           "\n",
		filepath.Join(configDir, "rule", "greylist.txt"):            "\n",
		filepath.Join(configDir, "rule", "realiplist.txt"):          "full:default.example\nfull:branch.example\n",
		filepath.Join(configDir, "rule", "rewrite.txt"):             "\n",
		filepath.Join(configDir, "rule", "whitelist.txt"):           "\n",
	}
	for path, content := range files {
		if err := writeServiceE2EFile(path, content); err != nil {
			return err
		}
	}
	return nil
}

func writeServiceE2ECachePolicies(configDir string) error {
	content := `response:
  cache_main:
    persist: false
  cache_branch_domestic:
    persist: false
  cache_branch_foreign:
    persist: false
  cache_branch_foreign_ecs:
    persist: false
`
	return writeServiceE2EFile(filepath.Join(configDir, serviceE2ECachePoliciesRelPath), content)
}

func serviceE2ETemplate(name string) string {
	path := filepath.Join("testdata", "service_e2e", name)
	data, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("ReadFile(%s): %v", path, err))
	}
	return string(data)
}

func readServiceE2EYAML(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, dst)
}

func writeServiceE2EYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return writeServiceE2EFile(path, string(data))
}

func writeServiceE2EFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
