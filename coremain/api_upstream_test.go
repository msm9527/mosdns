package coremain

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleSetUpstreamConfig_NoDeadlockWhenOverridesNil(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	upstreamOverridesLock.Lock()
	oldOverrides := upstreamOverrides
	upstreamOverrides = nil
	upstreamOverridesLock.Unlock()

	MainConfigBaseDir = t.TempDir()

	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
		upstreamOverridesLock.Lock()
		upstreamOverrides = oldOverrides
		upstreamOverridesLock.Unlock()
	})

	reqBody := `{"plugin_tag":"test_plugin","upstreams":[{"tag":"u1","enabled":false,"protocol":"udp"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/config", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handleSetUpstreamConfig(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleSetUpstreamConfig appears to deadlock when upstreamOverrides is nil")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}

	path := filepath.Join(MainConfigBaseDir, upstreamOverridesFilename)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected upstream overrides file to be written: %v", err)
	}
}

func TestHandleSetUpstreamConfig_InvalidBody_JSONErrorResponse(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/config", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleSetUpstreamConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	if !strings.Contains(w.Body.String(), `"error":"Invalid request body"`) {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
}
