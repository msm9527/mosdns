package coremain

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type CacheStatsSnapshot struct {
	Key          string                 `json:"key"`
	Name         string                 `json:"name"`
	Tag          string                 `json:"tag"`
	SnapshotFile string                 `json:"snapshot_file,omitempty"`
	WALFile      string                 `json:"wal_file,omitempty"`
	BackendSize  int                    `json:"backend_size"`
	L1Size       int                    `json:"l1_size"`
	UpdatedKeys  uint64                 `json:"updated_keys"`
	Counters     map[string]uint64      `json:"counters"`
	LastDump     map[string]any         `json:"last_dump,omitempty"`
	LastLoad     map[string]any         `json:"last_load,omitempty"`
	LastReplay   map[string]any         `json:"last_wal_replay,omitempty"`
	Config       map[string]interface{} `json:"config,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

type DomainStatsSnapshot struct {
	Key                  string `json:"key"`
	Name                 string `json:"name"`
	Tag                  string `json:"tag"`
	MemoryID             string `json:"memory_id,omitempty"`
	Kind                 string `json:"kind,omitempty"`
	TotalEntries         int    `json:"total_entries"`
	DirtyEntries         int    `json:"dirty_entries"`
	PromotedEntries      int64  `json:"promoted_entries"`
	PublishedRules       int64  `json:"published_rules"`
	HotRules             int64  `json:"hot_rules,omitempty"`
	HotPendingRules      int64  `json:"hot_pending_rules,omitempty"`
	HotAddTotal          int64  `json:"hot_add_total,omitempty"`
	HotReplaceTotal      int64  `json:"hot_replace_total,omitempty"`
	HotDispatchFailTotal int64  `json:"hot_dispatch_fail_total,omitempty"`
	LastHotSyncAtUnixMS  int64  `json:"last_hot_sync_at_unix_ms,omitempty"`
	TotalObservations    int64  `json:"total_observations"`
	DroppedObservations  int64  `json:"dropped_observations"`
	DroppedByBuffer      int64  `json:"dropped_by_buffer"`
	DroppedByCap         int64  `json:"dropped_by_cap"`
	Error                string `json:"error,omitempty"`
}

type CacheStatsProvider interface {
	SnapshotCacheStats() CacheStatsSnapshot
}

type DomainStatsProvider interface {
	SnapshotDomainStats() DomainStatsSnapshot
}

type runtimeCacheProfile struct {
	Key  string
	Name string
	Tag  string
}

type runtimeDomainProfile struct {
	Key  string
	Name string
	Tag  string
}

var runtimeCacheProfiles = []runtimeCacheProfile{
	{Key: "cache_main", Name: "主缓存", Tag: "cache_main"},
	{Key: "cache_branch_domestic", Name: "国内分支缓存", Tag: "cache_branch_domestic"},
	{Key: "cache_branch_foreign", Name: "国外分支缓存", Tag: "cache_branch_foreign"},
	{Key: "cache_branch_foreign_ecs", Name: "国外 ECS 分支缓存", Tag: "cache_branch_foreign_ecs"},
	{Key: "cache_fakeip_domestic", Name: "国内 FakeIP 缓存", Tag: "cache_fakeip_domestic"},
	{Key: "cache_fakeip_proxy", Name: "代理 FakeIP 缓存", Tag: "cache_fakeip_proxy"},
	{Key: "cache_probe", Name: "节点探测缓存", Tag: "cache_probe"},
}

func RegisterRuntimeStatsAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/cache/stats", handleAggregatedCacheStats(m))
	router.Get("/api/v1/data/domain_stats", handleAggregatedDomainStats(m))
}

func handleAggregatedCacheStats(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items := make([]CacheStatsSnapshot, 0, len(runtimeCacheProfiles))
		for _, profile := range runtimeCacheProfiles {
			item := CacheStatsSnapshot{
				Key:      profile.Key,
				Name:     profile.Name,
				Tag:      profile.Tag,
				Counters: map[string]uint64{},
			}

			if provider, ok := m.GetPlugin(profile.Tag).(CacheStatsProvider); ok && provider != nil {
				item = provider.SnapshotCacheStats()
				item.Key = profile.Key
				item.Name = profile.Name
				if item.Tag == "" {
					item.Tag = profile.Tag
				}
			} else {
				item.Error = "plugin not found or stats unavailable"
			}
			items = append(items, item)
		}

		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func handleAggregatedDomainStats(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profiles, err := loadRuntimeDomainProfiles()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "domain_stats_profiles_failed", err.Error())
			return
		}

		items := make([]DomainStatsSnapshot, 0, len(profiles))
		for _, profile := range profiles {
			item := DomainStatsSnapshot{
				Key:  profile.Key,
				Name: profile.Name,
				Tag:  profile.Tag,
			}

			if provider, ok := m.GetPlugin(profile.Tag).(DomainStatsProvider); ok && provider != nil {
				item = provider.SnapshotDomainStats()
				item.Key = profile.Key
				item.Name = profile.Name
				if item.Tag == "" {
					item.Tag = profile.Tag
				}
			} else {
				item.Error = "plugin not found or stats unavailable"
			}
			items = append(items, item)
		}

		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}
