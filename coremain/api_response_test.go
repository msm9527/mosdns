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
