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

func TestRuntimeStateStore_StructuredSwitchState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	switchFile := filepath.Join(t.TempDir(), "switches.json")

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceSwitch, switchFile, map[string]string{
		"core_mode":        "secure",
		"block_ipv6":       "off",
		"block_query_type": "on",
	}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath switch: %v", err)
	}

	var values map[string]string
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeNamespaceSwitch, switchFile, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath switch: %v", err)
	}
	if !ok {
		t.Fatalf("expected switch state to exist")
	}
	if values["core_mode"] != "secure" || values["block_ipv6"] != "off" || values["block_query_type"] != "on" {
		t.Fatalf("unexpected switch values: %+v", values)
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeNamespaceSwitch)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace switch: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != switchFile {
		t.Fatalf("unexpected switch entries: %+v", entries)
	}

	if err := DeleteRuntimeStateJSONFromPath(dbPath, runtimeNamespaceSwitch, switchFile); err != nil {
		t.Fatalf("DeleteRuntimeStateJSONFromPath switch: %v", err)
	}
	ok, err = LoadRuntimeStateJSONFromPath(dbPath, runtimeNamespaceSwitch, switchFile, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath switch after delete: %v", err)
	}
	if ok {
		t.Fatalf("expected switch state to be deleted")
	}
}

func TestRuntimeStateStore_StructuredOverridesState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	payload := GlobalOverrides{
		Socks5: "127.0.0.1:1080",
		ECS:    "1.1.1.1",
		Replacements: []*ReplacementRule{
			{Original: "udp://8.8.8.8", New: "udp://1.1.1.1", Comment: "test"},
		},
	}

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceOverrides, runtimeStateKeyGlobalOverrides, payload); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath overrides: %v", err)
	}

	var values GlobalOverrides
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeStateNamespaceOverrides, runtimeStateKeyGlobalOverrides, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath overrides: %v", err)
	}
	if !ok {
		t.Fatalf("expected overrides state to exist")
	}
	if values.Socks5 != payload.Socks5 || values.ECS != payload.ECS || len(values.Replacements) != 1 || values.Replacements[0].New != "udp://1.1.1.1" {
		t.Fatalf("unexpected overrides values: %+v", values)
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeStateNamespaceOverrides)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace overrides: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != runtimeStateKeyGlobalOverrides {
		t.Fatalf("unexpected overrides entries: %+v", entries)
	}
}

func TestRuntimeStateStore_StructuredUpstreamState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), runtimeStateDBFilename)
	payload := GlobalUpstreamOverrides{
		"test_plugin": {
			{Tag: "u1", Enabled: true, Protocol: "udp", Addr: "8.8.8.8"},
			{Tag: "u2", Enabled: false, Protocol: "doh", Addr: "https://dns.google/dns-query"},
		},
	}

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceUpstreams, runtimeStateKeyUpstreamConfig, payload); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath upstreams: %v", err)
	}

	var values GlobalUpstreamOverrides
	ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeStateNamespaceUpstreams, runtimeStateKeyUpstreamConfig, &values)
	if err != nil {
		t.Fatalf("LoadRuntimeStateJSONFromPath upstreams: %v", err)
	}
	if !ok {
		t.Fatalf("expected upstream state to exist")
	}
	if len(values["test_plugin"]) != 2 || values["test_plugin"][0].Tag != "u1" || values["test_plugin"][1].Tag != "u2" {
		t.Fatalf("unexpected upstream values: %+v", values)
	}

	entries, err := ListRuntimeStateByNamespace(dbPath, runtimeStateNamespaceUpstreams)
	if err != nil {
		t.Fatalf("ListRuntimeStateByNamespace upstreams: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != "test_plugin" {
		t.Fatalf("unexpected upstream entries: %+v", entries)
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
	configKey := filepath.Join(t.TempDir(), "webinfo", "requeryconfig.json") + ":config"
	stateKey := filepath.Join(t.TempDir(), "webinfo", "requeryconfig.state.json") + ":state"

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
