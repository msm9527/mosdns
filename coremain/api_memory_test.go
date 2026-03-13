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

type mockMemoryController struct {
	stats         DomainStatsSnapshot
	writeErr      error
	saveErr       error
	flushErr      error
	verifyErr     error
	lastQuery     string
	lastOffset    int
	lastLimit     int
	lastDomain    string
	lastVerified  string
	verifiedCount int
	items         []MemoryEntry
	total         int
}

func (m *mockMemoryController) SnapshotDomainStats() DomainStatsSnapshot { return m.stats }
func (m *mockMemoryController) MemoryEntries(query string, offset, limit int) ([]MemoryEntry, int, error) {
	m.lastQuery, m.lastOffset, m.lastLimit = query, offset, limit
	if m.writeErr != nil {
		return nil, 0, m.writeErr
	}
	if len(m.items) == 0 {
		m.items = []MemoryEntry{{Domain: "example.com", Count: 1, Date: "2026-03-13"}}
	}
	if m.total == 0 {
		m.total = len(m.items)
	}
	return m.items, m.total, nil
}
func (m *mockMemoryController) SaveToDisk(context.Context) error     { return m.saveErr }
func (m *mockMemoryController) FlushRuntime(context.Context) error   { return m.flushErr }
func (m *mockMemoryController) MarkDomainVerified(_ context.Context, domain, verifiedAt string) (int, error) {
	m.lastDomain, m.lastVerified = domain, verifiedAt
	if m.verifyErr != nil {
		return 0, m.verifyErr
	}
	if m.verifiedCount == 0 {
		m.verifiedCount = 1
	}
	return m.verifiedCount, nil
}

func TestMemoryAPI_EntriesAndStats(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": &mockMemoryController{stats: DomainStatsSnapshot{MemoryID: "my_fakeiplist", TotalEntries: 2}},
	})
	router := chi.NewRouter()
	RegisterMemoryAPI(router, m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/my_fakeiplist/stats", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/memory/my_fakeiplist/entries?q=exa&offset=3&limit=5", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("entries status = %d", rec.Code)
	}
	var body MemoryEntriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].Domain != "example.com" {
		t.Fatalf("unexpected body %#v", body)
	}
}

func TestMemoryAPI_SaveFlushVerify(t *testing.T) {
	controller := &mockMemoryController{}
	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": controller,
	})
	router := chi.NewRouter()
	RegisterMemoryAPI(router, m)

	for _, path := range []string{"/api/v1/memory/my_fakeiplist/save", "/api/v1/memory/my_fakeiplist/flush"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/my_fakeiplist/verify", strings.NewReader(`{"domain":"example.com"}`))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status = %d", rec.Code)
	}
}

func TestMemoryAPI_VerifyNotFound(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": &mockMemoryController{verifyErr: errors.New("domain not found")},
	})
	router := chi.NewRouter()
	RegisterMemoryAPI(router, m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/my_fakeiplist/verify", strings.NewReader(`{"domain":"example.com"}`))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("verify status = %d", rec.Code)
	}
}
