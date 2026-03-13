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

type mockJSONStoreController struct {
	value any
	err   error
}

func (m *mockJSONStoreController) SnapshotJSONValue() any { return m.value }
func (m *mockJSONStoreController) ReplaceJSONValue(_ context.Context, value any) error {
	if m.err != nil {
		return m.err
	}
	m.value = value
	return nil
}

type mockReverseLookupController struct {
	result string
	err    error
}

func (m *mockReverseLookupController) LookupIPString(_ string) (string, error) {
	return m.result, m.err
}

func TestMiscAPI_ClientnameGetPut(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"clientname": &mockJSONStoreController{value: map[string]any{"1.1.1.1": "router"}},
	})
	router := chi.NewRouter()
	RegisterMiscAPI(router, m)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/clientname", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("unexpected get status: %d", getRec.Code)
	}

	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/clientname", strings.NewReader(`{"8.8.8.8":"dns"}`))
	putRec := httptest.NewRecorder()
	router.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("unexpected put status: %d", putRec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(putRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got := body["8.8.8.8"]; got != "dns" {
		t.Fatalf("unexpected updated value: %q", got)
	}
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
