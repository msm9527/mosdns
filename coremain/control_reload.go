package coremain

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ControlConfigReloader can apply global/upstream overrides without process restart.
type ControlConfigReloader interface {
	ReloadControlConfig(global *GlobalOverrides, upstreams []UpstreamOverrideConfig) error
}

type ruleSourceReloadModeKey struct{}

// RuleSourceReloadMode returns the rule-source sync behavior for the current
// runtime reload. The empty mode keeps the normal strict behavior.
func RuleSourceReloadMode(global *GlobalOverrides) string {
	if global == nil || global.Extra == nil {
		return ""
	}
	mode, _ := global.Extra[ruleSourceReloadModeKey{}].(string)
	return mode
}

// ReloadControlConfig applies current runtime overrides to plugins that support hot reload.
// If targetPluginTag is non-empty, only that plugin is refreshed.
func (m *Mosdns) ReloadControlConfig(targetPluginTag string) error {
	global := m.GetGlobalOverrides()
	return m.reloadControlConfigWithGlobal(targetPluginTag, global)
}

func (m *Mosdns) reloadControlConfigWithGlobal(targetPluginTag string, global *GlobalOverrides) error {
	var errs []string

	for tag, p := range m.plugins {
		if targetPluginTag != "" && tag != targetPluginTag {
			continue
		}

		reloader, ok := p.(ControlConfigReloader)
		if !ok {
			continue
		}

		upstreams := GetUpstreamOverrides(tag)
		if err := reloader.ReloadControlConfig(global, upstreams); err != nil {
			m.logger.Error("control config reload failed",
				zap.String("plugin_tag", tag),
				zap.Error(err))
			errs = append(errs, fmt.Sprintf("%s: %v", tag, err))
			continue
		}

		m.logger.Info("control config reloaded",
			zap.String("plugin_tag", tag))
	}

	if len(errs) > 0 {
		return fmt.Errorf("control reload failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ReloadRuleSourceDefinitions reloads rule-source definitions without forcing
// remote downloads for newly restored sources that have no local cache yet.
func (m *Mosdns) ReloadRuleSourceDefinitions(targetPluginTag string) error {
	global := m.GetGlobalOverrides()
	if global == nil {
		global = &GlobalOverrides{}
	}
	next := *global
	if global.Extra != nil {
		next.Extra = make(map[any]any, len(global.Extra)+1)
		for k, v := range global.Extra {
			next.Extra[k] = v
		}
	} else {
		next.Extra = make(map[any]any, 1)
	}
	next.Extra[ruleSourceReloadModeKey{}] = "definitions"
	return m.reloadControlConfigWithGlobal(targetPluginTag, &next)
}
