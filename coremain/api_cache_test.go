package coremain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type mockCacheController struct {
	stats         CacheStatsSnapshot
	entries       []CacheEntry
	total         int
	saveErr       error
	flushErr      error
	purgeCount    int
	purgeErr      error
	purgeBatchErr error
	lastQuery     string
	lastOffset    int
	lastLimit     int
	lastQName     string
	lastQType     uint16
	lastDomains   []string
	lastQTypes    []uint16
	saveInvoked   bool
	flushInvoked  bool
}

func (m *mockCacheController) SnapshotCacheStats() CacheStatsSnapshot {
	return m.stats
}

func (m *mockCacheController) CacheEntries(query string, offset, limit int) ([]CacheEntry, int, error) {
	m.lastQuery = query
	m.lastOffset = offset
	m.lastLimit = limit
	return m.entries, m.total, nil
}

func (m *mockCacheController) SaveToDisk(ctx context.Context) error {
	m.saveInvoked = true
	return m.saveErr
}

func (m *mockCacheController) FlushRuntime(ctx context.Context) error {
	m.flushInvoked = true
	return m.flushErr
}

func (m *mockCacheController) PurgeDomainRuntime(ctx context.Context, qname string, qtype uint16) (int, error) {
	m.lastQName = qname
	m.lastQType = qtype
	return m.purgeCount, m.purgeErr
}

func (m *mockCacheController) RuntimeCacheKind() string {
	return "response"
}

func (m *mockCacheController) FlushRuntimeCache(ctx context.Context) error {
	return m.FlushRuntime(ctx)
}

func (m *mockCacheController) PurgeDomainsRuntimeCache(ctx context.Context, domains []string, qtypes []uint16) (int, error) {
	m.lastDomains = append([]string(nil), domains...)
	m.lastQTypes = append([]uint16(nil), qtypes...)
	if m.purgeBatchErr != nil {
		return 0, m.purgeBatchErr
	}
	return m.purgeCount, nil
}

func (m *mockCacheController) RuntimeCacheEntryCount() int {
	return m.total
}

func TestCacheAPI_GetEntries(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_cn": &mockCacheController{
				total: 1,
				entries: []CacheEntry{
					{Key: "example.com. A", DNSMessage: "dns message"},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cache/cache_cn/entries?q=example&offset=10&limit=20", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "cache_cn")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleCacheEntriesByTag(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	controller := m.plugins["cache_cn"].(*mockCacheController)
	if controller.lastQuery != "example" || controller.lastOffset != 10 || controller.lastLimit != 20 {
		t.Fatalf("unexpected query params %+v", controller)
	}
	var body CacheEntriesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Tag != "cache_cn" || body.Total != 1 || len(body.Items) != 1 || body.Items[0].Key != "example.com. A" {
		t.Fatalf("unexpected body %+v", body)
	}
}

func TestCacheAPI_Flush(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_all": &mockCacheController{},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/cache_all/flush", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "cache_all")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleCacheFlushByTag(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	if !m.plugins["cache_all"].(*mockCacheController).flushInvoked {
		t.Fatal("expected flush to be invoked")
	}
	if !strings.Contains(rr.Body.String(), "缓存已清空") {
		t.Fatalf("unexpected body %q", rr.Body.String())
	}
}

func TestCacheAPI_Save(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_all": &mockCacheController{},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/cache_all/save", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "cache_all")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleCacheSaveByTag(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	if !m.plugins["cache_all"].(*mockCacheController).saveInvoked {
		t.Fatal("expected save to be invoked")
	}
	if !strings.Contains(rr.Body.String(), "缓存已保存") {
		t.Fatalf("unexpected body %q", rr.Body.String())
	}
}

func TestCacheAPI_PurgeDomain(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_all": &mockCacheController{purgeCount: 3},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/cache_all/purge_domain", strings.NewReader(`{"qname":"example.com","qtype":1}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "cache_all")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleCachePurgeDomainByTag(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	controller := m.plugins["cache_all"].(*mockCacheController)
	if controller.lastQName != "example.com" || controller.lastQType != 1 {
		t.Fatalf("unexpected purge args %+v", controller)
	}
	if !strings.Contains(rr.Body.String(), `"purged":3`) {
		t.Fatalf("unexpected body %q", rr.Body.String())
	}
}

func TestCacheAPI_FlushError(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_all": &mockCacheController{flushErr: errors.New("boom")},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/cache_all/flush", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "cache_all")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleCacheFlushByTag(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
}

func TestCacheAPI_SaveError(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_all": &mockCacheController{saveErr: errors.New("boom")},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/cache_all/save", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "cache_all")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleCacheSaveByTag(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
}

func TestCacheAPI_PurgeDomains(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_main": &mockCacheController{purgeCount: 2},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge_domains", strings.NewReader(`{"tags":["cache_main"],"domains":["example.com","www.example.com"],"qtypes":[1,28]}`))
	rr := httptest.NewRecorder()

	handleCachePurgeDomains(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d: %s", rr.Code, rr.Body.String())
	}
	controller := m.plugins["cache_main"].(*mockCacheController)
	if got := strings.Join(controller.lastDomains, ","); got != "example.com,www.example.com" {
		t.Fatalf("unexpected domains %q", got)
	}
	if len(controller.lastQTypes) != 2 || controller.lastQTypes[0] != 1 || controller.lastQTypes[1] != 28 {
		t.Fatalf("unexpected qtypes %#v", controller.lastQTypes)
	}
}

func TestCacheAPI_FlushAllIncludesUDPFastWhenRequested(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_main": &mockCacheController{},
			"udp_all":    &mockUDPFastCacheController{},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/flush_all", strings.NewReader(`{"include_udp_fast":true}`))
	rr := httptest.NewRecorder()

	handleCacheFlushAll(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d: %s", rr.Code, rr.Body.String())
	}
	if !m.plugins["cache_main"].(*mockCacheController).flushInvoked {
		t.Fatal("expected response cache to be flushed")
	}
	if !m.plugins["udp_all"].(*mockUDPFastCacheController).flushInvoked {
		t.Fatal("expected udp fast cache to be flushed")
	}
}

func TestCacheAPI_FlushAllKindsCanTargetUDPFastOnly(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache_main": &mockCacheController{},
			"udp_all":    &mockUDPFastCacheController{},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/flush_all", strings.NewReader(`{"kinds":["udp_fast"],"include_udp_fast":true}`))
	rr := httptest.NewRecorder()

	handleCacheFlushAll(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d: %s", rr.Code, rr.Body.String())
	}
	if m.plugins["cache_main"].(*mockCacheController).flushInvoked {
		t.Fatal("expected response cache to be skipped")
	}
	if !m.plugins["udp_all"].(*mockUDPFastCacheController).flushInvoked {
		t.Fatal("expected udp fast cache to be flushed")
	}
}

type mockUDPFastCacheController struct {
	flushInvoked bool
	lastDomains  []string
	lastQTypes   []uint16
}

func (m *mockUDPFastCacheController) RuntimeCacheKind() string {
	return "udp_fast"
}

func (m *mockUDPFastCacheController) FlushRuntimeCache(context.Context) error {
	m.flushInvoked = true
	return nil
}

func (m *mockUDPFastCacheController) PurgeDomainsRuntimeCache(_ context.Context, domains []string, qtypes []uint16) (int, error) {
	m.lastDomains = append([]string(nil), domains...)
	m.lastQTypes = append([]uint16(nil), qtypes...)
	return len(domains), nil
}

func (m *mockUDPFastCacheController) RuntimeCacheEntryCount() int {
	return len(m.lastDomains)
}
