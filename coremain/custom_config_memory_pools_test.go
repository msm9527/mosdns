package coremain

import "testing"

func TestMemoryPoolsCustomConfigRoundTrip(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	values := defaultDomainPoolPolicyMap()
	policy := values["my_realiplist"]
	policy.MaxDomains = 1234
	policy.TrackFlags = true
	values["my_realiplist"] = policy

	if err := SaveMemoryPoolPoliciesToCustomConfig(values); err != nil {
		t.Fatalf("SaveMemoryPoolPoliciesToCustomConfig: %v", err)
	}

	loaded, ok, err := LoadMemoryPoolPoliciesFromCustomConfig()
	if err != nil {
		t.Fatalf("LoadMemoryPoolPoliciesFromCustomConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected memory_pools.yaml to exist")
	}
	if loaded["my_realiplist"].MaxDomains != 1234 {
		t.Fatalf("unexpected max_domains: %+v", loaded["my_realiplist"])
	}
	if !loaded["my_realiplist"].TrackFlags {
		t.Fatalf("expected track_flags to round-trip: %+v", loaded["my_realiplist"])
	}
	if loaded["top_domains"].Kind != DomainPoolKindStats {
		t.Fatalf("expected top_domains to keep stats kind: %+v", loaded["top_domains"])
	}
}

func TestLoadMemoryPoolsCustomConfigFallsBackToDefaults(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	values, ok, err := LoadMemoryPoolPoliciesFromCustomConfig()
	if err != nil {
		t.Fatalf("LoadMemoryPoolPoliciesFromCustomConfig: %v", err)
	}
	if ok {
		t.Fatal("expected memory_pools.yaml to be absent")
	}
	if values["my_fakeiplist"].Kind != DomainPoolKindMemory {
		t.Fatalf("unexpected defaults: %+v", values["my_fakeiplist"])
	}
}

func TestDefaultMemoryPoolPoliciesUseConservativeCapacities(t *testing.T) {
	values := defaultDomainPoolPolicyMap()
	totalMaxDomains := 0
	for _, policy := range values {
		totalMaxDomains += policy.MaxDomains
	}

	if values["top_domains"].MaxDomains != defaultTopDomainsMaxDomains {
		t.Fatalf("top_domains max_domains = %d, want %d", values["top_domains"].MaxDomains, defaultTopDomainsMaxDomains)
	}
	if values["my_realiplist"].MaxDomains != defaultRealIPPoolMaxDomains {
		t.Fatalf("my_realiplist max_domains = %d, want %d", values["my_realiplist"].MaxDomains, defaultRealIPPoolMaxDomains)
	}
	if totalMaxDomains > 90000 {
		t.Fatalf("default memory pool total max_domains is too large: %d", totalMaxDomains)
	}
}
