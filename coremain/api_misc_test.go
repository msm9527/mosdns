package coremain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type mockReverseLookupController struct {
	result string
	err    error
}

type mockJSONStoreController struct {
	value any
	err   error
}

func (m *mockReverseLookupController) LookupIPString(_ string) (string, error) {
	return m.result, m.err
}

func (m *mockJSONStoreController) SnapshotJSONValue() any {
	return m.value
}

func (m *mockJSONStoreController) ReplaceJSONValue(_ context.Context, value any) error {
	if m.err != nil {
		return m.err
	}
	m.value = value
	return nil
}

func TestMiscAPI_ReverseLookup(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"reverse_lookup": &mockReverseLookupController{result: "example.org."},
	})
	router := chi.NewRouter()
	RegisterMiscAPI(router, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/reverse_lookup?ip=1.1.1.1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["domain"] != "example.org." {
		t.Fatalf("unexpected domain: %q", body["domain"])
	}
}

func TestMiscAPI_ReverseLookupError(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"reverse_lookup": &mockReverseLookupController{err: errors.New("bad ip")},
	})
	router := chi.NewRouter()
	RegisterMiscAPI(router, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/reverse_lookup?ip=x", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestClientnameAPI_FallsBackToSingleJSONStoreController(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"webinfo_client": &mockJSONStoreController{value: map[string]any{"client_name": "e2e"}},
	})
	router := chi.NewRouter()
	router.Get("/api/v1/control/clientname", handleGetClientname(m))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/clientname", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientnameAPI_RejectsAmbiguousJSONStoreControllers(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"webinfo_client": &mockJSONStoreController{value: map[string]any{"client_name": "a"}},
		"other_client":   &mockJSONStoreController{value: map[string]any{"client_name": "b"}},
	})
	router := chi.NewRouter()
	router.Get("/api/v1/control/clientname", handleGetClientname(m))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/clientname", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}
