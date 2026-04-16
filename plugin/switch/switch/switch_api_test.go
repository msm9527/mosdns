package switcher

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/plugin/switch/switchmeta"
)

func TestCoreSwitchesAPI_GetAll(t *testing.T) {
	resetSwitchTestRegistry()

	dir := t.TempDir()
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})
	def := switchmeta.MustLookup("branch_cache")
	sw := &Switch{
		store: getStateStore(),
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
		if item.Name == "branch_cache" {
			found = true
			if item.Value != "on" {
				t.Fatalf("unexpected branch_cache value: %s", item.Value)
			}
		}
	}
	if !found {
		t.Fatalf("branch_cache not found in response")
	}
}

func TestCoreSwitchesAPI_Update(t *testing.T) {
	resetSwitchTestRegistry()

	dir := t.TempDir()
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})
	def := switchmeta.MustLookup("branch_cache")
	sw := &Switch{
		store: getStateStore(),
		def:   def,
	}
	if err := sw.load(); err != nil {
		t.Fatalf("load switch: %v", err)
	}
	globalRegistry.instances[def.Name] = sw

	body := bytes.NewBufferString(`{"value":"off"}`)
	req := httptest.NewRequest(http.MethodPut, "/branch_cache", body)
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
	if item.Name != "branch_cache" || item.Value != "off" {
		t.Fatalf("unexpected response: %+v", item)
	}
	if got := sw.GetValue(); got != "off" {
		t.Fatalf("switch value not updated: %s", got)
	}
	path := filepath.Join(dir, "custom_config", "switches.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected switches yaml to exist: %v", err)
	}
	if !bytes.Contains(raw, []byte("branch_cache: \"off\"")) {
		t.Fatalf("expected branch_cache to persist in switches yaml, got: %s", string(raw))
	}
}

func TestCoreSwitchesAPI_UpdateSameValueDoesNotRewriteFile(t *testing.T) {
	resetSwitchTestRegistry()

	dir := t.TempDir()
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = dir
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})
	def := switchmeta.MustLookup("branch_cache")
	sw := &Switch{
		store: getStateStore(),
		def:   def,
	}
	if err := sw.load(); err != nil {
		t.Fatalf("load switch: %v", err)
	}
	globalRegistry.instances[def.Name] = sw

	path := filepath.Join(dir, "custom_config", "switches.yaml")
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat initial switches yaml: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	body := bytes.NewBufferString(`{"value":"on"}`)
	req := httptest.NewRequest(http.MethodPut, "/branch_cache", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	coreSwitchesAPI().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body=%s", w.Code, w.Body.String())
	}

	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat switches yaml after no-op update: %v", err)
	}
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Fatalf("expected no-op switch update to preserve mod time, before=%v after=%v", infoBefore.ModTime(), infoAfter.ModTime())
	}
}

func resetSwitchTestRegistry() {
	globalRegistry.Lock()
	defer globalRegistry.Unlock()
	globalRegistry.instances = make(map[string]*Switch)
	globalRegistry.apiOnce = sync.Once{}
}
