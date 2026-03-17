package coremain

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

const (
	adguardSourcesConfigFilename   = "adguard_sources.yaml"
	diversionSourcesConfigFilename = "diversion_sources.yaml"
)

func AdguardSourcesConfigPath() string {
	return filepath.Join(customConfigDirPath(), adguardSourcesConfigFilename)
}

func DiversionSourcesConfigPath() string {
	return filepath.Join(customConfigDirPath(), diversionSourcesConfigFilename)
}

func LoadAdguardSourcesFromCustomConfig() (rulesource.Config, bool, error) {
	return rulesource.LoadConfig(AdguardSourcesConfigPath(), rulesource.ScopeAdguard)
}

func LoadDiversionSourcesFromCustomConfig() (rulesource.Config, bool, error) {
	return rulesource.LoadConfig(DiversionSourcesConfigPath(), rulesource.ScopeDiversion)
}

func SaveAdguardSourcesToCustomConfig(cfg rulesource.Config) error {
	body, err := renderRuleSourceConfig(rulesource.ScopeAdguard, cfg)
	if err != nil {
		return err
	}
	return writeTextFileAtomically(AdguardSourcesConfigPath(), body)
}

func SaveDiversionSourcesToCustomConfig(cfg rulesource.Config) error {
	body, err := renderRuleSourceConfig(rulesource.ScopeDiversion, cfg)
	if err != nil {
		return err
	}
	return writeTextFileAtomically(DiversionSourcesConfigPath(), body)
}

func renderRuleSourceConfig(scope rulesource.Scope, cfg rulesource.Config) ([]byte, error) {
	body, err := rulesource.MarshalConfig(cfg, scope)
	if err != nil {
		return nil, fmt.Errorf("marshal %s sources config: %w", scope, err)
	}
	var buf bytes.Buffer
	buf.WriteString(ruleSourceConfigHeader(scope))
	buf.Write(body)
	return buf.Bytes(), nil
}

func ruleSourceConfigHeader(scope rulesource.Scope) string {
	if scope == rulesource.ScopeAdguard {
		return "# 用户自定义广告拦截规则源\n#\n# 这个文件是广告拦截入口的长期配置真源。\n" +
			"# - 前端保存后会直接写回本文件，并立即热重载相关插件。\n" +
			"# - 你也可以手工修改本文件，然后重启 mosdns 生效。\n" +
			"# - 数据库只保存运行态元数据，不再把 rule_count / last_updated 回写到这里。\n\n"
	}
	return "# 用户自定义在线分流规则源\n#\n# 这个文件是在线分流入口的长期配置真源。\n" +
		"# - 前端保存后会直接写回本文件，并立即热重载相关 provider。\n" +
		"# - 你也可以手工修改本文件，然后重启 mosdns 生效。\n" +
		"# - 数据库只保存运行态元数据，不再把 rule_count / last_updated 回写到这里。\n\n"
}

func ResolveMainConfigPath(path string) string {
	if filepath.IsAbs(path) || strings.TrimSpace(MainConfigBaseDir) == "" {
		return path
	}
	return filepath.Join(MainConfigBaseDir, path)
}
