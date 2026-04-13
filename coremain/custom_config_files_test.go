package coremain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSwitchesCustomConfigRoundTrip(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := SaveSwitchesToCustomConfig(map[string]string{
		"branch_cache":      "off",
		"client_proxy_mode": "whitelist",
		"core_mode":         "compat",
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
	if values["branch_cache"] != "off" || values["client_proxy_mode"] != "whitelist" || values["core_mode"] != "compat" {
		t.Fatalf("unexpected persisted switch values: %+v", values)
	}
	if values["block_response"] != "on" {
		t.Fatalf("expected missing switches to fall back to defaults: %+v", values)
	}
	if values["fakeip_cache"] != "off" || values["probe_cache"] != "on" {
		t.Fatalf("expected new cache switches to use defaults: %+v", values)
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

func TestLoadSwitchesFromCustomConfigIgnoresUnknownSwitch(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	path := filepath.Join(MainConfigBaseDir, "custom_config", "switches.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	raw := []byte("branch_cache: off\nprefer_ipv6: on\nprefer_ipv4: off\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	values, ok, err := LoadSwitchesFromCustomConfig()
	if err != nil {
		t.Fatalf("LoadSwitchesFromCustomConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected switches config file to exist")
	}
	if values["branch_cache"] != "off" {
		t.Fatalf("expected branch_cache override to survive, got %+v", values)
	}
	if _, exists := values["prefer_ipv6"]; exists {
		t.Fatalf("expected deprecated prefer_ipv6 to be ignored, got %+v", values)
	}
	if _, exists := values["prefer_ipv4"]; exists {
		t.Fatalf("expected deprecated prefer_ipv4 to be ignored, got %+v", values)
	}
	if values["block_response"] != "on" {
		t.Fatalf("expected known defaults to remain available, got %+v", values)
	}
}

func TestWriteTextFileAtomicallySupportsConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "global_overrides.yaml")
	start := make(chan struct{})
	errCh := make(chan error, 16)
	var wg sync.WaitGroup

	for writer := 0; writer < 8; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			<-start
			for round := 0; round < 20; round++ {
				content := []byte(fmt.Sprintf("writer=%d round=%d\n", writer, round))
				if err := writeTextFileAtomically(path, content); err != nil {
					errCh <- err
					return
				}
			}
		}(writer)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !strings.HasPrefix(string(raw), "writer=") {
		t.Fatalf("unexpected final file content: %q", string(raw))
	}

	matches, err := filepath.Glob(filepath.Join(dir, "global_overrides.yaml.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temp files left, got %v", matches)
	}
}
