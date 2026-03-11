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

func TestHandleSelfRestart_RejectDuplicateSchedule(t *testing.T) {
	if !SelfRestartSupported() {
		t.Skip("self restart is unsupported on this platform")
	}

	m := NewTestMosdnsWithPlugins(map[string]any{})
	h := handleSelfRestart(m)

	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/system/restart", strings.NewReader(`{"delay_ms":86400000}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstW := httptest.NewRecorder()
	h.ServeHTTP(firstW, firstReq)
	if firstW.Code != http.StatusOK {
		t.Fatalf("unexpected first status code: got %d, body=%s", firstW.Code, firstW.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/system/restart", strings.NewReader(`{"delay_ms":86400000}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondW := httptest.NewRecorder()
	h.ServeHTTP(secondW, secondReq)
	if secondW.Code != http.StatusConflict {
		t.Fatalf("unexpected second status code: got %d, body=%s", secondW.Code, secondW.Body.String())
	}
	if !strings.Contains(secondW.Body.String(), `"code":"RESTART_ALREADY_SCHEDULED"`) {
		t.Fatalf("unexpected body: %s", secondW.Body.String())
	}
}
