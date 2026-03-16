package coremain

import "testing"

func TestSwitchesCustomConfigRoundTrip(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveSwitchesToCustomConfig(map[string]string{
		"branch_cache":      "off",
		"client_proxy_mode": "whitelist",
	}); err != nil {
		t.Fatalf("SaveSwitchesToCustomConfig: %v", err)
	}

	values, ok, err := LoadSwitchesFromCustomConfig()
	if err != nil {
		t.Fatalf("LoadSwitchesFromCustomConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected switches config file to exist")
	}
	if values["branch_cache"] != "off" || values["client_proxy_mode"] != "whitelist" {
		t.Fatalf("unexpected persisted switch values: %+v", values)
	}
	if values["block_response"] != "on" {
		t.Fatalf("expected missing switches to fall back to defaults: %+v", values)
	}
}

func TestSaveSwitchesToCustomConfigRejectsUnknownSwitch(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveSwitchesToCustomConfig(map[string]string{"unknown_switch": "on"}); err == nil {
		t.Fatal("expected unknown switch to be rejected")
	}
}
