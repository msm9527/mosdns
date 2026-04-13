package coremain

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestAuditAPIV3OverviewSettingsAndClear(t *testing.T) {
	router, collector, base := newAuditAPITestHarness(t)
	overview := fetchAuditOverview(t, router, "/api/v3/audit/overview?window=3600")
	if !overview.Enabled {
		t.Fatal("overview.Enabled = false, want true")
	}
	if overview.QueryCount != 3 {
		t.Fatalf("overview.QueryCount = %d, want 3", overview.QueryCount)
	}
	if overview.TotalQueryCount != 3 {
		t.Fatalf("overview.TotalQueryCount = %d, want 3", overview.TotalQueryCount)
	}
	if overview.CacheHitCount != 2 {
		t.Fatalf("overview.CacheHitCount = %d, want 2", overview.CacheHitCount)
	}
	if overview.TotalAverageDurationMs != 5 {
		t.Fatalf("overview.TotalAverageDurationMs = %.2f, want 5", overview.TotalAverageDurationMs)
	}
	if len(overview.PeriodSummaries) != 5 {
		t.Fatalf("len(overview.PeriodSummaries) = %d, want 5", len(overview.PeriodSummaries))
	}
	periods := make(map[string]AuditPeriodSummary, len(overview.PeriodSummaries))
	for _, item := range overview.PeriodSummaries {
		periods[item.Key] = item
	}
	assertAuditSummary(t, periods["total"], 3, 5)
	assertAuditSummary(t, periods["7d"], 3, 5)
	assertAuditSummary(t, periods["3d"], 3, 5)
	assertAuditSummary(t, periods["24h"], 3, 5)
	assertAuditSummary(t, periods["1h"], 3, 5)

	settings := fetchAuditSettings(t, router)
	if settings.RawLogCount != 3 {
		t.Fatalf("settings.RawLogCount = %d, want 3", settings.RawLogCount)
	}
	if settings.OldestLogTime == nil || settings.NewestLogTime == nil {
		t.Fatalf("settings log range = (%v, %v), want non-nil", settings.OldestLogTime, settings.NewestLogTime)
	}
	if settings.AllocatedStorageBytes < settings.LiveStorageBytes {
		t.Fatalf("allocated storage = %d, live storage = %d, want allocated >= live", settings.AllocatedStorageBytes, settings.LiveStorageBytes)
	}
	if settings.CurrentStorageBytes != settings.AllocatedStorageBytes {
		t.Fatalf("current storage = %d, allocated storage = %d, want equal", settings.CurrentStorageBytes, settings.AllocatedStorageBytes)
	}

	settings.Enabled = false
	settings.OverviewWindowSeconds = 120
	settings.RawRetentionDays = 14
	settings.AggregateRetentionDays = 45
	settings.MaxStorageMB = 256
	settings.SQLitePath = filepath.Join(MainConfigBaseDir, "custom-audit.db")

	updated := putAuditSettings(t, router, settings)
	if updated.Enabled {
		t.Fatal("updated.Enabled = true, want false")
	}
	if updated.OverviewWindowSeconds != 120 {
		t.Fatalf("updated.OverviewWindowSeconds = %d, want 120", updated.OverviewWindowSeconds)
	}
	if updated.AggregateRetentionDays != 45 {
		t.Fatalf("updated.AggregateRetentionDays = %d, want 45", updated.AggregateRetentionDays)
	}
	if collector.GetSettings().SQLitePath != settings.SQLitePath {
		t.Fatalf("collector SQLitePath = %q, want %q", collector.GetSettings().SQLitePath, settings.SQLitePath)
	}
	updatedConfig, err := os.ReadFile(MainConfigFilePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, want := range []string{
		"enabled: false",
		"overview_window_seconds: 120",
		"raw_retention_days: 14",
		"aggregate_retention_days: 45",
		"max_storage_mb: 256",
		"sqlite_path: " + settings.SQLitePath,
	} {
		if !strings.Contains(string(updatedConfig), want) {
			t.Fatalf("updated config missing %q:\n%s", want, string(updatedConfig))
		}
	}

	postAuditNoBody(t, router, http.MethodPost, "/api/v3/audit/clear")

	overview = fetchAuditOverview(t, router, "/api/v3/audit/overview?window=3600")
	for _, item := range overview.PeriodSummaries {
		assertAuditSummary(t, item, 0, 0)
	}

	logs, err := collector.GetLogs(AuditLogsQuery{
		From:  base.Add(-time.Minute),
		To:    base.Add(5 * time.Minute),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("collector.GetLogs() error = %v", err)
	}
	if logs.Summary.MatchedCount != 0 {
		t.Fatalf("logs.Summary.MatchedCount = %d, want 0", logs.Summary.MatchedCount)
	}
}

func TestAuditAPIV3LogsRankAndTimeseries(t *testing.T) {
	router, _, base := newAuditAPITestHarness(t)
	from := strconv.FormatInt(base.Add(-time.Minute).UnixMilli(), 10)
	to := strconv.FormatInt(base.Add(5*time.Minute).UnixMilli(), 10)
	checkAuditTimeseriesAndRank(t, router, from, to)
	checkAuditCursorLogs(t, router, from, to)
	checkAuditSlowLogs(t, router, from, to)
}

func checkAuditTimeseriesAndRank(t *testing.T, router *chi.Mux, from, to string) {
	t.Helper()
	var points []AuditTimeseriesPoint
	getAuditJSON(t, router, "/api/v3/audit/timeseries?from="+from+"&to="+to+"&step=minute", &points)
	if len(points) == 0 {
		t.Fatal("len(points) = 0, want > 0")
	}

	var domainRank []AuditRankItem
	getAuditJSON(t, router, "/api/v3/audit/rank/domain?from="+from+"&to="+to+"&limit=2", &domainRank)
	if len(domainRank) != 2 {
		t.Fatalf("len(domainRank) = %d, want 2", len(domainRank))
	}
}

func checkAuditCursorLogs(t *testing.T, router *chi.Mux, from, to string) {
	t.Helper()
	var resp AuditLogsResponse
	getAuditJSON(t, router, "/api/v3/audit/logs?from="+from+"&to="+to+"&limit=2&q=example", &resp)
	if resp.Summary.MatchedCount != 3 {
		t.Fatalf("resp.Summary.MatchedCount = %d, want 3", resp.Summary.MatchedCount)
	}
	if len(resp.Logs) != 2 {
		t.Fatalf("len(resp.Logs) = %d, want 2", len(resp.Logs))
	}
	if resp.NextCursor == "" {
		t.Fatal("resp.NextCursor is empty")
	}

	var next AuditLogsResponse
	getAuditJSON(t, router, "/api/v3/audit/logs?from="+from+"&to="+to+"&limit=2&cursor="+resp.NextCursor, &next)
	if len(next.Logs) != 1 {
		t.Fatalf("len(next.Logs) = %d, want 1", len(next.Logs))
	}
}

func checkAuditSlowLogs(t *testing.T, router *chi.Mux, from, to string) {
	t.Helper()
	var slowLogs []AuditLog
	getAuditJSON(t, router, "/api/v3/audit/logs/slow?from="+from+"&to="+to+"&limit=1", &slowLogs)
	if len(slowLogs) != 1 {
		t.Fatalf("len(slowLogs) = %d, want 1", len(slowLogs))
	}
	if slowLogs[0].QueryName != "three.example" {
		t.Fatalf("slowLogs[0].QueryName = %q, want %q", slowLogs[0].QueryName, "three.example")
	}
}

func newAuditAPITestHarness(t *testing.T) (*chi.Mux, *AuditCollector, time.Time) {
	t.Helper()

	oldCollector := GlobalAuditCollector
	oldBaseDir := MainConfigBaseDir
	oldFilePath := MainConfigFilePath
	MainConfigBaseDir = t.TempDir()
	MainConfigFilePath = filepath.Join(MainConfigBaseDir, "config.yaml")
	writeAuditMainConfigForTest(t, MainConfigFilePath)

	collector := NewAuditCollector(AuditSettings{
		Enabled:                true,
		SQLitePath:             "db/audit.db",
		OverviewWindowSeconds:  60,
		RawRetentionDays:       7,
		AggregateRetentionDays: 30,
		MaxStorageMB:           128,
	}, MainConfigBaseDir)
	GlobalAuditCollector = collector

	base := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	logs := []AuditLog{
		testAuditLog("one.example", base.Add(10*time.Second), 2, "NOERROR", "domestic", AuditCacheHit),
		testAuditLog("two.example", base.Add(70*time.Second), 5, "SERVFAIL", "foreign", AuditCacheMiss),
		testAuditLog("three.example", base.Add(80*time.Second), 8, "NOERROR", "foreign", AuditCacheLazy),
	}
	for _, log := range logs {
		collector.realtime.Record(log)
	}
	if err := collector.storage.WriteBatch(logs); err != nil {
		t.Fatalf("collector.storage.WriteBatch() error = %v", err)
	}

	t.Cleanup(func() {
		GlobalAuditCollector = oldCollector
		MainConfigBaseDir = oldBaseDir
		MainConfigFilePath = oldFilePath
		collector.closeStorage()
	})

	router := chi.NewMux()
	RegisterAuditAPI(router)
	return router, collector, base
}

func writeAuditMainConfigForTest(t *testing.T, path string) {
	t.Helper()
	content := `version: v2
audit:
  enabled: true
  overview_window_seconds: 60
  raw_retention_days: 7
  aggregate_retention_days: 30
  max_storage_mb: 128
  sqlite_path: db/audit.db
  flush_batch_size: 256
  flush_interval_ms: 250
  maintenance_interval_seconds: 60
storage:
  control_db: db/control.db
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func decodeAuditResponse(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(dst); err != nil {
		t.Fatalf("json decode error = %v, body = %s", err, rec.Body.String())
	}
}

func fetchAuditOverview(t *testing.T, router *chi.Mux, path string) AuditOverview {
	t.Helper()
	var overview AuditOverview
	getAuditJSON(t, router, path, &overview)
	return overview
}

func fetchAuditSettings(t *testing.T, router *chi.Mux) auditSettingsResponse {
	t.Helper()
	var settings auditSettingsResponse
	getAuditJSON(t, router, "/api/v3/audit/settings", &settings)
	return settings
}

func putAuditSettings(t *testing.T, router *chi.Mux, settings auditSettingsResponse) auditSettingsResponse {
	t.Helper()
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v3/audit/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertStatusCode(t, rec.Code, http.StatusOK)

	var updated auditSettingsResponse
	decodeAuditResponse(t, rec, &updated)
	return updated
}

func postAuditNoBody(t *testing.T, router *chi.Mux, method, path string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertStatusCode(t, rec.Code, http.StatusOK)
}

func getAuditJSON(t *testing.T, router *chi.Mux, path string, dst any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertStatusCode(t, rec.Code, http.StatusOK)
	decodeAuditResponse(t, rec, dst)
}

func assertStatusCode(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}
