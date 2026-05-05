package coremain

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type testRuntimeReloader struct{}

func (t *testRuntimeReloader) ReloadControlConfig(_ *GlobalOverrides, _ []UpstreamOverrideConfig) error {
	return nil
}

type countingRuntimeReloader struct {
	count int
}

func (t *countingRuntimeReloader) ReloadControlConfig(_ *GlobalOverrides, _ []UpstreamOverrideConfig) error {
	t.count++
	return nil
}

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

	cfg, ok, err := loadUpstreamOverridesFromCustomConfig()
	if err != nil {
		t.Fatalf("loadUpstreamOverridesFromCustomConfig: %v", err)
	}
	if !ok || len(cfg["test_plugin"]) != 1 {
		t.Fatalf("expected upstream overrides in custom config, got %+v", cfg)
	}
}

func TestSetUpstreamOverridesBaseDirClearsLoadedOverridesOnDirChange(t *testing.T) {
	upstreamOverridesLock.Lock()
	oldOverrides := upstreamOverrides
	oldDir := upstreamOverridesDir
	upstreamOverrides = GlobalUpstreamOverrides{
		"foreign": {
			{Tag: "old", Enabled: true, Protocol: "udp", Addr: "udp://127.0.0.1:5353"},
		},
	}
	upstreamOverridesDir = "/tmp/mosdns-old-config"
	upstreamOverridesLock.Unlock()

	t.Cleanup(func() {
		upstreamOverridesLock.Lock()
		upstreamOverrides = oldOverrides
		upstreamOverridesDir = oldDir
		upstreamOverridesLock.Unlock()
	})

	setUpstreamOverridesBaseDir("/tmp/mosdns-new-config")

	upstreamOverridesLock.RLock()
	defer upstreamOverridesLock.RUnlock()
	if upstreamOverridesDir != "/tmp/mosdns-new-config" {
		t.Fatalf("upstreamOverridesDir = %q, want new dir", upstreamOverridesDir)
	}
	if upstreamOverrides != nil {
		t.Fatalf("upstreamOverrides should be cleared after base dir change, got %+v", upstreamOverrides)
	}
}

func TestHandleSetUpstreamConfigWithMosdns_NoDeadlockOnRuntimeApply(t *testing.T) {
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

	m := NewTestMosdnsWithPlugins(map[string]any{
		"test_plugin": &testRuntimeReloader{},
	})

	reqBody := `{"plugin_tag":"test_plugin","upstreams":[{"tag":"u1","enabled":false,"protocol":"udp"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/config", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handleSetUpstreamConfigWithMosdns(w, req, m)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleSetUpstreamConfigWithMosdns appears to deadlock during runtime apply")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "上游配置已保存并生效") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleSetUpstreamConfigWithMosdns_SkipsReloadWhenUnchanged(t *testing.T) {
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

	reloader := &countingRuntimeReloader{}
	m := NewTestMosdnsWithPlugins(map[string]any{
		"test_plugin": reloader,
	})

	reqBody := `{"plugin_tag":"test_plugin","upstreams":[{"tag":"u1","enabled":false,"protocol":"udp"}]}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/config", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handleSetUpstreamConfigWithMosdns(w, req, m)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d unexpected status code: got %d, body=%s", i+1, w.Code, w.Body.String())
		}
	}

	if reloader.count != 1 {
		t.Fatalf("reload count = %d, want 1", reloader.count)
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

func TestHandleReplaceUpstreamConfigWithMosdns_SaveAndApply(t *testing.T) {
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

	m := NewTestMosdnsWithPlugins(map[string]any{
		"test_plugin": &testRuntimeReloader{},
	})

	reqBody := `{
		"config": {
			"test_plugin": [
				{"tag":"u1","enabled":false,"protocol":"udp"}
			]
		},
		"apply": true
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/upstream/config", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleReplaceUpstreamConfigWithMosdns(w, req, m)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "上游配置已保存并生效") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}

	cfg, ok, err := loadUpstreamOverridesFromCustomConfig()
	if err != nil {
		t.Fatalf("loadUpstreamOverridesFromCustomConfig: %v", err)
	}
	items, found := cfg["test_plugin"]
	if !ok || !found || len(items) != 1 || items[0].Tag != "u1" {
		t.Fatalf("unexpected custom config content: %+v", cfg)
	}
}

func TestHandleUpstreamStatsResetRejectsUpstreamOnlyScope(t *testing.T) {
	router := chi.NewRouter()
	RegisterUpstreamAPI(router, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/stats/reset", strings.NewReader(`{"upstream_tag":"u1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"INVALID_UPSTREAM_STATS_RESET_SCOPE"`) {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
}

func TestHandleUpstreamItemCRUD(t *testing.T) {
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

	createBody := `{
		"plugin_tag": "test_plugin",
		"upstream": {"tag":"u1","enabled":false,"protocol":"udp"}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/items", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createW := httptest.NewRecorder()
	handleCreateUpstreamItemWithMosdns(createW, createReq, nil)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create failed: status=%d body=%s", createW.Code, createW.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/upstream/items?plugin_tag=test_plugin", nil)
	getW := httptest.NewRecorder()
	handleGetUpstreamItems(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get failed: status=%d body=%s", getW.Code, getW.Body.String())
	}
	var items []UpstreamOverrideConfig
	if err := json.Unmarshal(getW.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode get items failed: %v", err)
	}
	if len(items) != 1 || items[0].Tag != "u1" {
		t.Fatalf("unexpected get items: %+v", items)
	}

	updatePayload := map[string]any{
		"plugin_tag": "test_plugin",
		"upstream": map[string]any{
			"tag":      "u2",
			"enabled":  false,
			"protocol": "udp",
		},
	}
	updateBytes, _ := json.Marshal(updatePayload)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/upstream/items/u1", bytes.NewReader(updateBytes))
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq = withChiURLParam(updateReq, "upstreamTag", "u1")
	updateW := httptest.NewRecorder()
	handleUpdateUpstreamItemWithMosdns(updateW, updateReq, nil)
	if updateW.Code != http.StatusOK {
		t.Fatalf("update failed: status=%d body=%s", updateW.Code, updateW.Body.String())
	}

	getReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/upstream/items?plugin_tag=test_plugin", nil)
	getW2 := httptest.NewRecorder()
	handleGetUpstreamItems(getW2, getReq2)
	if err := json.Unmarshal(getW2.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode get items 2 failed: %v", err)
	}
	if len(items) != 1 || items[0].Tag != "u2" {
		t.Fatalf("unexpected items after update: %+v", items)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/upstream/items/u2?plugin_tag=test_plugin", nil)
	deleteReq = withChiURLParam(deleteReq, "upstreamTag", "u2")
	deleteW := httptest.NewRecorder()
	handleDeleteUpstreamItemWithMosdns(deleteW, deleteReq, nil)
	if deleteW.Code != http.StatusOK {
		t.Fatalf("delete failed: status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}

	getReq3 := httptest.NewRequest(http.MethodGet, "/api/v1/upstream/items?plugin_tag=test_plugin", nil)
	getW3 := httptest.NewRecorder()
	handleGetUpstreamItems(getW3, getReq3)
	if err := json.Unmarshal(getW3.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode get items 3 failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items, got %+v", items)
	}
}

func withChiURLParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}
