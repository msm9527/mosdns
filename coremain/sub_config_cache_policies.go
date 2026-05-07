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
	defaultCacheMainSize             = 400000
	defaultCacheBranchDomesticSize   = 5000
	defaultCacheBranchForeignSize    = 5000
	defaultCacheBranchForeignECSSize = 2000
	defaultCacheFakeIPDomesticSize   = 100000
	defaultCacheFakeIPProxySize      = 120000
	defaultCacheProbeSize            = 80000
	defaultCacheMainL1TotalCap       = 8192
	defaultCacheBranchL1TotalCap     = 256
	defaultCacheForeignECSL1TotalCap = 128
	defaultCacheFakeIPL1TotalCap     = 2048
	defaultCacheProbeL1TotalCap      = 2048
)

var defaultResponseCacheBypassDomainSets = []string{"DDNS域名", "高变化域名"}

type CachePolicy struct {
	Size             int
	LazyCacheTTL     int
	LazyStaleTTL     int
	ClientTTLMin     uint32
	ClientTTLMax     uint32
	NXDomainTTL      int
	ServfailTTL      int
	L1Enabled        bool
	L1TotalCap       int
	L1ShardCap       int
	BypassDomainSets []string
	ExcludeIPs       []string
	Persist          bool
	DumpFile         string
	DumpInterval     int
	WALFile          string
	WALSyncInterval  int
}

type cachePolicyFile struct {
	Size             *int                  `yaml:"size,omitempty"`
	LazyCacheTTL     *int                  `yaml:"lazy_cache_ttl,omitempty"`
	LazyStaleTTL     *int                  `yaml:"lazy_stale_ttl,omitempty"`
	ClientTTLMin     *uint32               `yaml:"client_ttl_min,omitempty"`
	ClientTTLMax     *uint32               `yaml:"client_ttl_max,omitempty"`
	NXDomainTTL      *int                  `yaml:"nxdomain_ttl,omitempty"`
	ServfailTTL      *int                  `yaml:"servfail_ttl,omitempty"`
	L1Enabled        *bool                 `yaml:"l1_enabled,omitempty"`
	L1TotalCap       *int                  `yaml:"l1_total_cap,omitempty"`
	L1ShardCap       *int                  `yaml:"l1_shard_cap,omitempty"`
	BypassDomainSets *[]string             `yaml:"bypass_domain_sets,omitempty"`
	ExcludeIPs       *cachePolicyStringSet `yaml:"exclude_ip,omitempty"`
	Persist          *bool                 `yaml:"persist,omitempty"`
	DumpFile         *string               `yaml:"dump_file,omitempty"`
	DumpInterval     *int                  `yaml:"dump_interval,omitempty"`
	WALFile          *string               `yaml:"wal_file,omitempty"`
	WALSyncInterval  *int                  `yaml:"wal_sync_interval,omitempty"`
}

type cachePolicyStringSet []string

func (s *cachePolicyStringSet) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*s = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("expected scalar string item, got yaml kind %d", item.Kind)
			}
			values = append(values, item.Value)
		}
		*s = values
		return nil
	default:
		return fmt.Errorf("expected string or string list, got yaml kind %d", node.Kind)
	}
}

type UDPFastCachePolicy struct {
	InternalTTL      int      `yaml:"internal_ttl"`
	StaleRetry       int      `yaml:"stale_retry_seconds"`
	StaleMax         int      `yaml:"stale_max_seconds"`
	TTLMin           uint32   `yaml:"ttl_min"`
	TTLMax           uint32   `yaml:"ttl_max"`
	MemoryBudgetMB   int      `yaml:"memory_budget_mb"`
	ResponseSlots    int      `yaml:"response_slots"`
	RuleSlots        int      `yaml:"rule_slots"`
	BypassDomainSets []string `yaml:"bypass_domain_sets"`
}

