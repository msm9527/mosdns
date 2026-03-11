package cache

import (
	"sync"
	"time"
)

type cacheOpStatus struct {
	Status   string    `json:"status"`
	At       time.Time `json:"at,omitempty"`
	Duration string    `json:"duration,omitempty"`
	Entries  int       `json:"entries,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type cacheRuntimeState struct {
	mu         sync.RWMutex
	snapshot   string
	wal        string
	lastDump   cacheOpStatus
	lastLoad   cacheOpStatus
	lastReplay cacheOpStatus
}

type cacheStatsResponse struct {
	Tag          string                 `json:"tag,omitempty"`
	SnapshotFile string                 `json:"snapshot_file,omitempty"`
	WALFile      string                 `json:"wal_file,omitempty"`
	BackendSize  int                    `json:"backend_size"`
	L1Size       int                    `json:"l1_size"`
	UpdatedKeys  uint64                 `json:"updated_keys"`
	Counters     map[string]uint64      `json:"counters"`
	LastDump     cacheOpStatus          `json:"last_dump"`
	LastLoad     cacheOpStatus          `json:"last_load"`
	LastReplay   cacheOpStatus          `json:"last_wal_replay"`
	Config       map[string]interface{} `json:"config"`
}

func newCacheRuntimeState(snapshotPath, walPath string) *cacheRuntimeState {
	return &cacheRuntimeState{
		snapshot:   snapshotPath,
		wal:        walPath,
		lastDump:   cacheOpStatus{Status: "not_run"},
		lastLoad:   cacheOpStatus{Status: "not_run"},
		lastReplay: cacheOpStatus{Status: "not_run"},
	}
}

func (s *cacheRuntimeState) recordDump(entries int, dur time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastDump = newCacheOpStatus(entries, dur, err)
}

func (s *cacheRuntimeState) recordLoad(entries int, dur time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = newCacheOpStatus(entries, dur, err)
}

func (s *cacheRuntimeState) recordReplay(entries int, dur time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReplay = newCacheOpStatus(entries, dur, err)
}

func (s *cacheRuntimeState) snapshotState() (string, string, cacheOpStatus, cacheOpStatus, cacheOpStatus) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot, s.wal, s.lastDump, s.lastLoad, s.lastReplay
}

func newCacheOpStatus(entries int, dur time.Duration, err error) cacheOpStatus {
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	return cacheOpStatus{
		Status:   status,
		At:       time.Now(),
		Duration: dur.String(),
		Entries:  entries,
		Error:    errMsg,
	}
}

func (c *Cache) l1Len() int {
	if !c.l1Enabled {
		return 0
	}
	total := 0
	for i := 0; i < shardCount; i++ {
		shard := c.shards[i]
		shard.RLock()
		total += len(shard.items)
		shard.RUnlock()
	}
	return total
}

func (c *Cache) snapshotStats() cacheStatsResponse {
	snapshotPath, walPath, lastDump, lastLoad, lastReplay := c.runtimeState.snapshotState()
	return cacheStatsResponse{
		Tag:          c.metricsTag,
		SnapshotFile: snapshotPath,
		WALFile:      walPath,
		BackendSize:  c.backend.Len(),
		L1Size:       c.l1Len(),
		UpdatedKeys:  c.updatedKey.Load(),
		Counters: map[string]uint64{
			"query_total":               c.queryCount.Load(),
			"hit_total":                 c.hitCount.Load(),
			"l1_hit_total":              c.l1HitCount.Load(),
			"l2_hit_total":              c.l2HitCount.Load(),
			"lazy_hit_total":            c.lazyHitCount.Load(),
			"lazy_update_total":         c.lazyUpdateCount.Load(),
			"lazy_update_dropped_total": c.lazyUpdateDroppedCount.Load(),
		},
		LastDump:   lastDump,
		LastLoad:   lastLoad,
		LastReplay: lastReplay,
		Config: map[string]interface{}{
			"size":              c.args.Size,
			"lazy_cache_ttl":    c.args.LazyCacheTTL,
			"l1_enabled":        c.l1Enabled,
			"l1_total_cap":      c.args.L1TotalCap,
			"l1_shard_cap":      c.l1ShardCap,
			"enable_ecs":        c.args.EnableECS,
			"dump_interval":     c.args.DumpInterval,
			"wal_sync_interval": c.args.WALSyncInterval,
		},
	}
}
