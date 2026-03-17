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
