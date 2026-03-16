package coremain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeUpstreamHealthProvider struct{}

func (fakeUpstreamHealthProvider) SnapshotUpstreamHealth() []UpstreamHealthSnapshot {
	return []UpstreamHealthSnapshot{{
		PluginTag:           "fake",
		PluginType:          "forward",
		UpstreamTag:         "u1",
		Address:             "udp://1.1.1.1:53",
		Score:               123,
		AverageLatencyMs:    12.3,
		Inflight:            1,
		ConsecutiveFailures: 0,
		Healthy:             true,
	}}
}

func TestHandleRuntimeSummary(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveGeneratedDatasetToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), filepath.Join(MainConfigBaseDir, "gen", "realip.rule"), "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	var resp runtimeSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if resp.StorageEngine != "sqlite" {
		t.Fatalf("unexpected storage engine: %+v", resp)
	}
	if len(resp.Namespaces) == 0 {
		t.Fatalf("expected namespace summary, got %+v", resp)
	}
	if resp.Upstreams.Total != 1 || len(resp.Upstreams.Items) != 1 {
		t.Fatalf("expected upstream summary in response, got %+v", resp.Upstreams)
	}
}

func TestHandleRuntimeHealth(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	target := filepath.Join(MainConfigBaseDir, "gen", "realip.rule")
	if err := SaveGeneratedDatasetToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	var resp runtimeHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if resp.StorageEngine != "sqlite" || len(resp.Checks) == 0 {
		t.Fatalf("unexpected health payload: %+v", resp)
	}
	if len(resp.SuggestedActions) == 0 {
		t.Fatalf("expected suggested actions, got %+v", resp)
	}
}

func TestHandleControlUpstreamHealth(t *testing.T) {
	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/upstreams/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var resp upstreamHealthOverview
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode upstream health: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].UpstreamTag != "u1" {
		t.Fatalf("unexpected upstream health payload: %+v", resp)
	}
}

func TestHandleRuntimeDatasetsAndExport(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	target := filepath.Join(MainConfigBaseDir, "gen", "realip.rule")
	if err := SaveGeneratedDatasetToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/datasets", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected dataset status: %d body=%s", w.Code, w.Body.String())
	}
	var datasets []GeneratedDatasetEntry
	if err := json.Unmarshal(w.Body.Bytes(), &datasets); err != nil {
		t.Fatalf("decode datasets: %v", err)
	}
	if len(datasets) != 1 || datasets[0].Key != target {
		t.Fatalf("unexpected datasets: %+v", datasets)
	}
	if datasets[0].Version != 1 || datasets[0].ContentSHA256 == "" {
		t.Fatalf("expected dataset integrity metadata: %+v", datasets)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/control/datasets/verify", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected verify status: %d body=%s", w.Code, w.Body.String())
	}
	var verifySummary GeneratedDatasetVerifySummary
	if err := json.Unmarshal(w.Body.Bytes(), &verifySummary); err != nil {
		t.Fatalf("decode verify summary: %v", err)
	}
	if verifySummary.Checked != 1 || verifySummary.Missing != 1 {
		t.Fatalf("unexpected verify summary: %+v", verifySummary)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/control/datasets/export", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected export status: %d body=%s", w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if string(raw) != "full:example.com\n" {
		t.Fatalf("unexpected exported content: %q", string(raw))
	}
}

func TestHandleRuntimeEvents(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := RecordSystemEvent("control.test", "info", "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("RecordSystemEvent: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/events?component=control.test&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected events status: %d body=%s", w.Code, w.Body.String())
	}
	var events []SystemEventEntry
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) != 1 || events[0].Component != "control.test" || events[0].Message != "hello" {
		t.Fatalf("unexpected events payload: %+v", events)
	}
}

func TestRuntimeAliasesForOverridesAndUpstreams(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := saveGlobalOverridesToCustomConfig(&GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("saveGlobalOverridesToCustomConfig: %v", err)
	}
	if err := saveUpstreamOverridesToCustomConfig(GlobalUpstreamOverrides{
		"test": {{Tag: "u1", Protocol: "udp", Addr: "8.8.8.8"}},
	}); err != nil {
		t.Fatalf("saveUpstreamOverridesToCustomConfig: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/overrides", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected overrides status: %d body=%s", w.Code, w.Body.String())
	}
	var overrides GlobalOverridesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &overrides); err != nil {
		t.Fatalf("decode overrides: %v", err)
	}
	if overrides.Socks5 != "127.0.0.1:1080" {
		t.Fatalf("unexpected overrides payload: %+v", overrides)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/control/upstreams", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected upstreams status: %d body=%s", w.Code, w.Body.String())
	}
	var upstreams GlobalUpstreamOverrides
	if err := json.Unmarshal(w.Body.Bytes(), &upstreams); err != nil {
		t.Fatalf("decode upstreams: %v", err)
	}
	if len(upstreams["test"]) != 1 {
		t.Fatalf("unexpected upstreams payload: %+v", upstreams)
	}
}
