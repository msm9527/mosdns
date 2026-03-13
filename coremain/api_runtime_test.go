package coremain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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

	router := chi.NewRouter()
	RegisterRuntimeAPI(router)

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

	router := chi.NewRouter()
	RegisterRuntimeAPI(router)

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
