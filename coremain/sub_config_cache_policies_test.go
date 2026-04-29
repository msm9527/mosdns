package coremain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCachePolicyConfigFromSubConfigDefaults(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if ok {
		t.Fatal("expected cache policy file to be absent")
	}
	if cfg.Response["cache_main"].Size <= 0 {
		t.Fatalf("expected default cache_main policy, got %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].LazyCacheTTL != 1800 {
		t.Fatalf("expected default cache_main lazy ttl 1800, got %+v", cfg.Response["cache_main"])
	}
	if got := cfg.Response["cache_main"].BypassDomainSets; len(got) != 1 || got[0] != "DDNS域名" {
		t.Fatalf("expected default cache_main bypass domain sets, got %+v", got)
	}
	if cfg.Response["cache_fakeip_proxy"].Persist {
		t.Fatalf("expected fakeip proxy cache to default to non-persistent, got %+v", cfg.Response["cache_fakeip_proxy"])
	}
	if cfg.UDPFastPath.InternalTTL != 5 || cfg.UDPFastPath.StaleRetry != 10 {
		t.Fatalf("unexpected udp fast policy: %+v", cfg.UDPFastPath)
	}
	if got := cfg.UDPFastPath.BypassDomainSets; len(got) != 1 || got[0] != "DDNS域名" {
		t.Fatalf("expected default udp fast bypass domain sets, got %+v", got)
	}
}

func TestDefaultCachePolicyConfigUsesConservativeMemoryProfile(t *testing.T) {
	cfg := defaultCachePolicyConfig()

	totalSize := 0
	totalL1Cap := 0
	for _, policy := range cfg.Response {
		totalSize += policy.Size
		totalL1Cap += policy.L1TotalCap
	}

	if cfg.Response["cache_main"].Size != defaultCacheMainSize {
		t.Fatalf("cache_main size = %d, want %d", cfg.Response["cache_main"].Size, defaultCacheMainSize)
	}
	if cfg.Response["cache_branch_foreign"].LazyCacheTTL != 1800 {
		t.Fatalf("cache_branch_foreign lazy ttl = %d, want 1800", cfg.Response["cache_branch_foreign"].LazyCacheTTL)
	}
	if totalSize > 120000 {
		t.Fatalf("default cache total size is too large: %d", totalSize)
	}
	if totalL1Cap > 3500 {
		t.Fatalf("default cache total l1 cap is too large: %d", totalL1Cap)
	}
}

func TestRepoCachePoliciesTemplateUsesConservativeMemoryProfile(t *testing.T) {
	baseDir := filepath.Join("..", "config")
	cfg, ok, err := LoadCachePolicyConfigFromSubConfigForBaseDir(baseDir)
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfigForBaseDir: %v", err)
	}
	if !ok {
		t.Fatal("expected repo cache policy template to exist")
	}

	totalSize := 0
	totalL1Cap := 0
	for _, policy := range cfg.Response {
		totalSize += policy.Size
		totalL1Cap += policy.L1TotalCap
	}

	if cfg.Response["cache_main"].Size != defaultCacheMainSize {
		t.Fatalf("template cache_main size = %d, want %d", cfg.Response["cache_main"].Size, defaultCacheMainSize)
	}
	if totalSize > 120000 {
		t.Fatalf("template cache total size is too large: %d", totalSize)
	}
	if totalL1Cap > 3500 {
		t.Fatalf("template cache total l1 cap is too large: %d", totalL1Cap)
	}
}

