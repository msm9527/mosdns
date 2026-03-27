package coremain

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const cachePoliciesConfigRelPath = "sub_config/cache_policies.yaml"

const (
	defaultCacheMainSize             = 120000
	defaultCacheBranchDomesticSize   = 60000
	defaultCacheBranchForeignSize    = 50000
	defaultCacheBranchForeignECSSize = 30000
	defaultCacheFakeIPDomesticSize   = 30000
	defaultCacheFakeIPProxySize      = 40000
	defaultCacheProbeSize            = 20000
	defaultCacheMainL1TotalCap       = 4096
	defaultCacheBranchL1TotalCap     = 2048
	defaultCacheForeignECSL1TotalCap = 1024
	defaultCacheFakeIPL1TotalCap     = 1024
	defaultCacheProbeL1TotalCap      = 512
)

type CachePolicy struct {
	Size            int
	LazyCacheTTL    int
	NXDomainTTL     int
	ServfailTTL     int
	L1Enabled       bool
	L1TotalCap      int
	L1ShardCap      int
	Persist         bool
	DumpFile        string
	DumpInterval    int
	WALFile         string
	WALSyncInterval int
}

type cachePolicyFile struct {
	Size            *int    `yaml:"size,omitempty"`
	LazyCacheTTL    *int    `yaml:"lazy_cache_ttl,omitempty"`
	NXDomainTTL     *int    `yaml:"nxdomain_ttl,omitempty"`
	ServfailTTL     *int    `yaml:"servfail_ttl,omitempty"`
	L1Enabled       *bool   `yaml:"l1_enabled,omitempty"`
	L1TotalCap      *int    `yaml:"l1_total_cap,omitempty"`
	L1ShardCap      *int    `yaml:"l1_shard_cap,omitempty"`
	Persist         *bool   `yaml:"persist,omitempty"`
	DumpFile        *string `yaml:"dump_file,omitempty"`
	DumpInterval    *int    `yaml:"dump_interval,omitempty"`
	WALFile         *string `yaml:"wal_file,omitempty"`
	WALSyncInterval *int    `yaml:"wal_sync_interval,omitempty"`
}

type UDPFastCachePolicy struct {
	InternalTTL int    `yaml:"internal_ttl"`
	StaleRetry  int    `yaml:"stale_retry_seconds"`
	TTLMin      uint32 `yaml:"ttl_min"`
	TTLMax      uint32 `yaml:"ttl_max"`
}

type udpFastCachePolicyFile struct {
	InternalTTL *int    `yaml:"internal_ttl,omitempty"`
	StaleRetry  *int    `yaml:"stale_retry_seconds,omitempty"`
	TTLMin      *uint32 `yaml:"ttl_min,omitempty"`
	TTLMax      *uint32 `yaml:"ttl_max,omitempty"`
}

type CachePolicyConfig struct {
	Response    map[string]CachePolicy
	UDPFastPath UDPFastCachePolicy
}

type cachePoliciesFile struct {
	Response    map[string]cachePolicyFile `yaml:"response,omitempty"`
	UDPFastPath udpFastCachePolicyFile     `yaml:"udp_fast_path,omitempty"`
}

func cachePoliciesConfigPath() string {
	return cachePoliciesConfigPathForBaseDir(MainConfigBaseDir)
}

func cachePoliciesConfigPathForBaseDir(baseDir string) string {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, cachePoliciesConfigRelPath)
}

