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

// ReloadControlConfig applies current runtime overrides to plugins that support hot reload.
// If targetPluginTag is non-empty, only that plugin is refreshed.
func (m *Mosdns) ReloadControlConfig(targetPluginTag string) error {
	global := m.GetGlobalOverrides()
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
