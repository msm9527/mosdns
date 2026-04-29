package coremain

import "time"

type AnswerDetail struct {
	Type string `json:"type"`
	TTL  uint32 `json:"ttl"`
	Data string `json:"data"`
}

type ResponseFlags struct {
	AA bool `json:"aa"`
	TC bool `json:"tc"`
	RA bool `json:"ra"`
}

type AuditLog struct {
	ID            int64          `json:"id"`
	QueryTime     time.Time      `json:"query_time"`
	ClientIP      string         `json:"client_ip"`
	QueryType     string         `json:"query_type"`
	QueryName     string         `json:"query_name"`
	QueryClass    string         `json:"query_class"`
	DurationMs    float64        `json:"duration_ms"`
	TraceID       string         `json:"trace_id"`
	ResponseCode  string         `json:"response_code"`
	ResponseFlags ResponseFlags  `json:"response_flags"`
	Answers       []AnswerDetail `json:"answers"`
	AnswerCount   int            `json:"answer_count"`
	DomainSetRaw  string         `json:"domain_set_raw"`
	DomainSetNorm string         `json:"domain_set_norm"`
	UpstreamTag   string         `json:"upstream_tag"`
	Transport     string         `json:"transport"`
	ServerName    string         `json:"server_name"`
	URLPath       string         `json:"url_path"`
	CacheStatus   string         `json:"cache_status"`
}

type AuditSettings struct {
	Enabled                    bool   `json:"enabled" yaml:"enabled,omitempty"`
	OverviewWindowSeconds      int    `json:"overview_window_seconds" yaml:"overview_window_seconds,omitempty"`
	RawRetentionDays           int    `json:"raw_retention_days" yaml:"raw_retention_days,omitempty"`
	AggregateRetentionDays     int    `json:"aggregate_retention_days" yaml:"aggregate_retention_days,omitempty"`
	MaxStorageMB               int    `json:"max_storage_mb" yaml:"max_storage_mb,omitempty"`
	SQLitePath                 string `json:"sqlite_path,omitempty" yaml:"sqlite_path,omitempty"`
	FlushBatchSize             int    `json:"flush_batch_size,omitempty" yaml:"flush_batch_size,omitempty"`
	FlushIntervalMs            int    `json:"flush_interval_ms,omitempty" yaml:"flush_interval_ms,omitempty"`
	MaintenanceIntervalSeconds int    `json:"maintenance_interval_seconds,omitempty" yaml:"maintenance_interval_seconds,omitempty"`
}

type AuditStorageStats struct {
	AllocatedBytes   int64      `json:"allocated_storage_bytes"`
	LiveBytes        int64      `json:"live_storage_bytes"`
	ReclaimableBytes int64      `json:"reclaimable_storage_bytes"`
	RawLogCount      int64      `json:"raw_log_count"`
	OldestLogTime    *time.Time `json:"oldest_log_time,omitempty"`
	NewestLogTime    *time.Time `json:"newest_log_time,omitempty"`
}

type AuditOverview struct {
	Enabled                        bool                 `json:"enabled"`
	WindowSeconds                  int                  `json:"window_seconds"`
	TotalQueryCount                uint64               `json:"total_query_count"`
	TotalAverageDurationMs         float64              `json:"total_average_duration_ms"`
	ResolvedTotalQueryCount        uint64               `json:"resolved_total_query_count"`
	ResolvedTotalAverageDurationMs float64              `json:"resolved_total_average_duration_ms"`
	PeriodSummaries                []AuditPeriodSummary `json:"period_summaries,omitempty"`
	QueryCount                     uint64               `json:"query_count"`
	QPS                            float64              `json:"qps"`
	AverageDurationMs              float64              `json:"average_duration_ms"`
	MaxDurationMs                  float64              `json:"max_duration_ms"`
	ResolvedQueryCount             uint64               `json:"resolved_query_count"`
	ResolvedQPS                    float64              `json:"resolved_qps"`
	ResolvedAverageDurationMs      float64              `json:"resolved_average_duration_ms"`
	ResolvedMaxDurationMs          float64              `json:"resolved_max_duration_ms"`
	ErrorCount                     uint64               `json:"error_count"`
	ErrorRate                      float64              `json:"error_rate"`
	NoResponseCount                uint64               `json:"no_response_count"`
	CacheHitCount                  uint64               `json:"cache_hit_count"`
	CacheHitRate                   float64              `json:"cache_hit_rate"`
	DroppedEvents                  uint64               `json:"dropped_events"`
	QueueDepth                     int                  `json:"queue_depth"`
	Degraded                       bool                 `json:"degraded"`
	CurrentStorageBytes            int64                `json:"current_storage_bytes"`
}

type AuditPeriodSummary struct {
	Key                       string  `json:"key"`
	Label                     string  `json:"label"`
	WindowSeconds             int     `json:"window_seconds,omitempty"`
	QueryCount                uint64  `json:"query_count"`
	AverageDurationMs         float64 `json:"average_duration_ms"`
	ResolvedQueryCount        uint64  `json:"resolved_query_count"`
	ResolvedAverageDurationMs float64 `json:"resolved_average_duration_ms"`
}

type AuditTimeseriesPoint struct {
	BucketStart               time.Time `json:"bucket_start"`
	QueryCount                int       `json:"query_count"`
	AverageDurationMs         float64   `json:"average_duration_ms"`
	MaxDurationMs             float64   `json:"max_duration_ms"`
	ResolvedQueryCount        int       `json:"resolved_query_count"`
	ResolvedAverageDurationMs float64   `json:"resolved_average_duration_ms"`
	ResolvedMaxDurationMs     float64   `json:"resolved_max_duration_ms"`
	ErrorCount                int       `json:"error_count"`
	CacheHitCount             int       `json:"cache_hit_count"`
}

type AuditRankItem struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type AuditLogsSummary struct {
	MatchedCount      int     `json:"matched_count"`
	AverageDurationMs float64 `json:"average_duration_ms"`
	MaxDurationMs     float64 `json:"max_duration_ms"`
}

type AuditLogsResponse struct {
	Summary    AuditLogsSummary `json:"summary"`
	Logs       []AuditLog       `json:"logs"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type AuditRangeQuery struct {
	From  time.Time
	To    time.Time
	Limit int
}

type AuditTimeseriesQuery struct {
	From time.Time
	To   time.Time
	Step string
}

type auditAggregateRow struct {
	BucketStartUnix       int64
	QueryCount            int
	DurationSumMs         float64
	DurationMaxMs         float64
	ResolvedQueryCount    int
	ResolvedDurationSumMs float64
	ResolvedDurationMaxMs float64
	ErrorCount            int
	NoResponseCount       int
	CacheHitCount         int
}

func (r auditAggregateRow) avgDurationMs() float64 {
	if r.QueryCount == 0 {
		return 0
	}
	return r.DurationSumMs / float64(r.QueryCount)
}

func (r auditAggregateRow) resolvedAvgDurationMs() float64 {
	if r.ResolvedQueryCount == 0 {
		return 0
	}
	return r.ResolvedDurationSumMs / float64(r.ResolvedQueryCount)
}
