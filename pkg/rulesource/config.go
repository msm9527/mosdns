package rulesource

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadConfig(path string, scope Scope) (Config, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	if len(raw) == 0 {
		return Config{}, true, nil
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("decode config %s: %w", path, err)
	}
	cfg = NormalizeConfig(cfg)
	if err := ValidateConfig(scope, cfg); err != nil {
		return Config{}, false, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, true, nil
}

func MarshalConfig(cfg Config, scope Scope) ([]byte, error) {
	cfg = NormalizeConfig(cfg)
	if err := ValidateConfig(scope, cfg); err != nil {
		return nil, err
	}
	return yaml.Marshal(cfg)
}