type udpFastCachePolicyFile struct {
	InternalTTL      *int      `yaml:"internal_ttl,omitempty"`
	StaleRetry       *int      `yaml:"stale_retry_seconds,omitempty"`
	StaleMax         *int      `yaml:"stale_max_seconds,omitempty"`
	TTLMin           *uint32   `yaml:"ttl_min,omitempty"`
	TTLMax           *uint32   `yaml:"ttl_max,omitempty"`
	MemoryBudgetMB   *int      `yaml:"memory_budget_mb,omitempty"`
	ResponseSlots    *int      `yaml:"response_slots,omitempty"`
	RuleSlots        *int      `yaml:"rule_slots,omitempty"`
	BypassDomainSets *[]string `yaml:"bypass_domain_sets,omitempty"`
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
	realCacheExcludeIPs := defaultRealCacheExcludeIPs()
	return &CachePolicyConfig{
		Response: map[string]CachePolicy{
			"cache_main": {
				Size: defaultCacheMainSize, LazyCacheTTL: 2592000, LazyStaleTTL: 2592000, ClientTTLMin: 120, ClientTTLMax: 900, NXDomainTTL: 300, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheMainL1TotalCap, Persist: true,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
				ExcludeIPs:       realCacheExcludeIPs,
				DumpFile:         "db/cache/cache_main.dump", DumpInterval: 3600, WALSyncInterval: 1,
			},
			"cache_branch_domestic": {
				Size: defaultCacheBranchDomesticSize, LazyCacheTTL: 900, LazyStaleTTL: 300, ClientTTLMin: 120, ClientTTLMax: 900, NXDomainTTL: 180, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheBranchL1TotalCap, Persist: false,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
				ExcludeIPs:       realCacheExcludeIPs,
			},
			"cache_branch_foreign": {
				Size: defaultCacheBranchForeignSize, LazyCacheTTL: 900, LazyStaleTTL: 300, ClientTTLMin: 120, ClientTTLMax: 900, NXDomainTTL: 180, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheBranchL1TotalCap, Persist: false,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
				ExcludeIPs:       realCacheExcludeIPs,
			},
			"cache_branch_foreign_ecs": {
				Size: defaultCacheBranchForeignECSSize, LazyCacheTTL: 900, LazyStaleTTL: 300, ClientTTLMin: 120, ClientTTLMax: 900, NXDomainTTL: 120, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheForeignECSL1TotalCap, Persist: false,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
				ExcludeIPs:       realCacheExcludeIPs,
			},
			"cache_fakeip_domestic": {
				Size: defaultCacheFakeIPDomesticSize, LazyCacheTTL: 14400, LazyStaleTTL: 14400, ClientTTLMin: 600, ClientTTLMax: 600, NXDomainTTL: 60, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheFakeIPL1TotalCap, Persist: false,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
			},
			"cache_fakeip_proxy": {
				Size: defaultCacheFakeIPProxySize, LazyCacheTTL: 14400, LazyStaleTTL: 14400, ClientTTLMin: 600, ClientTTLMax: 600, NXDomainTTL: 60, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheFakeIPL1TotalCap, Persist: false,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
			},
			"cache_probe": {
				Size: defaultCacheProbeSize, LazyCacheTTL: 600, LazyStaleTTL: 600, ClientTTLMin: 120, ClientTTLMax: 900, NXDomainTTL: 60, ServfailTTL: 5,
				L1Enabled: true, L1TotalCap: defaultCacheProbeL1TotalCap, Persist: false,
				BypassDomainSets: defaultResponseCacheBypassDomains(),
				ExcludeIPs:       realCacheExcludeIPs,
			},
		},
		UDPFastPath: UDPFastCachePolicy{
			InternalTTL:      7200,
			StaleRetry:       30,
			StaleMax:         7200,
			TTLMin:           120,
			TTLMax:           900,
			MemoryBudgetMB:   8,
			BypassDomainSets: defaultResponseCacheBypassDomains(),
		},
	}
}

func defaultResponseCacheBypassDomains() []string {
	return normalizeCachePolicyDomainSets(defaultResponseCacheBypassDomainSets)
}

