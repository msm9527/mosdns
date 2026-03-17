package coremain

import (
	"fmt"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func LoadRuleSourceByID(configPath string, scope rulesource.Scope, sourceID string) (rulesource.Source, error) {
	cfg, _, err := rulesource.LoadConfig(ResolveMainConfigPath(configPath), scope)
	if err != nil {
		return rulesource.Source{}, err
	}
	for _, source := range cfg.Sources {
		if source.ID == sourceID {
			return source, nil
		}
	}
	return rulesource.Source{}, fmt.Errorf("source %q not found in %s", sourceID, configPath)
}

func LoadRuleSourcesByBinding(configPath string, scope rulesource.Scope, bindTo string) ([]rulesource.Source, error) {
	cfg, _, err := rulesource.LoadConfig(ResolveMainConfigPath(configPath), scope)
	if err != nil {
		return nil, err
	}
	sources := make([]rulesource.Source, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if source.BindTo == bindTo {
			sources = append(sources, source)
		}
	}
	return sources, nil
}
