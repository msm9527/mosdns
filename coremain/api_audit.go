package coremain

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type auditSettingsResponse struct {
	Enabled                    bool   `json:"enabled"`
	OverviewWindowSeconds      int    `json:"overview_window_seconds"`
	RawRetentionDays           int    `json:"raw_retention_days"`
	AggregateRetentionDays     int    `json:"aggregate_retention_days"`
	MaxStorageMB               int    `json:"max_storage_mb"`
	SQLitePath                 string `json:"sqlite_path"`
	FlushBatchSize             int    `json:"flush_batch_size"`
	FlushIntervalMs            int    `json:"flush_interval_ms"`
	MaintenanceIntervalSeconds int    `json:"maintenance_interval_seconds"`
	CurrentStorageBytes        int64  `json:"current_storage_bytes"`
	QueueDepth                 int    `json:"queue_depth"`
	Degraded                   bool   `json:"degraded"`
}

func RegisterAuditAPI(router *chi.Mux) {
	router.Route("/api/v3/audit", func(r chi.Router) {
		r.Get("/overview", handleGetAuditOverview)
		r.Get("/timeseries", handleGetAuditTimeseries)
		r.Get("/rank/domain", handleGetAuditDomainRank)
		r.Get("/rank/client", handleGetAuditClientRank)
		r.Get("/rank/domain_set", handleGetAuditDomainSetRank)
		r.Get("/logs", handleGetAuditLogs)
		r.Get("/logs/slow", handleGetAuditSlowLogs)
		r.Get("/settings", handleGetAuditSettings)
		r.Put("/settings", handlePutAuditSettings)
		r.Post("/clear", handleClearAuditLogs)
	})
}

func handleGetAuditOverview(w http.ResponseWriter, r *http.Request) {
	window := queryInt(r, "window", GlobalAuditCollector.GetSettings().OverviewWindowSeconds)
	writeJSON(w, http.StatusOK, GlobalAuditCollector.GetOverview(window))
}

func handleGetAuditTimeseries(w http.ResponseWriter, r *http.Request) {
	params, err := parseAuditTimeseriesQuery(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_AUDIT_TIME_RANGE", err.Error())
		return
	}
	points, err := GlobalAuditCollector.GetTimeseries(params)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "QUERY_AUDIT_TIMESERIES_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, points)
}

func handleGetAuditDomainRank(w http.ResponseWriter, r *http.Request) {
	handleAuditRank(w, r, RankByDomain)
}

func handleGetAuditClientRank(w http.ResponseWriter, r *http.Request) {
	handleAuditRank(w, r, RankByClient)
}

func handleGetAuditDomainSetRank(w http.ResponseWriter, r *http.Request) {
	handleAuditRank(w, r, RankByDomainSet)
}

func handleAuditRank(w http.ResponseWriter, r *http.Request, rankType RankType) {
	params, err := parseAuditRangeQuery(r, 50)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_AUDIT_TIME_RANGE", err.Error())
		return
	}
	items, err := GlobalAuditCollector.GetRank(rankType, params)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "QUERY_AUDIT_RANK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func handleGetAuditLogs(w http.ResponseWriter, r *http.Request) {
	params, err := parseAuditLogsQuery(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_AUDIT_QUERY", err.Error())
		return
	}
	resp, err := GlobalAuditCollector.GetLogs(params)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "QUERY_AUDIT_LOGS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleGetAuditSlowLogs(w http.ResponseWriter, r *http.Request) {
	params, err := parseAuditRangeQuery(r, 100)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_AUDIT_TIME_RANGE", err.Error())
		return
	}
	logs, err := GlobalAuditCollector.GetSlowLogs(params)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "QUERY_AUDIT_SLOW_LOGS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func handleGetAuditSettings(w http.ResponseWriter, r *http.Request) {
	settings := GlobalAuditCollector.GetSettings()
	overview := GlobalAuditCollector.GetOverview(settings.OverviewWindowSeconds)
	writeJSON(w, http.StatusOK, auditSettingsResponse{
		Enabled:                    settings.Enabled,
		OverviewWindowSeconds:      settings.OverviewWindowSeconds,
		RawRetentionDays:           settings.RawRetentionDays,
		AggregateRetentionDays:     settings.AggregateRetentionDays,
		MaxStorageMB:               settings.MaxStorageMB,
		SQLitePath:                 settings.SQLitePath,
		FlushBatchSize:             settings.FlushBatchSize,
		FlushIntervalMs:            settings.FlushIntervalMs,
		MaintenanceIntervalSeconds: settings.MaintenanceIntervalSeconds,
		CurrentStorageBytes:        overview.CurrentStorageBytes,
		QueueDepth:                 overview.QueueDepth,
		Degraded:                   overview.Degraded,
	})
}

