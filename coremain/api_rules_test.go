package coremain

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func TestRulesAPI_CreateAndListAdguardRules(t *testing.T) {
	baseDir := t.TempDir()
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = baseDir
	t.Cleanup(func() { MainConfigBaseDir = oldBaseDir })

	mustWriteRuleTestFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: adguard
    type: adguard_rule
    args:
      config_file: custom_config/adguard_sources.yaml
`)
	mustWriteRuleTestFile(t, filepath.Join(baseDir, "custom_config", "adguard_sources.yaml"), `
# empty truth source, POST should still be able to create the first item
`)

	m := NewTestMosdnsWithPlugins(nil)
	RegisterRulesAPI(m.httpMux, m)

	body := bytes.NewBufferString(`{
		"id":"httpdns",
		"name":"HttpDNS",
		"enabled":true,
		"match_mode":"adguard_native",
		"format":"rules",
		"source_kind":"remote",
		"url":"https://example.com/httpdns.rules",
		"auto_update":true,
		"update_interval_hours":24
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rules/adguard", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/rules/adguard", nil)
	w = httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	var items []RuleSourceItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 || items[0].Path != "adguard/httpdns.rules" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestRulesAPI_CreateAndListDiversionRules(t *testing.T) {
	baseDir := t.TempDir()
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = baseDir
	t.Cleanup(func() { MainConfigBaseDir = oldBaseDir })

	mustWriteRuleTestFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: geosite_cn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: geosite_cn
`)

	m := NewTestMosdnsWithPlugins(nil)
	RegisterRulesAPI(m.httpMux, m)

	body := bytes.NewBufferString(`{
		"id":"geo-custom",
		"name":"Geo Custom",
		"bind_to":"geosite_cn",
		"enabled":true,
		"match_mode":"domain_set",
		"format":"list",
		"source_kind":"local",
		"path":"diversion/geo-custom.list"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rules/diversion", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}
	if err := SaveRuleSourceStatus(RuntimeStateDBPath(), RuleSourceStatus{
		Scope:     string(rulesource.ScopeDiversion),
		SourceID:  "geo-custom",
		RuleCount: 42,
	}); err != nil {
		t.Fatalf("SaveRuleSourceStatus: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/rules/diversion", nil)
	w = httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	var items []RuleSourceItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 || items[0].BindTo != "geosite_cn" || items[0].RuleCount != 42 {
		t.Fatalf("unexpected items: %+v", items)
	}
	if len(items[0].Bindings) != 1 || items[0].Bindings[0] != "geosite_cn" {
		t.Fatalf("unexpected bindings: %+v", items[0].Bindings)
	}
}

func TestRulesAPI_ListAdguardRulesFailsOnEmptyConfig(t *testing.T) {
	baseDir := t.TempDir()
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = baseDir
	t.Cleanup(func() { MainConfigBaseDir = oldBaseDir })

	mustWriteRuleTestFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: adguard
    type: adguard_rule
    args:
      config_file: custom_config/adguard_sources.yaml
`)
	mustWriteRuleTestFile(t, filepath.Join(baseDir, "custom_config", "adguard_sources.yaml"), `
# comment-only config should fail explicitly
`)

	m := NewTestMosdnsWithPlugins(nil)
	RegisterRulesAPI(m.httpMux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/rules/adguard", nil)
	w := httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}
	var resp APIErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Code != "RULE_SOURCE_CONFIG_EMPTY" {
		t.Fatalf("unexpected error response: %+v", resp)
	}
	if !strings.Contains(resp.Error, "custom_config/adguard_sources.yaml") {
		t.Fatalf("unexpected error message: %s", resp.Error)
	}
}

func mustWriteRuleTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
