package coremain

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// RuntimeConfigReloader can apply global/upstream overrides without process restart.
type RuntimeConfigReloader interface {
	ReloadRuntimeConfig(global *GlobalOverrides, upstreams []UpstreamOverrideConfig) error
}

// ReloadRuntimeConfig applies current runtime overrides to plugins that support hot reload.
// If targetPluginTag is non-empty, only that plugin is refreshed.
func (m *Mosdns) ReloadRuntimeConfig(targetPluginTag string) error {
	global := m.GetGlobalOverrides()
	var errs []string

	for tag, p := range m.plugins {
		if targetPluginTag != "" && tag != targetPluginTag {
			continue
		}

		reloader, ok := p.(RuntimeConfigReloader)
		if !ok {
			continue
		}

		upstreams := GetUpstreamOverrides(tag)
		if err := reloader.ReloadRuntimeConfig(global, upstreams); err != nil {
			m.logger.Error("runtime config reload failed",
				zap.String("plugin_tag", tag),
				zap.Error(err))
			errs = append(errs, fmt.Sprintf("%s: %v", tag, err))
			continue
		}

		m.logger.Info("runtime config reloaded",
			zap.String("plugin_tag", tag))
	}

	if len(errs) > 0 {
		return fmt.Errorf("runtime reload failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
