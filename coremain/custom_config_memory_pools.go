package coremain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func memoryPoolsConfigPath() string {
	return filepath.Join(customConfigDirPath(), memoryPoolsConfigFilename)
}

func memoryPoolsConfigPathForBaseDir(baseDir string) string {
	return filepath.Join(customConfigDirPathForBaseDir(baseDir), memoryPoolsConfigFilename)
}

func MemoryPoolsConfigPath() string {
	return memoryPoolsConfigPath()
}

func LoadMemoryPoolPoliciesFromCustomConfig() (map[string]DomainPoolPolicy, bool, error) {
	return LoadMemoryPoolPoliciesFromCustomConfigForBaseDir(MainConfigBaseDir)
}

func LoadMemoryPoolPoliciesFromCustomConfigForBaseDir(baseDir string) (map[string]DomainPoolPolicy, bool, error) {
	path := memoryPoolsConfigPathForBaseDir(baseDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultDomainPoolPolicyMap(), false, nil
		}
		return nil, false, fmt.Errorf("read memory pools config file: %w", err)
	}

	var cfg map[string]domainPoolPolicyFile
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, false, fmt.Errorf("decode memory pools config file %s: %w", path, err)
	}
	normalized, err := normalizeDomainPoolPolicyMap(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("normalize memory pools config file %s: %w", path, err)
	}
	return normalized, true, nil
}

func SaveMemoryPoolPoliciesToCustomConfig(values map[string]DomainPoolPolicy) error {
	return saveMemoryPoolPoliciesToCustomConfigAtPath(memoryPoolsConfigPath(), values)
}

func SaveMemoryPoolPoliciesToCustomConfigForBaseDir(baseDir string, values map[string]DomainPoolPolicy) error {
	return saveMemoryPoolPoliciesToCustomConfigAtPath(memoryPoolsConfigPathForBaseDir(baseDir), values)
}

func saveMemoryPoolPoliciesToCustomConfigAtPath(path string, values map[string]DomainPoolPolicy) error {
	normalized, err := normalizeDomainPoolPoliciesForSave(values)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	writeMemoryPoolConfigHeader(&buf)
	for i, tag := range orderedDomainPoolPolicyKeys(normalized) {
		if i > 0 {
			buf.WriteByte('\n')
		}
		if err := appendMemoryPoolPolicyBlock(&buf, tag, normalized[tag]); err != nil {
			return err
		}
	}
	return writeTextFileAtomically(path, buf.Bytes())
}

func normalizeDomainPoolPoliciesForSave(values map[string]DomainPoolPolicy) (map[string]DomainPoolPolicy, error) {
	if values == nil {
		values = defaultDomainPoolPolicyMap()
	}
	normalized := defaultDomainPoolPolicyMap()
	for tag, policy := range values {
		cleanTag := strings.TrimSpace(tag)
		if cleanTag == "" {
			return nil, fmt.Errorf("memory pool tag is empty")
		}
		next := policy
		if next.Kind == "" {
			next.Kind = defaultDomainPoolPolicy(cleanTag).Kind
		}
		if err := validateDomainPoolPolicy(cleanTag, &next); err != nil {
			return nil, err
		}
		normalized[cleanTag] = next
	}
	return normalized, nil
}

func writeMemoryPoolConfigHeader(buf *bytes.Buffer) {
	buf.WriteString("# 用户自定义域名记忆池/统计池策略\n")
	buf.WriteString("#\n")
	buf.WriteString("# 这个文件是运行态域名池的长期策略真源。\n")
	buf.WriteString("# - key 必须是池插件 tag，例如 top_domains / my_realiplist。\n")
	buf.WriteString("# - 前端或 API 修改应直接写这个文件，并触发对应插件热重载。\n")
	buf.WriteString("# - 你也可以手工修改这个文件，然后重启 mosdns 生效。\n")
	buf.WriteString("# - SQLite 只保存运行态累计、任务态和快照，不保存这类长期策略。\n")
	buf.WriteString("#\n")
	buf.WriteString("# 字段说明：\n")
	buf.WriteString("# - kind: memory / stats。memory 表示会产生规则候选；stats 表示只做排行统计。\n")
	buf.WriteString("# - publish_to: 目标 provider tag，只做拓扑描述；实际消费关系由 generated_from 建立。\n")
	buf.WriteString("# - requery_tag: 需要把脏域名推给哪个 requery 插件。\n")
	buf.WriteString("# - promote_after: memory 池里某个域名至少出现多少次后才进入规则候选。\n")
	buf.WriteString("# - track_qtype: 是否区分 A/AAAA 观察结果。\n")
	buf.WriteString("# - track_flags: 是否区分 AD/CD/DO 这类查询标志位变体。\n")
	buf.WriteString("# - max_domains: 该池最多保留多少个裸域名。\n")
	buf.WriteString("# - max_variants_per_domain: 每个裸域名最多保留多少个 qtype/flags 变体。\n")
	buf.WriteString("# - eviction_policy: 超出容量时如何淘汰；当前支持 lru / lfu。\n")
	buf.WriteString("# - stale_after_minutes: 进入 stale 判定的分钟数。\n")
	buf.WriteString("# - refresh_cooldown_minutes: 触发 requery 后的冷却时间。\n")
	buf.WriteString("# - flush_interval_ms: 运行态批量刷入 SQLite 的周期。\n")
	buf.WriteString("# - publish_debounce_ms: 规则/快照发布防抖周期；0 表示不额外延迟。\n")
	buf.WriteString("# - prune_interval_sec: 后台清理过期项与执行容量淘汰的周期。\n\n")
}

func appendMemoryPoolPolicyBlock(buf *bytes.Buffer, tag string, policy DomainPoolPolicy) error {
	body, err := yaml.Marshal(policy)
	if err != nil {
		return fmt.Errorf("encode memory pool policy %s: %w", tag, err)
	}
	buf.WriteString(tag)
	buf.WriteString(":\n")
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		buf.WriteString("  ")
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return nil
}