func defaultCachePolicyConfig() *CachePolicyConfig {
	return &CachePolicyConfig{
		Response: map[string]CachePolicy{
			"cache_main": {
				Size: defaultCacheMainSize, LazyCacheTTL: 86400, NXDomainTTL: 300, ServfailTTL: 30,
				L1Enabled: true, L1TotalCap: defaultCacheMainL1TotalCap, Persist: true,
				DumpFile: "db/cache/cache_main.dump", DumpInterval: 3600, WALSyncInterval: 1,
			},
			"cache_branch_domestic": {
				Size: defaultCacheBranchDomesticSize, LazyCacheTTL: 21600, NXDomainTTL: 180, ServfailTTL: 30,
				L1Enabled: true, L1TotalCap: defaultCacheBranchL1TotalCap, Persist: true,
				DumpFile: "db/cache/cache_branch_domestic.dump", DumpInterval: 3600, WALSyncInterval: 1,
			},
			"cache_branch_foreign": {
				Size: defaultCacheBranchForeignSize, LazyCacheTTL: 21600, NXDomainTTL: 180, ServfailTTL: 30,
				L1Enabled: true, L1TotalCap: defaultCacheBranchL1TotalCap, Persist: true,
				DumpFile: "db/cache/cache_branch_foreign.dump", DumpInterval: 3600, WALSyncInterval: 1,
			},
			"cache_branch_foreign_ecs": {
				Size: defaultCacheBranchForeignECSSize, LazyCacheTTL: 7200, NXDomainTTL: 120, ServfailTTL: 20,
				L1Enabled: true, L1TotalCap: defaultCacheForeignECSL1TotalCap, Persist: true,
				DumpFile: "db/cache/cache_branch_foreign_ecs.dump", DumpInterval: 1800, WALSyncInterval: 1,
			},
			"cache_fakeip_domestic": {
				Size: defaultCacheFakeIPDomesticSize, LazyCacheTTL: 0, NXDomainTTL: 60, ServfailTTL: 15,
				L1Enabled: true, L1TotalCap: defaultCacheFakeIPL1TotalCap, Persist: false,
			},
			"cache_fakeip_proxy": {
				Size: defaultCacheFakeIPProxySize, LazyCacheTTL: 0, NXDomainTTL: 60, ServfailTTL: 15,
				L1Enabled: true, L1TotalCap: defaultCacheFakeIPL1TotalCap, Persist: false,
			},
			"cache_probe": {
				Size: defaultCacheProbeSize, LazyCacheTTL: 600, NXDomainTTL: 60, ServfailTTL: 15,
				L1Enabled: true, L1TotalCap: defaultCacheProbeL1TotalCap, Persist: false,
			},
		},
		UDPFastPath: UDPFastCachePolicy{InternalTTL: 5, StaleRetry: 10, TTLMin: 1, TTLMax: 5},
	}
}

func LoadCachePolicyConfigFromSubConfig() (*CachePolicyConfig, bool, error) {
	return LoadCachePolicyConfigFromSubConfigForBaseDir(MainConfigBaseDir)
}

func LoadCachePolicyConfigFromSubConfigForBaseDir(baseDir string) (*CachePolicyConfig, bool, error) {
	cfg := defaultCachePolicyConfig()
	path := cachePoliciesConfigPathForBaseDir(baseDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return nil, false, fmt.Errorf("read cache policies config file: %w", err)
	}

	var fileCfg cachePoliciesFile
	if err := yaml.Unmarshal(raw, &fileCfg); err != nil {
		return nil, false, fmt.Errorf("decode cache policies config file %s: %w", path, err)
	}
	ignored, err := mergeCachePolicyFile(cfg, fileCfg)
	if err != nil {
		return nil, false, fmt.Errorf("normalize cache policies config file %s: %w", path, err)
	}
	if len(ignored) > 0 {
		mlog.L().Warn("ignoring unknown cache policies in sub config",
			zap.String("path", path),
			zap.Strings("tags", ignored))
	}
	return cfg, true, nil
}

func mergeCachePolicyFile(cfg *CachePolicyConfig, raw cachePoliciesFile) ([]string, error) {
	ignored := make([]string, 0)
	for tag, item := range raw.Response {
		policy, ok := cfg.Response[tag]
		if !ok {
			ignored = append(ignored, tag)
			continue
		}
		mergeOneCachePolicy(&policy, item)
		if err := validateCachePolicy(tag, policy); err != nil {
			return nil, err
		}
		cfg.Response[tag] = policy
	}
	if raw.UDPFastPath.InternalTTL != nil {
		cfg.UDPFastPath.InternalTTL = *raw.UDPFastPath.InternalTTL
	}
	if raw.UDPFastPath.StaleRetry != nil {
		cfg.UDPFastPath.StaleRetry = *raw.UDPFastPath.StaleRetry
	}
	if raw.UDPFastPath.TTLMin != nil {
		cfg.UDPFastPath.TTLMin = *raw.UDPFastPath.TTLMin
	}
	if raw.UDPFastPath.TTLMax != nil {
		cfg.UDPFastPath.TTLMax = *raw.UDPFastPath.TTLMax
	}
	if cfg.UDPFastPath.InternalTTL <= 0 {
		return nil, fmt.Errorf("udp_fast_path.internal_ttl requires > 0")
	}
	if cfg.UDPFastPath.StaleRetry <= 0 {
		return nil, fmt.Errorf("udp_fast_path.stale_retry_seconds requires > 0")
	}
	if cfg.UDPFastPath.TTLMax > 0 && cfg.UDPFastPath.TTLMin > cfg.UDPFastPath.TTLMax {
		return nil, fmt.Errorf("udp_fast_path.ttl_min cannot exceed ttl_max")
	}
	sort.Strings(ignored)
	return ignored, nil
}

