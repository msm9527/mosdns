package coremain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
	"github.com/go-chi/chi/v5"
)

func TestHandleRuntimeResources(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := saveGlobalOverridesToRuntimeStore(&GlobalOverrides{
		Socks5: "127.0.0.1:1080",
	}); err != nil {
		t.Fatalf("saveGlobalOverridesToRuntimeStore: %v", err)
	}
	if err := saveUpstreamOverridesToRuntimeStore(GlobalUpstreamOverrides{
		"test": {
			{Tag: "u1", Protocol: "udp", Addr: "8.8.8.8"},
		},
	}); err != nil {
		t.Fatalf("saveUpstreamOverridesToRuntimeStore: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), runtimeNamespaceSwitch, "switches.json", map[string]string{
		"core_mode": "secure",
	}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath switch: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), runtimeNamespaceWebinfo, "clientname.json", map[string]string{
		"1.1.1.1": "cloudflare",
	}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath webinfo: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), runtimeNamespaceRequery, "requery.json:config", map[string]any{
		"workflow": map[string]any{"mode": "hybrid"},
	}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath requery: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), runtimeNamespaceAdguard, "config.json", []map[string]any{{
		"id":   "rule-1",
		"name": "adguard-list",
	}}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath adguard: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), runtimeNamespaceDiversion, "rules.json", []map[string]any{{
		"name": "cuscn",
		"type": "cuscn",
	}}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath diversion: %v", err)
	}
	if err := SaveGeneratedDatasetToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), filepath.Join(MainConfigBaseDir, "gen", "realip.rule"), "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}
	if err := RecordSystemEvent("runtime.test", "info", "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("RecordSystemEvent: %v", err)
	}
	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	if err := requeryruntime.ReplaceJobs(dbPath, "cfg-a", []requeryruntime.Job{{
		JobID:         "cfg-a/full_rebuild/manual",
		ConfigKey:     "cfg-a",
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		Enabled:       true,
		Definition:    json.RawMessage(`{"limit":0}`),
	}}); err != nil {
		t.Fatalf("ReplaceJobs: %v", err)
	}
	if err := requeryruntime.SaveRun(dbPath, requeryruntime.Run{
		RunID:         "run-1",
		ConfigKey:     "cfg-a",
		JobID:         "cfg-a/full_rebuild/manual",
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		State:         "completed",
		Total:         10,
		Completed:     10,
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/resources", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	var resp runtimeResourcesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Overrides == nil || resp.Overrides.Socks5 != "127.0.0.1:1080" {
		t.Fatalf("unexpected overrides: %+v", resp.Overrides)
	}
	if len(resp.Upstreams["test"]) != 1 {
		t.Fatalf("unexpected upstreams: %+v", resp.Upstreams)
	}
	if resp.Switches["core_mode"] != "secure" {
		t.Fatalf("unexpected switches: %+v", resp.Switches)
	}
	if _, ok := resp.Webinfo["clientname.json"]; !ok {
		t.Fatalf("missing webinfo payload: %+v", resp.Webinfo)
	}
	if _, ok := resp.Requery["requery.json:config"]; !ok {
		t.Fatalf("missing requery payload: %+v", resp.Requery)
	}
	if _, ok := resp.Adguard["config.json"]; !ok {
		t.Fatalf("missing adguard payload: %+v", resp.Adguard)
	}
	if _, ok := resp.Diversion["rules.json"]; !ok {
		t.Fatalf("missing diversion payload: %+v", resp.Diversion)
	}
	if len(resp.Datasets) != 1 || resp.Datasets[0].Format != "domain_output_rule" {
		t.Fatalf("unexpected datasets payload: %+v", resp.Datasets)
	}
	foundRuntimeTest := false
	for _, event := range resp.Events {
		if event.Component == "runtime.test" {
			foundRuntimeTest = true
			break
		}
	}
	if !foundRuntimeTest {
		t.Fatalf("expected runtime.test event in payload: %+v", resp.Events)
	}
	if len(resp.RequeryJobs) != 1 || resp.RequeryJobs[0].Mode != "full_rebuild" {
		t.Fatalf("unexpected requery jobs payload: %+v", resp.RequeryJobs)
	}
	if len(resp.RequeryRuns) != 1 || resp.RequeryRuns[0].RunID != "run-1" {
		t.Fatalf("unexpected requery runs payload: %+v", resp.RequeryRuns)
	}
}

func TestHandleRuntimeSummary(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveRuntimeStateJSON("switch", "switches.json", map[string]string{"core_mode": "compat"}); err != nil {
		t.Fatalf("SaveRuntimeStateJSON: %v", err)
	}
	if err := SaveGeneratedDatasetToPath(filepath.Join(MainConfigBaseDir, runtimeStateDBFilename), filepath.Join(MainConfigBaseDir, "gen", "realip.rule"), "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/summary", nil)
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
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/health", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/datasets", nil)
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

	req = httptest.NewRequest(http.MethodPost, "/api/v1/runtime/datasets/verify", nil)
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

	req = httptest.NewRequest(http.MethodPost, "/api/v1/runtime/datasets/export", nil)
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

	if err := RecordSystemEvent("runtime.test", "info", "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("RecordSystemEvent: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?component=runtime.test&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected events status: %d body=%s", w.Code, w.Body.String())
	}
	var events []SystemEventEntry
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) != 1 || events[0].Component != "runtime.test" || events[0].Message != "hello" {
		t.Fatalf("unexpected events payload: %+v", events)
	}
}

func TestRuntimeAliasesForOverridesAndUpstreams(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := saveGlobalOverridesToRuntimeStore(&GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("saveGlobalOverridesToRuntimeStore: %v", err)
	}
	if err := saveUpstreamOverridesToRuntimeStore(GlobalUpstreamOverrides{
		"test": {{Tag: "u1", Protocol: "udp", Addr: "8.8.8.8"}},
	}); err != nil {
		t.Fatalf("saveUpstreamOverridesToRuntimeStore: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/overrides", nil)
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/runtime/upstreams", nil)
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
