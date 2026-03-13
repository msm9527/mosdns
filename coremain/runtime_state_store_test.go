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
