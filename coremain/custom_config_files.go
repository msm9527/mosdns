package coremain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/plugin/switch/switchmeta"
	"gopkg.in/yaml.v3"
)

const (
	customConfigDirname             = "custom_config"
	globalOverridesConfigFilename   = "global_overrides.yaml"
	upstreamOverridesConfigFilename = "upstreams.yaml"
	switchesConfigFilename          = "switches.yaml"
	clientNameConfigFilename        = "clientname.yaml"
)

type globalOverridesFile struct {
	Socks5       string             `yaml:"socks5"`
	ECS          string             `yaml:"ecs"`
	Replacements []*ReplacementRule `yaml:"replacements"`
}

func customConfigDirPath() string {
	baseDir := MainConfigBaseDir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	return customConfigDirPathForBaseDir(baseDir)
}

func customConfigDirPathForBaseDir(baseDir string) string {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, customConfigDirname)
}

func globalOverridesConfigPath() string {
	return filepath.Join(customConfigDirPath(), globalOverridesConfigFilename)
}

func GlobalOverridesConfigPathForBaseDir(baseDir string) string {
	return globalOverridesConfigPathForBaseDir(baseDir)
}

func globalOverridesConfigPathForBaseDir(baseDir string) string {
	return filepath.Join(customConfigDirPathForBaseDir(baseDir), globalOverridesConfigFilename)
}

func upstreamOverridesConfigPath() string {
	return filepath.Join(customConfigDirPath(), upstreamOverridesConfigFilename)
}

func upstreamOverridesConfigPathForBaseDir(baseDir string) string {
	return filepath.Join(customConfigDirPathForBaseDir(baseDir), upstreamOverridesConfigFilename)
}

func switchesConfigPath() string {
	return filepath.Join(customConfigDirPath(), switchesConfigFilename)
}

func switchesConfigPathForBaseDir(baseDir string) string {
	return filepath.Join(customConfigDirPathForBaseDir(baseDir), switchesConfigFilename)
}

func SwitchesConfigPath() string {
	return switchesConfigPath()
}

func clientNameConfigPath() string {
	return filepath.Join(customConfigDirPath(), clientNameConfigFilename)
}

func ClientNameConfigPath() string {
	return clientNameConfigPath()
}

func loadGlobalOverridesFromCustomConfig() (*GlobalOverrides, bool, error) {
	return loadGlobalOverridesFromCustomConfigAtPath(globalOverridesConfigPath())
}

func loadGlobalOverridesFromCustomConfigForBaseDir(baseDir string) (*GlobalOverrides, bool, error) {
	return loadGlobalOverridesFromCustomConfigAtPath(globalOverridesConfigPathForBaseDir(baseDir))
}

func loadGlobalOverridesFromCustomConfigAtPath(path string) (*GlobalOverrides, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read global overrides file: %w", err)
	}

	var fileCfg globalOverridesFile
	if err := yaml.Unmarshal(raw, &fileCfg); err != nil {
		return nil, false, fmt.Errorf("decode global overrides file %s: %w", path, err)
	}

	payload := &GlobalOverrides{
		Socks5:       strings.TrimSpace(fileCfg.Socks5),
		ECS:          strings.TrimSpace(fileCfg.ECS),
		Replacements: fileCfg.Replacements,
	}
	payload.Prepare()
	return payload, true, nil
}

func saveGlobalOverridesToCustomConfig(payload *GlobalOverrides) error {
	return saveGlobalOverridesToCustomConfigAtPath(globalOverridesConfigPath(), payload)
}

func saveGlobalOverridesToCustomConfigForBaseDir(baseDir string, payload *GlobalOverrides) error {
	return saveGlobalOverridesToCustomConfigAtPath(globalOverridesConfigPathForBaseDir(baseDir), payload)
}

