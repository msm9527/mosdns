package coremain

import (
	"strings"
	"testing"
)

type pluginArgsCapture struct {
	Value string `yaml:"value"`
}

func TestNewPluginRemovedTypeHint(t *testing.T) {
	m := NewTestMosdnsWithPlugins(make(map[string]any))
	err := m.newPlugin(PluginConfig{
		Tag:  "legacy",
		Type: "nft_add",
	}, PluginConfig{
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
	}, PluginConfig{
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

func TestNewPluginProvidesRawArgs(t *testing.T) {
	const pluginType = "test_plugin_raw_args_capture"
	type captured struct {
		effective string
		raw       string
	}

	t.Cleanup(func() {
		DelPluginType(pluginType)
	})

	var got captured
	RegNewPluginFunc(pluginType, func(bp *BP, args any) (any, error) {
		got.effective = args.(*pluginArgsCapture).Value
		if rawArgs, ok := bp.RawArgs().(*pluginArgsCapture); ok && rawArgs != nil {
			got.raw = rawArgs.Value
		}
		return struct{}{}, nil
	}, func() any { return new(pluginArgsCapture) })

	m := NewTestMosdnsWithPlugins(make(map[string]any))
	err := m.newPlugin(PluginConfig{
		Tag:  "capture",
		Type: pluginType,
		Args: map[string]any{"value": "raw"},
	}, PluginConfig{
		Tag:  "capture",
		Type: pluginType,
		Args: map[string]any{"value": "effective"},
	})
	if err != nil {
		t.Fatalf("newPlugin failed: %v", err)
	}
	if got.raw != "raw" {
		t.Fatalf("unexpected raw args value: %q", got.raw)
	}
	if got.effective != "effective" {
		t.Fatalf("unexpected effective args value: %q", got.effective)
	}
}
