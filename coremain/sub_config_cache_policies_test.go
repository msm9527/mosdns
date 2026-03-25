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
	if cfg.Response["cache_fakeip_proxy"].Persist {
		t.Fatalf("expected fakeip proxy cache to default to non-persistent, got %+v", cfg.Response["cache_fakeip_proxy"])
	}
	if cfg.UDPFastPath.InternalTTL != 5 || cfg.UDPFastPath.StaleRetry != 10 {
		t.Fatalf("unexpected udp fast policy: %+v", cfg.UDPFastPath)
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
    persist: false
udp_fast_path:
  internal_ttl: 3
  stale_retry_seconds: 9
  ttl_min: 1
  ttl_max: 3
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
	if cfg.UDPFastPath.InternalTTL != 3 || cfg.UDPFastPath.StaleRetry != 9 || cfg.UDPFastPath.TTLMax != 3 {
		t.Fatalf("unexpected udp fast policy: %+v", cfg.UDPFastPath)
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
		L1Enabled: true, L1TotalCap: 22, Persist: true,
		DumpFile: "db/cache/custom.dump", DumpInterval: 99, WALSyncInterval: 7,
	}
	cfg.UDPFastPath = UDPFastCachePolicy{InternalTTL: 9, StaleRetry: 12, TTLMin: 2, TTLMax: 4}

	pc := PluginConfig{Tag: "cache_main", Type: "cache", Args: map[string]any{"size": 1}}
	if err := ApplyRuntimeCachePolicy(&pc, cfg); err != nil {
		t.Fatalf("ApplyRuntimeCachePolicy(cache): %v", err)
	}
	args := pc.Args.(map[string]any)
	if args["size"] != 123 || args["dump_file"] != "db/cache/custom.dump" {
		t.Fatalf("unexpected cache args: %+v", args)
	}

	udp := PluginConfig{Tag: "udp_main", Type: "udp_server", Args: map[string]any{}}
	if err := ApplyRuntimeCachePolicy(&udp, cfg); err != nil {
		t.Fatalf("ApplyRuntimeCachePolicy(udp): %v", err)
	}
	udpArgs := udp.Args.(map[string]any)
	if udpArgs["fast_cache_internal_ttl"] != 9 || udpArgs["fast_cache_stale_retry_seconds"] != 12 || udpArgs["fast_cache_ttl_max"] != uint32(4) {
		t.Fatalf("unexpected udp args: %+v", udpArgs)
	}
}
