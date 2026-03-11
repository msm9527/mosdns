package coremain

import (
	"strings"
	"testing"
)

func TestNewPluginRemovedTypeHint(t *testing.T) {
	m := NewTestMosdnsWithPlugins(make(map[string]any))
	err := m.newPlugin(PluginConfig{
		Tag:  "legacy",
		Type: "nft_add",
	})
	if err == nil {
		t.Fatal("expected removed plugin type to return error")
	}
	if !strings.Contains(err.Error(), "removed") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), "docs/NFT_EBPF_REMOVAL.md") {
		t.Fatalf("expected migration guide hint in error message: %v", err)
	}
}

func TestNewPluginUnknownTypeKeepsGenericError(t *testing.T) {
	m := NewTestMosdnsWithPlugins(make(map[string]any))
	err := m.newPlugin(PluginConfig{
		Tag:  "unknown",
		Type: "totally_unknown_type",
	})
	if err == nil {
		t.Fatal("expected unknown plugin type to return error")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