func saveGlobalOverridesToCustomConfigAtPath(path string, payload *GlobalOverrides) error {
	if payload == nil {
		payload = &GlobalOverrides{}
	}

	fileCfg := globalOverridesFile{
		Socks5:       strings.TrimSpace(payload.Socks5),
		ECS:          strings.TrimSpace(payload.ECS),
		Replacements: payload.Replacements,
	}
	if fileCfg.Replacements == nil {
		fileCfg.Replacements = make([]*ReplacementRule, 0)
	}

	body, err := yaml.Marshal(fileCfg)
	if err != nil {
		return fmt.Errorf("encode global overrides yaml: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("# 用户自定义全局覆盖配置\n")
	buf.WriteString("#\n")
	buf.WriteString("# 这个文件是 socks5 / ecs / 文本替换规则 的长期配置真源。\n")
	buf.WriteString("# - 前端保存会直接改这个文件，并在运行中热重载。\n")
	buf.WriteString("# - 你也可以手工修改这个文件，然后重启 mosdns 生效。\n")
	buf.WriteString("# - 这里不再走 control.db，数据库只负责运行态和生成态数据。\n")
	buf.WriteString("#\n")
	buf.WriteString("# 字段说明：\n")
	buf.WriteString("# - socks5: 给规则下载、更新等非 DNS 上游请求提供统一代理地址，例如 127.0.0.1:7891\n")
	buf.WriteString("# - ecs: 用于替换配置里 `ecs x.x.x.x` / `ecs 2408:...` 这类 ECS 指定值\n")
	buf.WriteString("# - replacements: 可选的字符串替换表，适合少量精确替换\n\n")
	buf.Write(body)

	return writeTextFileAtomically(path, buf.Bytes())
}

func loadUpstreamOverridesFromCustomConfig() (GlobalUpstreamOverrides, bool, error) {
	return loadUpstreamOverridesFromCustomConfigAtPath(upstreamOverridesConfigPath())
}

func loadUpstreamOverridesFromCustomConfigForBaseDir(baseDir string) (GlobalUpstreamOverrides, bool, error) {
	return loadUpstreamOverridesFromCustomConfigAtPath(upstreamOverridesConfigPathForBaseDir(baseDir))
}

func loadUpstreamOverridesFromCustomConfigAtPath(path string) (GlobalUpstreamOverrides, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read upstream overrides file: %w", err)
	}

	var cfg GlobalUpstreamOverrides
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, false, fmt.Errorf("decode upstream overrides file %s: %w", path, err)
	}
	if cfg == nil {
		cfg = make(GlobalUpstreamOverrides)
	}
	return cfg, true, nil
}

func loadSwitchesFromCustomConfig() (map[string]string, bool, error) {
	return loadSwitchesFromCustomConfigAtPath(switchesConfigPath())
}

func LoadSwitchesFromCustomConfig() (map[string]string, bool, error) {
	return loadSwitchesFromCustomConfig()
}

func loadSwitchesFromCustomConfigForBaseDir(baseDir string) (map[string]string, bool, error) {
	return loadSwitchesFromCustomConfigAtPath(switchesConfigPathForBaseDir(baseDir))
}

func loadSwitchesFromCustomConfigAtPath(path string) (map[string]string, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultSwitchValueMap(), false, nil
		}
		return nil, false, fmt.Errorf("read switches config file: %w", err)
	}

	var cfg map[string]string
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, false, fmt.Errorf("decode switches config file %s: %w", path, err)
	}
	normalized, err := normalizeSwitchValueMap(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("normalize switches config file %s: %w", path, err)
	}
	return normalized, true, nil
}

