package coremain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockCacheStatsProvider struct {
	snapshot CacheStatsSnapshot
}

func (m mockCacheStatsProvider) SnapshotCacheStats() CacheStatsSnapshot {
	return m.snapshot
}

type mockDomainStatsProvider struct {
	snapshot DomainStatsSnapshot
}

func (m mockDomainStatsProvider) SnapshotDomainStats() DomainStatsSnapshot {
	return m.snapshot
}

func TestHandleAggregatedCacheStats(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"cache_all": mockCacheStatsProvider{
			snapshot: CacheStatsSnapshot{
				Tag:         "cache_all",
				BackendSize: 12,
				L1Size:      3,
				Counters: map[string]uint64{
					"query_total": 20,
					"hit_total":   10,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cache/stats", nil)
	w := httptest.NewRecorder()

	handleAggregatedCacheStats(m).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Items []CacheStatsSnapshot `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if len(resp.Items) != len(runtimeCacheProfiles) {
		t.Fatalf("unexpected item count: got %d want %d", len(resp.Items), len(runtimeCacheProfiles))
	}
	if resp.Items[0].Key != "cache_all" || resp.Items[0].BackendSize != 12 {
		t.Fatalf("unexpected first cache item: %+v", resp.Items[0])
	}
	foundMissing := false
	for _, item := range resp.Items {
		if item.Tag == "cache_cn" && item.Error != "" {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Fatal("expected missing cache plugin to return error")
	}
}

func TestHandleAggregatedDomainStats(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})
	if err := SaveMemoryPoolPoliciesToCustomConfig(map[string]DomainPoolPolicy{
		"my_fakeiplist": DefaultDomainPoolPolicy("my_fakeiplist"),
		"custom_hotlist": {
			Kind:                 DomainPoolKindMemory,
			MaxDomains:           1000,
			MaxVariantsPerDomain: 4,
			EvictionPolicy:       "lru",
			FlushIntervalMS:      1000,
			PublishDebounceMS:    0,
			PruneIntervalSec:     60,
		},
	}); err != nil {
		t.Fatalf("SaveMemoryPoolPoliciesToCustomConfig: %v", err)
	}

	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": mockDomainStatsProvider{
			snapshot: DomainStatsSnapshot{
				MemoryID:     "fakeip_memory",
				Kind:         "fakeip",
				TotalEntries: 101,
				DirtyEntries: 4,
			},
		},
		"custom_hotlist": mockDomainStatsProvider{
			snapshot: DomainStatsSnapshot{
				MemoryID:     "generic",
				Kind:         "memory",
				TotalEntries: 7,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/domain_stats", nil)
	w := httptest.NewRecorder()

	handleAggregatedDomainStats(m).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Items []DomainStatsSnapshot `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	profiles, err := loadRuntimeDomainProfiles()
	if err != nil {
		t.Fatalf("loadRuntimeDomainProfiles: %v", err)
	}
	if len(resp.Items) != len(profiles) {
		t.Fatalf("unexpected item count: got %d want %d", len(resp.Items), len(profiles))
	}
	foundFakeip := false
	foundCustom := false
	foundMissing := false
	for _, item := range resp.Items {
		switch item.Tag {
		case "my_fakeiplist":
			foundFakeip = item.Key == "fakeip" && item.TotalEntries == 101
		case "custom_hotlist":
			foundCustom = item.Key == "custom_hotlist" && item.TotalEntries == 7
		case "my_realiplist":
			foundMissing = item.Error != ""
		}
	}
	if !foundFakeip {
		t.Fatal("expected fakeip item to be present with snapshot values")
	}
	if !foundCustom {
		t.Fatal("expected custom runtime profile to be discovered from memory_pools config")
	}
	if !foundMissing {
		t.Fatal("expected missing domain plugin to return error")
	}
}
