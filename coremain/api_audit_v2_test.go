package coremain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestHandleV2GetDomainSetRankNormalizes(t *testing.T) {
	oldCollector := GlobalAuditCollector
	t.Cleanup(func() {
		GlobalAuditCollector = oldCollector
	})

	collector := NewAuditCollector(AuditSettings{
		MemoryEntries: 8,
		RetentionDays: 7,
		MaxDiskSizeMB: 32,
	}, "")

	now := time.Now()
	logs := []AuditLog{
		{QueryName: "one.example", QueryType: "A", QueryTime: now, DomainSet: "记忆代理|记忆无V6|订阅代理"},
		{QueryName: "two.example", QueryType: "A", QueryTime: now, DomainSet: "记忆代理|订阅代理"},
		{QueryName: "three.example", QueryType: "AAAA", QueryTime: now, DomainSet: "记忆无V6|记忆直连|订阅直连"},
	}

	collector.mu.Lock()
	for _, log := range logs {
		collector.appendLogLocked(log)
	}
	collector.mu.Unlock()
	GlobalAuditCollector = collector

	router := chi.NewRouter()
	RegisterAuditAPIV2(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/audit/rank/domain_set?limit=10", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body []V2RankItem
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	got := make(map[string]int, len(body))
	for _, item := range body {
		got[item.Key] = item.Count
	}

	if got["记忆代理"] != 2 {
		t.Fatalf("记忆代理 count = %d, want 2", got["记忆代理"])
	}
	if got["记忆无V6"] != 1 {
		t.Fatalf("记忆无V6 count = %d, want 1", got["记忆无V6"])
	}
	if got["记忆代理|记忆无V6|订阅代理"] != 0 {
		t.Fatalf("unexpected raw combo remained in response: %#v", body)
	}
}