func saveSwitchesToCustomConfig(values map[string]string) error {
	normalized, err := normalizeSwitchValueMap(values)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	buf.WriteString("# 用户自定义功能开关配置\n")
	buf.WriteString("#\n")
	buf.WriteString("# 这个文件是全部功能开关的长期配置真源。\n")
	buf.WriteString("# - 前端功能开关 API 的修改会直接写入这里，并立即同步到运行中的开关实例。\n")
	buf.WriteString("# - 你也可以手工修改这个文件，然后重启 mosdns 生效。\n")
	buf.WriteString("# - 这里不再走 control.db；数据库只保留运行态和生成态数据。\n")
	buf.WriteString("#\n")
	buf.WriteString("# 字段说明：\n")
	buf.WriteString("# - block_response: on/off，是否拦截黑名单和空响应结果。\n")
	buf.WriteString("# - client_proxy_mode: all/blacklist/whitelist，控制哪些客户端允许走代理链路。\n")
	buf.WriteString("# - main_cache: on/off，控制真实解析主缓存总开关。\n")
	buf.WriteString("# - branch_cache: on/off，控制真实解析分支缓存（国内/国外/ECS）。\n")
	buf.WriteString("# - fakeip_cache: on/off，控制 FakeIP DNS 响应缓存，不影响系统记录 FakeIP 路径域名的运行记忆列表。\n")
	buf.WriteString("# - probe_cache: on/off，控制节点探测专用缓存。\n")
	buf.WriteString("# - block_query_type: on/off，控制 SOA/PTR/HTTPS 等类型屏蔽。\n")
	buf.WriteString("# - block_ipv6: on/off，控制 AAAA 请求屏蔽。\n")
	buf.WriteString("# - ad_block: on/off，控制 AdGuard 在线规则拦截。\n")
	buf.WriteString("# - cn_answer_mode: realip/fakeip，控制国内域名返回真实 IP 还是 FakeIP。\n")
	buf.WriteString("# - udp_fast_path: on/off，控制 UDP 极限缓存快路径。\n\n")

	for i, def := range switchmeta.Ordered() {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(def.Name)
		buf.WriteString(": ")
		buf.WriteString(strconvQuote(normalized[def.Name]))
		buf.WriteByte('\n')
	}

	return writeTextFileAtomically(switchesConfigPath(), buf.Bytes())
}

func SaveSwitchesToCustomConfig(values map[string]string) error {
	return saveSwitchesToCustomConfig(values)
}

func LoadClientNamesFromCustomConfig() (map[string]string, bool, error) {
	path := clientNameConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), false, nil
		}
		return nil, false, fmt.Errorf("read clientname config file: %w", err)
	}

	var values map[string]string
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return nil, false, fmt.Errorf("decode clientname config file %s: %w", path, err)
	}
	if values == nil {
		values = make(map[string]string)
	}
	return values, true, nil
}

func SaveClientNamesToCustomConfig(values map[string]string) error {
	if values == nil {
		values = make(map[string]string)
	}
	path := clientNameConfigPath()

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteString("# 用户自定义客户端别名配置\n")
	buf.WriteString("#\n")
	buf.WriteString("# 这个文件是客户端 IP -> 显示别名 的长期配置真源。\n")
	buf.WriteString("# - 前端“客户端别名”保存会直接改这个文件，并立即生效。\n")
	buf.WriteString("# - 你也可以手工修改这个文件，然后重启 mosdns 生效。\n")
	buf.WriteString("# - 这里不再走 control.db；数据库只保留运行态和生成态数据。\n")
	buf.WriteString("#\n")
	buf.WriteString("# 格式说明：\n")
	buf.WriteString("# - key: 客户端 IP，支持 IPv4/IPv6\n")
	buf.WriteString("# - value: 展示名称，例如 手机 / 客厅电视 / NAS\n\n")
	if len(keys) == 0 {
		buf.WriteString("{}\n")
		return writeTextFileAtomically(path, buf.Bytes())
	}
	for _, key := range keys {
		buf.WriteString(strconvQuote(key))
		buf.WriteString(": ")
		buf.WriteString(strconvQuote(values[key]))
		buf.WriteByte('\n')
	}
	return writeTextFileAtomically(path, buf.Bytes())
}

func defaultSwitchValueMap() map[string]string {
	values := make(map[string]string, len(switchmeta.Ordered()))
	for _, def := range switchmeta.Ordered() {
		values[def.Name] = def.DefaultValue
	}
	return values
}

