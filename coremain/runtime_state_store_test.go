package coremain

import (
	"path/filepath"
	"testing"
)

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
