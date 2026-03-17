package coremain

import "testing"

func TestClientnameCustomConfigRoundTrip(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveClientNamesToCustomConfig(map[string]string{
		"127.0.0.1":   "desktop",
		"192.168.1.2": "tv",
	}); err != nil {
		t.Fatalf("SaveClientNamesToCustomConfig: %v", err)
	}

	values, ok, err := LoadClientNamesFromCustomConfig()
	if err != nil {
		t.Fatalf("LoadClientNamesFromCustomConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected clientname file to exist")
	}
	if values["127.0.0.1"] != "desktop" || values["192.168.1.2"] != "tv" {
		t.Fatalf("unexpected clientname values: %+v", values)
	}
}
