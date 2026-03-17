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
		return "# 用户自定义广告拦截规则源\n#\n" +
			"# 这是“广告拦截”入口的长期配置真源。\n" +
			"# - 前端规则管理页保存后会直接改这个文件，并立即热重载相关插件。\n" +
			"# - 你也可以手工修改这个文件，然后重启 mosdns 生效。\n" +
			"# - 数据库只保留运行态元数据，例如 rule_count / last_updated / last_error。\n" +
			"# - 原始规则文件保留在 config/adguard/ 目录；这里不再混入运行态统计字段。\n#\n" +
			"# 字段说明：\n" +
			"# - id: 规则源唯一标识，只允许使用字母、数字、下划线和中划线。\n" +
			"# - name: Web UI 显示名称。\n" +
			"# - enabled: 是否启用该规则源。\n" +
			"# - behavior: 广告入口允许 adguard / domain 两种语义。\n" +
			"# - match_mode: 广告入口允许 adguard_native / domain_set。\n" +
			"# - format: 原始文件格式，例如 rules / list / txt / json / yaml。\n" +
			"# - source_kind: local / remote。\n" +
			"# - path: 原始文件相对 config 根目录的路径，建议保留在 adguard/ 目录下。\n" +
			"# - url: 仅 remote 源需要填写。\n" +
			"# - auto_update: 仅 remote 源有效，表示是否允许后台自动更新。\n" +
			"# - update_interval_hours: 仅 remote 源有效，自动更新间隔（小时）。\n\n"
	}
	return "# 用户自定义在线分流规则源\n#\n" +
		"# 这是“在线分流”入口的长期配置真源。\n" +
		"# - 前端规则管理页保存后会直接改这个文件，并立即热重载相关 provider。\n" +
		"# - 你也可以手工修改这个文件，然后重启 mosdns 生效。\n" +
		"# - 数据库只保留运行态元数据，例如 rule_count / last_updated / last_error。\n" +
		"# - 原始规则文件建议保留在 config/diversion/ 目录；这里不再保存运行态统计。\n#\n" +
		"# 字段说明：\n" +
		"# - id: 规则源唯一标识，只用于识别单条 source。\n" +
		"# - name: Web UI 显示名称。\n" +
		"# - bind_to: 绑定到哪个系统分流入口；同一个 bind_to 下可以同时挂多个 source，运行时会自动聚合。\n" +
		"# - enabled: 是否启用该规则源。\n" +
		"# - behavior: 规则语义，允许 domain / ipcidr。\n" +
		"# - match_mode: 允许 domain_set / ip_cidr_set。\n" +
		"# - format: 原始文件格式，例如 list / txt / json / yaml / srs / mrs。\n" +
		"# - source_kind: local / remote。\n" +
		"# - path: 原始文件相对 config 根目录的路径，建议放在 diversion/ 目录下。\n" +
		"# - url: 仅 remote 源需要填写。\n" +
		"# - auto_update / update_interval_hours: 仅 remote 源有效。\n\n"
}

func ResolveMainConfigPath(path string) string {
	if filepath.IsAbs(path) || strings.TrimSpace(MainConfigBaseDir) == "" {
		return path
	}
	return filepath.Join(MainConfigBaseDir, path)
}
