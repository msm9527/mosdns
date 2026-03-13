package coremain

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteAPIError(t *testing.T) {
	w := httptest.NewRecorder()

	writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "invalid request body")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("missing code field: %s", body)
	}
	if !strings.Contains(body, `"error":"invalid request body"`) {
		t.Fatalf("missing error field: %s", body)
	}
}

func TestWriteAPINotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/not-found", nil)
	w := httptest.NewRecorder()

	writeAPINotFound(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"NOT_FOUND"`) {
		t.Fatalf("missing code field: %s", body)
	}
	if !strings.Contains(body, `"error":"api endpoint not found: GET /api/v1/not-found"`) {
		t.Fatalf("missing error field: %s", body)
	}
}

func TestInitHttpMux_InvalidRequestUsesJSON404(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{})
	m.initHttpMux()

	req := httptest.NewRequest(http.MethodGet, "/no-such-route", nil)
	w := httptest.NewRecorder()
	m.GetAPIRouter().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"NOT_FOUND"`) {
		t.Fatalf("missing code field: %s", body)
	}
	if !strings.Contains(body, `"error":"api endpoint not found: GET /no-such-route"`) {
		t.Fatalf("missing error field: %s", body)
	}
}

func TestInitHttpMux_MethodNotAllowedUsesJSON404(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{})
	m.initHttpMux()

	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	w := httptest.NewRecorder()
	m.GetAPIRouter().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"NOT_FOUND"`) {
		t.Fatalf("missing code field: %s", body)
	}
	if !strings.Contains(body, `"error":"api endpoint not found: POST /metrics"`) {
		t.Fatalf("missing error field: %s", body)
	}
}