func defaultRealCacheExcludeIPs() []string {
	return []string{
		"0.0.0.0/32",
		"127.0.0.0/8",
		"28.0.0.0/8",
		"30.0.0.0/8",
		"::/128",
		"::1/128",
		"f2b0::/18",
		"2400::1/64",
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
	if raw.UDPFastPath.StaleMax != nil {
		cfg.UDPFastPath.StaleMax = *raw.UDPFastPath.StaleMax
	}
	if raw.UDPFastPath.TTLMin != nil {
		cfg.UDPFastPath.TTLMin = *raw.UDPFastPath.TTLMin
	}
	if raw.UDPFastPath.TTLMax != nil {
		cfg.UDPFastPath.TTLMax = *raw.UDPFastPath.TTLMax
	}
	if raw.UDPFastPath.MemoryBudgetMB != nil {
		cfg.UDPFastPath.MemoryBudgetMB = *raw.UDPFastPath.MemoryBudgetMB
	}
	if raw.UDPFastPath.ResponseSlots != nil {
		cfg.UDPFastPath.ResponseSlots = *raw.UDPFastPath.ResponseSlots
	}
	if raw.UDPFastPath.RuleSlots != nil {
		cfg.UDPFastPath.RuleSlots = *raw.UDPFastPath.RuleSlots
	}
	if raw.UDPFastPath.BypassDomainSets != nil {
		cfg.UDPFastPath.BypassDomainSets = normalizeCachePolicyDomainSets(*raw.UDPFastPath.BypassDomainSets)
	}
	if cfg.UDPFastPath.InternalTTL <= 0 {
		return nil, fmt.Errorf("udp_fast_path.internal_ttl requires > 0")
	}
	if cfg.UDPFastPath.StaleRetry <= 0 {
		return nil, fmt.Errorf("udp_fast_path.stale_retry_seconds requires > 0")
	}
	if cfg.UDPFastPath.StaleMax <= 0 {
		return nil, fmt.Errorf("udp_fast_path.stale_max_seconds requires > 0")
	}
	if cfg.UDPFastPath.TTLMax > 0 && cfg.UDPFastPath.TTLMin > cfg.UDPFastPath.TTLMax {
		return nil, fmt.Errorf("udp_fast_path.ttl_min cannot exceed ttl_max")
	}
	if cfg.UDPFastPath.MemoryBudgetMB < 0 {
		return nil, fmt.Errorf("udp_fast_path.memory_budget_mb cannot be negative")
	}
	if cfg.UDPFastPath.ResponseSlots < 0 || cfg.UDPFastPath.RuleSlots < 0 {
		return nil, fmt.Errorf("udp_fast_path slots cannot be negative")
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
		if src.LazyStaleTTL == nil {
			dst.LazyStaleTTL = *src.LazyCacheTTL
		}
	}
	if src.LazyStaleTTL != nil {
		dst.LazyStaleTTL = *src.LazyStaleTTL
	}
	if src.ClientTTLMin != nil {
		dst.ClientTTLMin = *src.ClientTTLMin
	}
	if src.ClientTTLMax != nil {
		dst.ClientTTLMax = *src.ClientTTLMax
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
	if src.BypassDomainSets != nil {
		dst.BypassDomainSets = normalizeCachePolicyDomainSets(*src.BypassDomainSets)
	}
	if src.ExcludeIPs != nil {
		dst.ExcludeIPs = normalizeCachePolicyExcludeIPs([]string(*src.ExcludeIPs))
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

func normalizeCachePolicyDomainSets(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == '|' || r == ',' || r == '，'
		}) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeCachePolicyExcludeIPs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == '|' || r == ',' || r == '，'
		}) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

func validateCachePolicy(tag string, policy CachePolicy) error {
	if policy.Size <= 0 {
		return fmt.Errorf("%s.size requires > 0", tag)
	}
	if policy.NXDomainTTL <= 0 || policy.ServfailTTL <= 0 {
		return fmt.Errorf("%s negative ttl requires > 0", tag)
	}
	if policy.LazyCacheTTL < 0 || policy.LazyStaleTTL < 0 {
		return fmt.Errorf("%s lazy ttl cannot be negative", tag)
	}
	if policy.ClientTTLMax > 0 && policy.ClientTTLMin > policy.ClientTTLMax {
		return fmt.Errorf("%s client_ttl_min cannot exceed client_ttl_max", tag)
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
		args["lazy_stale_ttl"] = policy.LazyStaleTTL
		args["client_ttl_min"] = policy.ClientTTLMin
		args["client_ttl_max"] = policy.ClientTTLMax
		args["nxdomain_ttl"] = policy.NXDomainTTL
		args["servfail_ttl"] = policy.ServfailTTL
		args["l1_enabled"] = policy.L1Enabled
		args["l1_total_cap"] = policy.L1TotalCap
		args["l1_shard_cap"] = policy.L1ShardCap
		if len(policy.BypassDomainSets) > 0 {
			args["bypass_domain_sets"] = append([]string(nil), policy.BypassDomainSets...)
		} else {
			delete(args, "bypass_domain_sets")
		}
		if len(policy.ExcludeIPs) > 0 {
			args["exclude_ip"] = append([]string(nil), policy.ExcludeIPs...)
		} else {
			delete(args, "exclude_ip")
		}
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
		args["fast_cache_stale_max_seconds"] = cfg.UDPFastPath.StaleMax
		args["fast_cache_ttl_min"] = cfg.UDPFastPath.TTLMin
		args["fast_cache_ttl_max"] = cfg.UDPFastPath.TTLMax
		args["fast_cache_memory_budget_mb"] = cfg.UDPFastPath.MemoryBudgetMB
		if cfg.UDPFastPath.ResponseSlots > 0 {
			args["fast_cache_slots"] = cfg.UDPFastPath.ResponseSlots
		} else {
			delete(args, "fast_cache_slots")
		}
		if cfg.UDPFastPath.RuleSlots > 0 {
			args["fast_rule_cache_slots"] = cfg.UDPFastPath.RuleSlots
		} else {
			delete(args, "fast_rule_cache_slots")
		}
		if len(cfg.UDPFastPath.BypassDomainSets) > 0 {
			args["fast_cache_bypass_domain_sets"] = append([]string(nil), cfg.UDPFastPath.BypassDomainSets...)
		} else {
			delete(args, "fast_cache_bypass_domain_sets")
		}
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
