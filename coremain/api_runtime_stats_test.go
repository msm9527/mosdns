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
	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": mockDomainStatsProvider{
			snapshot: DomainStatsSnapshot{
				MemoryID:     "fakeip_memory",
				Kind:         "fakeip",
				TotalEntries: 101,
				DirtyEntries: 4,
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
	if len(resp.Items) != len(runtimeDomainProfiles) {
		t.Fatalf("unexpected item count: got %d want %d", len(resp.Items), len(runtimeDomainProfiles))
	}
	if resp.Items[0].Key != "fakeip" || resp.Items[0].Tag != "my_fakeiplist" || resp.Items[0].TotalEntries != 101 {
		t.Fatalf("unexpected first domain item: %+v", resp.Items[0])
	}
	foundMissing := false
	for _, item := range resp.Items {
		if item.Tag == "my_realiplist" && item.Error != "" {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Fatal("expected missing domain plugin to return error")
	}
}