func mergeOneCachePolicy(dst *CachePolicy, src cachePolicyFile) {
	if src.Size != nil {
		dst.Size = *src.Size
	}
	if src.LazyCacheTTL != nil {
		dst.LazyCacheTTL = *src.LazyCacheTTL
	}
	if src.NXDomainTTL != nil {
		dst.NXDomainTTL = *src.NXDomainTTL
	}
	if src.ServfailTTL != nil {
		dst.ServfailTTL = *src.ServfailTTL
	}
	if src.L1Enabled != nil {
		dst.L1Enabled = *src.L1Enabled
	}
	if src.L1TotalCap != nil {
		dst.L1TotalCap = *src.L1TotalCap
	}
	if src.L1ShardCap != nil {
		dst.L1ShardCap = *src.L1ShardCap
	}
	if src.Persist != nil {
		dst.Persist = *src.Persist
	}
	if src.DumpFile != nil {
		dst.DumpFile = strings.TrimSpace(*src.DumpFile)
	}
	if src.DumpInterval != nil {
		dst.DumpInterval = *src.DumpInterval
	}
	if src.WALFile != nil {
		dst.WALFile = strings.TrimSpace(*src.WALFile)
	}
	if src.WALSyncInterval != nil {
		dst.WALSyncInterval = *src.WALSyncInterval
	}
}

func validateCachePolicy(tag string, policy CachePolicy) error {
	if policy.Size <= 0 {
		return fmt.Errorf("%s.size requires > 0", tag)
	}
	if policy.NXDomainTTL <= 0 || policy.ServfailTTL <= 0 {
		return fmt.Errorf("%s negative ttl requires > 0", tag)
	}
	if policy.L1TotalCap < 0 || policy.L1ShardCap < 0 {
		return fmt.Errorf("%s l1 capacity cannot be negative", tag)
	}
	if !policy.Persist {
		return nil
	}
	if strings.TrimSpace(policy.DumpFile) == "" {
		return fmt.Errorf("%s.dump_file is required when persist=true", tag)
	}
	if policy.DumpInterval <= 0 || policy.WALSyncInterval <= 0 {
		return fmt.Errorf("%s persistence interval requires > 0", tag)
	}
	return nil
}

func ApplyRuntimeCachePolicy(pluginConf *PluginConfig, cfg *CachePolicyConfig) error {
	if pluginConf == nil || cfg == nil {
		return nil
	}
	switch pluginConf.Type {
	case "cache":
		policy, ok := cfg.Response[pluginConf.Tag]
		if !ok {
			return nil
		}
		args, err := pluginArgsMap(pluginConf.Args)
		if err != nil {
			return err
		}
		args["size"] = policy.Size
		args["lazy_cache_ttl"] = policy.LazyCacheTTL
		args["nxdomain_ttl"] = policy.NXDomainTTL
		args["servfail_ttl"] = policy.ServfailTTL
		args["l1_enabled"] = policy.L1Enabled
		args["l1_total_cap"] = policy.L1TotalCap
		args["l1_shard_cap"] = policy.L1ShardCap
		if policy.Persist {
			args["dump_file"] = policy.DumpFile
			args["dump_interval"] = policy.DumpInterval
			args["wal_sync_interval"] = policy.WALSyncInterval
			if policy.WALFile != "" {
				args["wal_file"] = policy.WALFile
			} else {
				delete(args, "wal_file")
			}
		} else {
			delete(args, "dump_file")
			delete(args, "dump_interval")
			delete(args, "wal_file")
			delete(args, "wal_sync_interval")
		}
		pluginConf.Args = args
	case "udp_server":
		args, err := pluginArgsMap(pluginConf.Args)
		if err != nil {
			return err
		}
		args["fast_cache_internal_ttl"] = cfg.UDPFastPath.InternalTTL
		args["fast_cache_stale_retry_seconds"] = cfg.UDPFastPath.StaleRetry
		args["fast_cache_ttl_min"] = cfg.UDPFastPath.TTLMin
		args["fast_cache_ttl_max"] = cfg.UDPFastPath.TTLMax
		pluginConf.Args = args
	}
	return nil
}

func pluginArgsMap(raw any) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	if m, ok := raw.(map[string]any); ok {
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, nil
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode plugin args: %w", err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode plugin args: %w", err)
	}
	if out == nil {
		out = make(map[string]any)
	}
	return out, nil
}
