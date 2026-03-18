package coremain

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestAuditAPIV3SearchLogs(t *testing.T) {
	router, _, base := newAuditSearchAPITestHarness(t)
	body := map[string]any{
		"time_range": map[string]any{
			"from": base.UnixMilli(),
			"to":   base.Add(3 * time.Minute).UnixMilli(),
		},
		"page": map[string]any{
			"limit": 2,
		},
		"keyword": map[string]any{
			"value":  "dns.google",
			"mode":   "fuzzy",
			"fields": []string{"server_name"},
		},
		"filters": map[string]any{
			"response_code": "NOERROR",
			"has_answer":    true,
		},
	}

	var resp AuditLogsResponse
	postAuditJSON(t, router, "/api/v3/audit/logs/search", body, http.StatusOK, &resp)
	if resp.Summary.MatchedCount != 1 {
		t.Fatalf("MatchedCount = %d, want 1", resp.Summary.MatchedCount)
	}
	if len(resp.Logs) != 1 || resp.Logs[0].QueryName != "alpha.example" {
		t.Fatalf("unexpected logs = %+v", resp.Logs)
	}
}

func TestAuditAPIV3SearchLogsRejectInvalidMode(t *testing.T) {
	router, _, _ := newAuditSearchAPITestHarness(t)
	body := map[string]any{
		"keyword": map[string]any{
			"value": "example",
			"mode":  "broken",
		},
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v3/audit/logs/search", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertStatusCode(t, rec.Code, http.StatusBadRequest)
}

func newAuditSearchAPITestHarness(t *testing.T) (*chi.Mux, *AuditCollector, time.Time) {
	t.Helper()

	oldCollector := GlobalAuditCollector
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()

	collector := NewAuditCollector(AuditSettings{
		Enabled:                true,
		SQLitePath:             filepath.Join(MainConfigBaseDir, "audit.db"),
		OverviewWindowSeconds:  60,
		RawRetentionDays:       7,
		AggregateRetentionDays: 30,
		MaxStorageMB:           128,
	}, MainConfigBaseDir)
	GlobalAuditCollector = collector

	base, logs := buildAuditSearchFixtures()
	for _, log := range logs {
		collector.realtime.Record(log)
	}
	if err := collector.storage.WriteBatch(logs); err != nil {
		t.Fatalf("collector.storage.WriteBatch() error = %v", err)
	}

	t.Cleanup(func() {
		GlobalAuditCollector = oldCollector
		MainConfigBaseDir = oldBaseDir
		collector.closeStorage()
	})

	router := chi.NewMux()
	RegisterAuditAPI(router)
	return router, collector, base
}

func postAuditJSON(t *testing.T, router *chi.Mux, path string, body any, wantStatus int, dst any) {
	t.Helper()
	reqBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertStatusCode(t, rec.Code, wantStatus)
	if dst == nil {
		return
	}
	decodeAuditResponse(t, rec, dst)
}