func handlePutAuditSettings(w http.ResponseWriter, r *http.Request) {
	var req auditSettingsResponse
	if err := decodeJSONBodyStrict(w, r, &req, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body: "+err.Error())
		return
	}
	settings := AuditSettings{
		Enabled:                    req.Enabled,
		OverviewWindowSeconds:      req.OverviewWindowSeconds,
		RawRetentionDays:           req.RawRetentionDays,
		AggregateRetentionDays:     req.AggregateRetentionDays,
		MaxStorageMB:               req.MaxStorageMB,
		SQLitePath:                 req.SQLitePath,
		FlushBatchSize:             req.FlushBatchSize,
		FlushIntervalMs:            req.FlushIntervalMs,
		MaintenanceIntervalSeconds: req.MaintenanceIntervalSeconds,
	}
	if err := GlobalAuditCollector.SetSettings(settings, MainConfigBaseDir); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SET_AUDIT_SETTINGS_FAILED", err.Error())
		return
	}
	handleGetAuditSettings(w, r)
}

func handleClearAuditLogs(w http.ResponseWriter, r *http.Request) {
	if err := GlobalAuditCollector.ClearLogs(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "CLEAR_AUDIT_LOGS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "审计日志已清空。"})
}

func parseAuditTimeseriesQuery(r *http.Request) (AuditTimeseriesQuery, error) {
	from, to, err := parseAuditTimeRange(r, 6*time.Hour)
	if err != nil {
		return AuditTimeseriesQuery{}, err
	}
	step := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("step")))
	if step == "" {
		if to.Sub(from) > 48*time.Hour {
			step = "hour"
		} else {
			step = "minute"
		}
	}
	if step != "minute" && step != "hour" {
		return AuditTimeseriesQuery{}, errors.New("step must be minute or hour")
	}
	return AuditTimeseriesQuery{From: from, To: to, Step: step}, nil
}

func parseAuditRangeQuery(r *http.Request, defaultLimit int) (AuditRangeQuery, error) {
	from, to, err := parseAuditTimeRange(r, time.Hour)
	if err != nil {
		return AuditRangeQuery{}, err
	}
	return AuditRangeQuery{
		From:  from,
		To:    to,
		Limit: queryInt(r, "limit", defaultLimit),
	}, nil
}

func parseAuditLogsQuery(r *http.Request) (AuditLogsQuery, error) {
	from, to, err := parseAuditTimeRange(r, time.Hour)
	if err != nil {
		return AuditLogsQuery{}, err
	}
	exact, _ := strconv.ParseBool(r.URL.Query().Get("exact"))
	return AuditLogsQuery{
		From:         from,
		To:           to,
		Limit:        queryInt(r, "limit", 100),
		Cursor:       r.URL.Query().Get("cursor"),
		Domain:       r.URL.Query().Get("domain"),
		ClientIP:     r.URL.Query().Get("client_ip"),
		Query:        r.URL.Query().Get("q"),
		ResponseCode: strings.ToUpper(r.URL.Query().Get("rcode")),
		DomainSet:    r.URL.Query().Get("domain_set"),
		CacheStatus:  r.URL.Query().Get("cache_status"),
		UpstreamTag:  r.URL.Query().Get("upstream_tag"),
		Transport:    r.URL.Query().Get("transport"),
		Answer:       r.URL.Query().Get("answer"),
		Exact:        exact,
	}, nil
}

func parseAuditTimeRange(r *http.Request, defaultWindow time.Duration) (time.Time, time.Time, error) {
	now := time.Now()
	from, err := parseAuditTimeValue(r.URL.Query().Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseAuditTimeValue(r.URL.Query().Get("to"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-defaultWindow)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be earlier than to")
	}
	return from, to, nil
}

func parseAuditTimeValue(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if millis, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(millis), nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New("time must be unix milliseconds or RFC3339")
	}
	return value, nil
}

func queryInt(r *http.Request, key string, defaultValue int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}
