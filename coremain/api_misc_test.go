package coremain

import (
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

func (m *mockReverseLookupController) LookupIPString(_ string) (string, error) {
	return m.result, m.err
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