func TestLoadCachePolicyConfigFromSubConfigOverride(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	path := filepath.Join(MainConfigBaseDir, cachePoliciesConfigRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	const body = `
response:
  cache_main:
    size: 2048
    lazy_cache_ttl: 120
    bypass_domain_sets:
      - 高变CDN
      - DDNS域名
    persist: false
udp_fast_path:
  internal_ttl: 3
  stale_retry_seconds: 9
  ttl_min: 1
  ttl_max: 3
  bypass_domain_sets:
    - 高变CDN
    - DDNS域名
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected cache policy file to exist")
	}
	if cfg.Response["cache_main"].Size != 2048 || cfg.Response["cache_main"].Persist {
		t.Fatalf("unexpected cache_main policy: %+v", cfg.Response["cache_main"])
	}
	if got := cfg.Response["cache_main"].BypassDomainSets; len(got) != 2 || got[0] != "DDNS域名" || got[1] != "高变CDN" {
		t.Fatalf("unexpected cache_main bypass domain sets: %+v", got)
	}
	if cfg.UDPFastPath.InternalTTL != 3 || cfg.UDPFastPath.StaleRetry != 9 || cfg.UDPFastPath.TTLMax != 3 {
		t.Fatalf("unexpected udp fast policy: %+v", cfg.UDPFastPath)
	}
	if got := cfg.UDPFastPath.BypassDomainSets; len(got) != 2 || got[0] != "DDNS域名" || got[1] != "高变CDN" {
		t.Fatalf("unexpected udp fast bypass domain sets: %+v", got)
	}
}

func TestLoadCachePolicyConfigFromSubConfigIgnoresUnknownPolicy(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	path := filepath.Join(MainConfigBaseDir, cachePoliciesConfigRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	const body = `
response:
  cache_main:
    size: 4096
  cache_legacy_removed:
    size: 1
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected cache policy file to exist")
	}
	if cfg.Response["cache_main"].Size != 4096 {
		t.Fatalf("expected cache_main override to survive, got %+v", cfg.Response["cache_main"])
	}
	if _, exists := cfg.Response["cache_legacy_removed"]; exists {
		t.Fatalf("expected unknown legacy cache policy to be ignored, got %+v", cfg.Response)
	}
}

func TestApplyRuntimeCachePolicy(t *testing.T) {
	cfg := defaultCachePolicyConfig()
	cfg.Response["cache_main"] = CachePolicy{
		Size: 123, LazyCacheTTL: 45, NXDomainTTL: 11, ServfailTTL: 12,
		L1Enabled: true, L1TotalCap: 22, BypassDomainSets: []string{"DDNS域名"}, Persist: true,
		DumpFile: "db/cache/custom.dump", DumpInterval: 99, WALSyncInterval: 7,
	}
	cfg.UDPFastPath = UDPFastCachePolicy{
		InternalTTL:      9,
		StaleRetry:       12,
		TTLMin:           2,
		TTLMax:           4,
		BypassDomainSets: []string{"DDNS域名"},
	}

	pc := PluginConfig{Tag: "cache_main", Type: "cache", Args: map[string]any{"size": 1}}
	if err := ApplyRuntimeCachePolicy(&pc, cfg); err != nil {
		t.Fatalf("ApplyRuntimeCachePolicy(cache): %v", err)
	}
	args := pc.Args.(map[string]any)
	if args["size"] != 123 || args["dump_file"] != "db/cache/custom.dump" {
		t.Fatalf("unexpected cache args: %+v", args)
	}
	bypassDomainSets, ok := args["bypass_domain_sets"].([]string)
	if !ok || len(bypassDomainSets) != 1 || bypassDomainSets[0] != "DDNS域名" {
		t.Fatalf("unexpected bypass domain sets: %+v", args["bypass_domain_sets"])
	}

	udp := PluginConfig{Tag: "udp_main", Type: "udp_server", Args: map[string]any{}}
	if err := ApplyRuntimeCachePolicy(&udp, cfg); err != nil {
		t.Fatalf("ApplyRuntimeCachePolicy(udp): %v", err)
	}
	udpArgs := udp.Args.(map[string]any)
	if udpArgs["fast_cache_internal_ttl"] != 9 || udpArgs["fast_cache_stale_retry_seconds"] != 12 || udpArgs["fast_cache_ttl_max"] != uint32(4) {
		t.Fatalf("unexpected udp args: %+v", udpArgs)
	}
	udpBypassDomainSets, ok := udpArgs["fast_cache_bypass_domain_sets"].([]string)
	if !ok || len(udpBypassDomainSets) != 1 || udpBypassDomainSets[0] != "DDNS域名" {
		t.Fatalf("unexpected udp bypass domain sets: %+v", udpArgs["fast_cache_bypass_domain_sets"])
	}
}
