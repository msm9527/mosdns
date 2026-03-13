package coremain

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockAdguardRuleController struct {
	items []AdguardRuleItem
}

func (m *mockAdguardRuleController) ListAdguardRules() ([]AdguardRuleItem, error) {
	return append([]AdguardRuleItem(nil), m.items...), nil
}

func (m *mockAdguardRuleController) CreateAdguardRule(item AdguardRuleItem) (AdguardRuleItem, error) {
	item.ID = "new-rule"
	m.items = append(m.items, item)
	return item, nil
}

func (m *mockAdguardRuleController) UpdateAdguardRule(id string, item AdguardRuleItem) (AdguardRuleItem, error) {
	item.ID = id
	return item, nil
}

func (m *mockAdguardRuleController) DeleteAdguardRule(id string) error {
	return nil
}

func (m *mockAdguardRuleController) TriggerAdguardUpdate() error {
	return nil
}

type mockDiversionRuleController struct {
	items []DiversionRuleItem
}

func (m *mockDiversionRuleController) ListDiversionRules() ([]DiversionRuleItem, error) {
	return append([]DiversionRuleItem(nil), m.items...), nil
}

func (m *mockDiversionRuleController) UpsertDiversionRule(name string, item DiversionRuleItem) (DiversionRuleItem, bool, error) {
	item.Name = name
	return item, true, nil
}

func (m *mockDiversionRuleController) DeleteDiversionRule(name string) error {
	return nil
}

func (m *mockDiversionRuleController) TriggerDiversionRuleUpdate(name string) error {
	return nil
}

func TestRulesAPI_ListAdguardRules(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"adguard": &mockAdguardRuleController{
			items: []AdguardRuleItem{{ID: "1", Name: "rule-a", URL: "https://example.com/a.txt"}},
		},
	})
	RegisterRulesAPI(m.httpMux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/rules/adguard", nil)
	w := httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	var items []AdguardRuleItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 || items[0].Name != "rule-a" {
		t.Fatalf("unexpected rules: %+v", items)
	}
}

func TestRulesAPI_UpsertDiversionRule(t *testing.T) {
	m := NewTestMosdnsWithPlugins(map[string]any{
		"geosite_cn": &mockDiversionRuleController{},
	})
	RegisterRulesAPI(m.httpMux, m)

	body := bytes.NewBufferString(`{"type":"geositecn","files":"a.srs","url":"https://example.com/a.srs","enabled":true}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/rules/diversion/geositecn/example", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.httpMux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	var item DiversionRuleItem
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if item.Name != "example" || item.Type != "geositecn" {
		t.Fatalf("unexpected diversion rule: %+v", item)
	}
}
