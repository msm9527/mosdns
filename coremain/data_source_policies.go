package coremain

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"gopkg.in/yaml.v3"
)

const dataSourcePolicyConfigRelPath = "sub_config/20-data-sources.yaml"

type dataSourcePolicyFile struct {
	Policies []dataSourcePolicy `yaml:"policies"`
}

type dataSourcePolicy struct {
	Name string    `yaml:"name"`
	Type string    `yaml:"type"`
	Args yaml.Node `yaml:"args"`
}

type adguardPolicyArgs struct {
	Socks5     string `yaml:"socks5,omitempty"`
	ConfigFile string `yaml:"config_file"`
}

type diversionPolicyArgs struct {
	Socks5     string `yaml:"socks5,omitempty"`
	ConfigFile string `yaml:"config_file"`
	BindTo     string `yaml:"bind_to"`
}

func loadDataSourcePolicies(baseDir string) ([]dataSourcePolicy, error) {
	path := filepath.Join(baseDir, dataSourcePolicyConfigRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dataSourcePolicyConfigRelPath, err)
	}
	var cfg dataSourcePolicyFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", dataSourcePolicyConfigRelPath, err)
	}
	return cfg.Policies, nil
}

func listRuleSourceBindings(baseDir string, scope rulesource.Scope) (map[string][]string, error) {
	policies, err := loadDataSourcePolicies(baseDir)
	if err != nil {
		return nil, err
	}
	configPath := ruleSourceConfigPathForBaseDir(baseDir, scope)
	bindings := make(map[string][]string)
	for _, policy := range policies {
		if err := appendRuleSourceBinding(bindings, baseDir, configPath, scope, policy); err != nil {
			return nil, err
		}
	}
	for key := range bindings {
		sort.Strings(bindings[key])
	}
	return bindings, nil
}

func resolveRuleSourceSocks5(m *Mosdns, scope rulesource.Scope, bindTo string) (string, error) {
	baseDir := strings.TrimSpace(MainConfigBaseDir)
	if baseDir == "" {
		return "", nil
	}
	policies, err := loadDataSourcePolicies(baseDir)
	if err != nil {
		return "", err
	}
	for _, policy := range policies {
		socks5, ok, err := matchRuleSourceSocks5(m, baseDir, scope, bindTo, policy)
		if err != nil {
			return "", err
		}
		if ok {
			return socks5, nil
		}
	}
	if m == nil || m.GetGlobalOverrides() == nil {
		return "", nil
	}
	return strings.TrimSpace(m.GetGlobalOverrides().Socks5), nil
}

func appendRuleSourceBinding(
	bindings map[string][]string,
	baseDir string,
	configPath string,
	scope rulesource.Scope,
	policy dataSourcePolicy,
) error {
	switch scope {
	case rulesource.ScopeAdguard:
		if policy.Type != "adguard_rule" {
			return nil
		}
		var args adguardPolicyArgs
		if err := policy.Args.Decode(&args); err != nil {
			return fmt.Errorf("decode adguard_rule args %s: %w", policy.Name, err)
		}
		if resolvePolicyConfigPath(baseDir, args.ConfigFile) != configPath {
			return nil
		}
		bindings[""] = append(bindings[""], policy.Name)
	case rulesource.ScopeDiversion:
		if policy.Type != "sd_set" && policy.Type != "sd_set_light" && policy.Type != "si_set" {
			return nil
		}
		var args diversionPolicyArgs
		if err := policy.Args.Decode(&args); err != nil {
			return fmt.Errorf("decode diversion provider args %s: %w", policy.Name, err)
		}
		if resolvePolicyConfigPath(baseDir, args.ConfigFile) != configPath {
			return nil
		}
		bindings[strings.TrimSpace(args.BindTo)] = append(bindings[strings.TrimSpace(args.BindTo)], policy.Name)
	default:
		return fmt.Errorf("unsupported scope %q", scope)
	}
	return nil
}

func matchRuleSourceSocks5(
	m *Mosdns,
	baseDir string,
	scope rulesource.Scope,
	bindTo string,
	policy dataSourcePolicy,
) (string, bool, error) {
	configPath := ruleSourceConfigPathForBaseDir(baseDir, scope)
	global := (*GlobalOverrides)(nil)
	if m != nil {
		global = m.GetGlobalOverrides()
	}
	switch scope {
	case rulesource.ScopeAdguard:
		if policy.Type != "adguard_rule" {
			return "", false, nil
		}
		var args adguardPolicyArgs
		if err := policy.Args.Decode(&args); err != nil {
			return "", false, fmt.Errorf("decode adguard_rule args %s: %w", policy.Name, err)
		}
		if resolvePolicyConfigPath(baseDir, args.ConfigFile) != configPath {
			return "", false, nil
		}
		effective := new(adguardPolicyArgs)
		if err := DecodeRawArgsWithGlobalOverrides(policy.Name, args, effective, global); err != nil {
			return "", false, err
		}
		return strings.TrimSpace(effective.Socks5), true, nil
	case rulesource.ScopeDiversion:
		if policy.Type != "sd_set" && policy.Type != "sd_set_light" && policy.Type != "si_set" {
			return "", false, nil
		}
		var args diversionPolicyArgs
		if err := policy.Args.Decode(&args); err != nil {
			return "", false, fmt.Errorf("decode diversion provider args %s: %w", policy.Name, err)
		}
		if resolvePolicyConfigPath(baseDir, args.ConfigFile) != configPath || strings.TrimSpace(args.BindTo) != bindTo {
			return "", false, nil
		}
		effective := new(diversionPolicyArgs)
		if err := DecodeRawArgsWithGlobalOverrides(policy.Name, args, effective, global); err != nil {
			return "", false, err
		}
		return strings.TrimSpace(effective.Socks5), true, nil
	default:
		return "", false, fmt.Errorf("unsupported scope %q", scope)
	}
}

func ruleSourceConfigPathForBaseDir(baseDir string, scope rulesource.Scope) string {
	switch scope {
	case rulesource.ScopeAdguard:
		return filepath.Join(baseDir, "custom_config", adguardSourcesConfigFilename)
	case rulesource.ScopeDiversion:
		return filepath.Join(baseDir, "custom_config", diversionSourcesConfigFilename)
	default:
		return ""
	}
}

func resolvePolicyConfigPath(baseDir, configFile string) string {
	if filepath.IsAbs(configFile) {
		return filepath.Clean(configFile)
	}
	return filepath.Join(baseDir, filepath.FromSlash(strings.TrimSpace(configFile)))
}
