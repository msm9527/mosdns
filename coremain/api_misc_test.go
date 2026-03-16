package coremain

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestClientnameAPI_ReadsAndWritesCustomConfig(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveClientNamesToCustomConfig(map[string]string{"127.0.0.1": "laptop"}); err != nil {
		t.Fatalf("SaveClientNamesToCustomConfig: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/api/v1/control/clientname", handleGetClientname(nil))
	router.Put("/api/v1/control/clientname", handlePutClientname(nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/clientname", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["127.0.0.1"] != "laptop" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/v1/control/clientname", strings.NewReader(`{"127.0.0.1":"desktop","192.168.1.2":"tv"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected put status: %d body=%s", rec.Code, rec.Body.String())
	}

	values, ok, err := LoadClientNamesFromCustomConfig()
	if err != nil {
		t.Fatalf("LoadClientNamesFromCustomConfig: %v", err)
	}
	if !ok || values["127.0.0.1"] != "desktop" || values["192.168.1.2"] != "tv" {
		t.Fatalf("unexpected stored client names: ok=%v values=%+v", ok, values)
	}
}
