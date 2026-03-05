package coremain

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleSelfRestart_InvalidJSON(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{})
	h := handleSelfRestart(m)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/restart", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid request body") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleSelfRestart_NilMosdns(t *testing.T) {
	h := handleSelfRestart(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/restart", strings.NewReader(`{"delay_ms":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "service unavailable") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"SERVICE_UNAVAILABLE"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}
