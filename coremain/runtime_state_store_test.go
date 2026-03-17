package coremain

import (
	"path/filepath"
	"testing"
)

func TestRuntimeStateDBPathForPath(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	refPath := filepath.Join(MainConfigBaseDir, "runtime", "clientname.json")
	got := RuntimeStateDBPathForPath(refPath)
	want := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	if got != want {
		t.Fatalf("unexpected runtime db path with main config base dir: got %q want %q", got, want)
	}
}

func TestRuntimeStateDBPathForPathWithoutMainConfigBaseDir(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = ""
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	refDir := t.TempDir()
	refPath := filepath.Join(refDir, "runtime", "clientname.json")
	got := RuntimeStateDBPathForPath(refPath)
	want := filepath.Join(filepath.Dir(refPath), runtimeStateDBFilename)
	if got != want {
		t.Fatalf("unexpected runtime db path fallback: got %q want %q", got, want)
	}
}

func TestRuntimeStateDBPathForBaseDirUsesOverride(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	oldOverride := runtimeStateDBPathOverride
	MainConfigBaseDir = t.TempDir()
	setRuntimeStateDBPath(filepath.Join(MainConfigBaseDir, "db", runtimeStateDBFilename))
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
		runtimeStateDBPathOverride = oldOverride
	})

	got := runtimeStateDBPathForBaseDir(MainConfigBaseDir)
	want := filepath.Join(MainConfigBaseDir, "db", runtimeStateDBFilename)
	if got != want {
		t.Fatalf("unexpected runtime db path override: got %q want %q", got, want)
	}
}

func TestRuntimeStateStore_StructuredWebinfoState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	configKey := filepath.Join(t.TempDir(), "webinfo", "clientname.json")
	payload := map[string]string{"1.1.1.1": "cloudflare"}

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceWebinfo, configKey, payload); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath webinfo: %v", err)
	}

	var values map[string]string
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeNamespaceWebinfo, configKey, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath webinfo: %v", err)
	}
	if !ok || values["1.1.1.1"] != "cloudflare" {
		t.Fatalf("unexpected webinfo values: ok=%v payload=%+v", ok, values)
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeNamespaceWebinfo)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace webinfo: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != configKey {
		t.Fatalf("unexpected webinfo entries: %+v", entries)
	}
}

func TestRuntimeStateStore_StructuredRequeryState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	configKey := "state/requery:config"
	stateKey := "state/requery:state"

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceRequery, configKey, map[string]any{
		"workflow": map[string]any{"mode": "hybrid"},
	}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath requery config: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceRequery, stateKey, map[string]any{
		"status": map[string]any{"task_state": "idle"},
	}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath requery state: %v", err)
	}

	var configPayload map[string]any
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeNamespaceRequery, configKey, &configPayload)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath requery config: %v", err)
	}
	if !ok {
		t.Fatalf("expected requery config to exist")
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeNamespaceRequery)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace requery: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected requery entries: %+v", entries)
	}
}

func TestRuntimeStateStore_StructuredAdguardState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	configKey := filepath.Join(t.TempDir(), "adguard", "config.json")
	payload := []map[string]any{
		{"id": "rule-1", "name": "gfw", "url": "https://example.com/gfw.txt", "enabled": true, "auto_update": true, "update_interval_hours": 24},
		{"id": "rule-2", "name": "privacy", "url": "https://example.com/privacy.txt", "enabled": false, "auto_update": false, "update_interval_hours": 48},
	}

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceAdguard, configKey, payload); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath adguard: %v", err)
	}

	var values []map[string]any
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeNamespaceAdguard, configKey, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath adguard: %v", err)
	}
	if !ok {
		t.Fatalf("expected adguard state to exist")
	}
	if len(values) != 2 || values[0]["id"] != "rule-1" || values[1]["id"] != "rule-2" {
		t.Fatalf("unexpected adguard values: %+v", values)
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeNamespaceAdguard)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace adguard: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != configKey {
		t.Fatalf("unexpected adguard entries: %+v", entries)
	}
}

func TestRuntimeStateStore_StructuredDiversionState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	configKey := filepath.Join(t.TempDir(), "rule", "rules.json")
	payload := []map[string]any{
		{"name": "geoipcn", "type": "geoipcn", "files": "geoipcn.srs", "url": "https://example.com/geoipcn.srs", "enabled": true, "auto_update": true, "update_interval_hours": 24},
		{"name": "geositecn", "type": "geositecn", "files": "geositecn.srs", "url": "https://example.com/geositecn.srs", "enabled": false, "auto_update": false, "update_interval_hours": 48},
	}

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceDiversion, configKey, payload); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath diversion: %v", err)
	}

	var values []map[string]any
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeNamespaceDiversion, configKey, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath diversion: %v", err)
	}
	if !ok {
		t.Fatalf("expected diversion state to exist")
	}
	if len(values) != 2 || values[0]["name"] != "geoipcn" || values[1]["name"] != "geositecn" {
		t.Fatalf("unexpected diversion values: %+v", values)
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeNamespaceDiversion)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace diversion: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != configKey {
		t.Fatalf("unexpected diversion entries: %+v", entries)
	}
}
