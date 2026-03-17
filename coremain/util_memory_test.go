package coremain

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManualGCIsNoop(t *testing.T) {
	ManualGC()
}

func TestWithAsyncGCPassThrough(t *testing.T) {
	called := false
	handler := WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/memory", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("expected wrapped handler to be called")
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusNoContent)
	}
}