func normalizeSwitchValueMap(values map[string]string) (map[string]string, error) {
	normalized := defaultSwitchValueMap()
	if values == nil {
		return normalized, nil
	}
	for name, value := range values {
		def, ok := switchmeta.Lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown switch %q", name)
		}
		next, err := def.NormalizeValue(value)
		if err != nil {
			return nil, err
		}
		normalized[name] = next
	}
	return normalized, nil
}

func strconvQuote(value string) string {
	var buf bytes.Buffer
	buf.WriteByte('"')
	for _, r := range value {
		if r == '"' || r == '\\' {
			buf.WriteByte('\\')
		}
		buf.WriteRune(r)
	}
	buf.WriteByte('"')
	return buf.String()
}

func saveUpstreamOverridesToCustomConfig(cfg GlobalUpstreamOverrides) error {
	return saveUpstreamOverridesToCustomConfigAtPath(upstreamOverridesConfigPath(), cfg)
}

func saveUpstreamOverridesToCustomConfigForBaseDir(baseDir string, cfg GlobalUpstreamOverrides) error {
	return saveUpstreamOverridesToCustomConfigAtPath(upstreamOverridesConfigPathForBaseDir(baseDir), cfg)
}

func saveUpstreamOverridesToCustomConfigAtPath(path string, cfg GlobalUpstreamOverrides) error {
	if cfg == nil {
		cfg = make(GlobalUpstreamOverrides)
	}

	var buf bytes.Buffer
	buf.WriteString("# 用户自定义上游配置\n")
	buf.WriteString("#\n")
	buf.WriteString("# 这个文件是各个 aliapi 上游组的长期配置真源。\n")
	buf.WriteString("# - key 必须是插件 tag，例如 domestic / foreign / foreignecs / cnfake / nocnfake\n")
	buf.WriteString("# - value 是该插件实际使用的上游列表\n")
	buf.WriteString("# - 前端保存会直接改这个文件，并在运行中热重载\n")
	buf.WriteString("# - 你也可以手工修改这个文件，然后重启 mosdns 生效\n")
	buf.WriteString("#\n")
	buf.WriteString("# 字段说明：\n")
	buf.WriteString("# - tag: 上游名称，只要求在同一组内唯一\n")
	buf.WriteString("# - enabled: 是否启用；false 表示保留但不参与运行\n")
	buf.WriteString("# - protocol: aliapi / udp / tcp / tls / https 等\n")
	buf.WriteString("# - addr: 普通 DNS 上游地址，例如 udp://1.1.1.1:53 或 https://dns.google/dns-query\n")
	buf.WriteString("# - dial_addr: DoH/DoT 可选拨号地址，用来避免额外解析\n")
	buf.WriteString("# - socks5: 只给这一条上游单独指定代理；不填则按插件/全局配置处理\n")
	buf.WriteString("# - ecs_client_ip / ecs_client_mask: 仅 aliapi 私享接口等场景使用\n\n")

	keys := make([]string, 0, len(cfg))
	for key := range cfg {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		buf.WriteString("{}\n")
		return writeTextFileAtomically(path, buf.Bytes())
	}

	for i, key := range keys {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(key)
		buf.WriteString(":\n")

		items := cloneUpstreamList(cfg[key])
		if len(items) == 0 {
			buf.WriteString("  []\n")
			continue
		}

		body, err := yaml.Marshal(items)
		if err != nil {
			return fmt.Errorf("encode upstream group %s: %w", key, err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			buf.WriteString("  ")
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}

	return writeTextFileAtomically(path, buf.Bytes())
}

func writeTextFileAtomically(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir for %s: %w", path, err)
	}
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmpFile.Write(content); err != nil {
		cleanup()
		return fmt.Errorf("write temp file %s: %w", path, err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file %s: %w", path, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file %s: %w", path, err)
	}
	return nil
}
