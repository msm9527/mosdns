package coremain

import (
	"fmt"
	"os"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/internal/configv2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

func resolveConfigInput(filePath string) (*viper.Viper, []byte, string, error) {
	v := viper.New()

	if len(filePath) > 0 {
		v.SetConfigFile(filePath)
	} else {
		v.SetConfigName("config")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, nil, "", fmt.Errorf("failed to read config: %w", err)
	}

	fileUsed := v.ConfigFileUsed()
	raw, err := os.ReadFile(fileUsed)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to read resolved config file: %w", err)
	}
	return v, raw, fileUsed, nil
}

func isConfigV2Document(raw []byte) (bool, error) {
	var meta struct {
		Version any `yaml:"version"`
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return false, fmt.Errorf("parse config version: %w", err)
	}

	switch v := meta.Version.(type) {
	case nil:
		return false, nil
	case string:
		return configv2.IsV2Version(v), nil
	case int:
		return v == 2, nil
	case int64:
		return v == 2, nil
	case float64:
		return v == 2, nil
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		return configv2.IsV2Version(s), nil
	}
}

func compileConfigV2(raw []byte) (*Config, error) {
	cfgV2, err := configv2.Load(raw)
	if err != nil {
		return nil, err
	}
	compiled, err := configv2.Compile(cfgV2)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Log:     compiled.Log,
		Include: append([]string(nil), compiled.Include...),
		API: APIConfig{
			HTTP: compiled.API.HTTP,
		},
		ControlDBPath: strings.TrimSpace(cfgV2.Storage.ControlDB),
	}
	if audit := compileAuditSettings(cfgV2.Audit); audit != nil {
		cfg.Audit = audit
	}
	cfg.Plugins = make([]PluginConfig, 0, len(compiled.Plugins))
	for _, plugin := range compiled.Plugins {
		cfg.Plugins = append(cfg.Plugins, PluginConfig{
			Tag:  plugin.Tag,
			Type: plugin.Type,
			Args: plugin.Args,
		})
	}
	return cfg, nil
}

func compileAuditSettings(a configv2.AuditConfig) *AuditSettings {
	if a == (configv2.AuditConfig{}) {
		return nil
	}
	return &AuditSettings{
		Enabled:                    a.Enabled,
		OverviewWindowSeconds:      a.OverviewWindowSeconds,
		RawRetentionDays:           a.RawRetentionDays,
		AggregateRetentionDays:     a.AggregateRetentionDays,
		MaxStorageMB:               a.MaxStorageMB,
		SQLitePath:                 a.SQLitePath,
		FlushBatchSize:             a.FlushBatchSize,
		FlushIntervalMs:            a.FlushIntervalMs,
		MaintenanceIntervalSeconds: a.MaintenanceIntervalSeconds,
	}
}
