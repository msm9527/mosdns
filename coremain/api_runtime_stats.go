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
	Key                 string `json:"key"`
	Name                string `json:"name"`
	Tag                 string `json:"tag"`
	MemoryID            string `json:"memory_id,omitempty"`
	Kind                string `json:"kind,omitempty"`
	TotalEntries        int    `json:"total_entries"`
	DirtyEntries        int    `json:"dirty_entries"`
	PromotedEntries     int64  `json:"promoted_entries"`
	PublishedRules      int64  `json:"published_rules"`
	TotalObservations   int64  `json:"total_observations"`
	DroppedObservations int64  `json:"dropped_observations"`
	DroppedByBuffer     int64  `json:"dropped_by_buffer"`
	DroppedByCap        int64  `json:"dropped_by_cap"`
	Error               string `json:"error,omitempty"`
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
	{Key: "cache_all", Name: "全部缓存 (兼容)", Tag: "cache_all"},
	{Key: "cache_cn", Name: "国内缓存", Tag: "cache_cn"},
	{Key: "cache_node", Name: "节点缓存", Tag: "cache_node"},
	{Key: "cache_google", Name: "国外缓存 (兼容)", Tag: "cache_google"},
	{Key: "cache_all_noleak", Name: "全部缓存 (安全)", Tag: "cache_all_noleak"},
	{Key: "cache_google_node", Name: "国外缓存 (安全)", Tag: "cache_google_node"},
	{Key: "cache_cnmihomo", Name: "国内域名fakeip", Tag: "cache_cnmihomo"},
}

var runtimeDomainProfiles = []runtimeDomainProfile{
	{Key: "fakeip", Name: "FakeIP 域名", Tag: "my_fakeiplist"},
	{Key: "realip", Name: "RealIP 域名", Tag: "my_realiplist"},
	{Key: "nov4", Name: "无 V4 域名", Tag: "my_nov4list"},
	{Key: "nov6", Name: "无 V6 域名", Tag: "my_nov6list"},
	{Key: "total", Name: "请求排行", Tag: "top_domains"},
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
		items := make([]DomainStatsSnapshot, 0, len(runtimeDomainProfiles))
		for _, profile := range runtimeDomainProfiles {
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
