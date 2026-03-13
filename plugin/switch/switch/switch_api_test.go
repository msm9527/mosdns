package switcher

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/plugin/switch/switchmeta"
)

func TestCoreSwitchesAPI_GetAll(t *testing.T) {
	resetSwitchTestRegistry()

	dir := t.TempDir()
	def := switchmeta.MustLookup("core_mode")
	sw := &Switch{
		store: getStateStore(filepath.Join(dir, "switches.json")),
		def:   def,
	}
	if err := sw.load(); err != nil {
		t.Fatalf("load switch: %v", err)
	}
	globalRegistry.instances[def.Name] = sw

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	coreSwitchesAPI().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("unexpected content type: %s", ct)
	}

	var items []switchState
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected non-empty switches list")
	}

	found := false
	for _, item := range items {
		if item.Name == "core_mode" {
			found = true
			if item.Value != "secure" {
				t.Fatalf("unexpected core_mode value: %s", item.Value)
			}
		}
	}
	if !found {
		t.Fatalf("core_mode not found in response")
	}
}

func TestCoreSwitchesAPI_Update(t *testing.T) {
	resetSwitchTestRegistry()

	dir := t.TempDir()
	def := switchmeta.MustLookup("core_mode")
	sw := &Switch{
		store: getStateStore(filepath.Join(dir, "switches.json")),
		def:   def,
	}
	if err := sw.load(); err != nil {
		t.Fatalf("load switch: %v", err)
	}
	globalRegistry.instances[def.Name] = sw

	body := bytes.NewBufferString(`{"value":"compat"}`)
	req := httptest.NewRequest(http.MethodPut, "/core_mode", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	coreSwitchesAPI().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	var item switchState
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if item.Name != "core_mode" || item.Value != "compat" {
		t.Fatalf("unexpected response: %+v", item)
	}
	if got := sw.GetValue(); got != "compat" {
		t.Fatalf("switch value not updated: %s", got)
	}
}

func resetSwitchTestRegistry() {
	globalRegistry.Lock()
	defer globalRegistry.Unlock()
	globalRegistry.instances = make(map[string]*Switch)
	globalRegistry.apiOnce = sync.Once{}
}
