package coremain

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	scdomain "github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
	"gopkg.in/yaml.v3"
)

type shuntAnalyzer struct {
	baseDir     string
	switches    map[string]string
	defaultMark uint8
	defaultTag  string
	rules       []shuntRuleConfig
	providers   map[string]*shuntProvider
	warnings    []string
}

type shuntRuleConfig struct {
	Tag       string `yaml:"tag"`
	Mark      uint8  `yaml:"mark"`
	OutputTag string `yaml:"output_tag"`
}

type shuntProvider struct {
	Tag         string
	PluginType  string
	Matcher     *domain.MixMatcher[struct{}]
	RuleKeys    []string
	SourceFiles []string
}

type shuntExplainResult struct {
	Domain       string              `json:"domain"`
	QType        string              `json:"qtype"`
	Switches     map[string]string   `json:"switches"`
	DefaultMark  uint8               `json:"default_mark"`
	DefaultTag   string              `json:"default_tag"`
	Matches      []shuntMatchedRule  `json:"matches"`
	Decision     shuntDecision       `json:"decision"`
	DecisionPath []shuntDecisionStep `json:"decision_path"`
	Warnings     []string            `json:"warnings,omitempty"`
}

type shuntMatchedRule struct {
	Tag         string   `json:"tag"`
	Mark        uint8    `json:"mark"`
	OutputTag   string   `json:"output_tag"`
	PluginType  string   `json:"plugin_type,omitempty"`
	SourceFiles []string `json:"source_files,omitempty"`
}

type shuntDecision struct {
	Stage   string `json:"stage"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Matched uint8  `json:"matched_mark,omitempty"`
}

type shuntDecisionStep struct {
	Order       int    `json:"order"`
	Stage       string `json:"stage"`
	Mark        uint8  `json:"mark,omitempty"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
	Matched     bool   `json:"matched"`
	RuleTag     string `json:"rule_tag,omitempty"`
	OutputTag   string `json:"output_tag,omitempty"`
	SkippedBy   string `json:"skipped_by,omitempty"`
	DecisionHit bool   `json:"decision_hit"`
}

type shuntConflictEntry struct {
	RuleKey   string              `json:"rule_key"`
	Providers []shuntConflictRule `json:"providers"`
}

type shuntConflictRule struct {
	Tag       string `json:"tag"`
	Mark      uint8  `json:"mark"`
	OutputTag string `json:"output_tag"`
}

type shuntRuleSetFile struct {
	Plugins []shuntPluginConfig `yaml:"plugins"`
}

type shuntPluginConfig struct {
	Tag  string    `yaml:"tag"`
	Type string    `yaml:"type"`
	Args yaml.Node `yaml:"args"`
}

type shuntFileProviderArgs struct {
	Files []string `yaml:"files"`
}

type shuntSRSProviderArgs struct {
	LocalConfig string `yaml:"local_config"`
}

type shuntDomainMapperArgs struct {
	DefaultMark uint8             `yaml:"default_mark"`
	DefaultTag  string            `yaml:"default_tag"`
	Rules       []shuntRuleConfig `yaml:"rules"`
}

type shuntSRSSource struct {
	Name         string `json:"name"`
	Files        string `json:"files"`
	Enabled      bool   `json:"enabled"`
	EnableRegexp bool   `json:"enable_regexp,omitempty"`
}

var (
	shuntBlockRuleRegex = regexp.MustCompile(`^\|\|([\w\.\-\*]+)\^$`)
	shuntAllowRuleRegex = regexp.MustCompile(`^@@\|\|([\w\.\-\*]+)\^$`)
	shuntRegexRuleRegex = regexp.MustCompile(`^\/(.*)\/$`)
	shuntFullMatchRegex = regexp.MustCompile(`^([\w\.\-]+)$`)
)

func newShuntAnalyzer(baseDir string) (*shuntAnalyzer, error) {
	a := &shuntAnalyzer{
		baseDir:   baseDir,
		switches:  make(map[string]string),
		providers: make(map[string]*shuntProvider),
	}
	if err := a.loadSwitches(); err != nil {
		return nil, err
	}
	if err := a.loadRuleSet(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *shuntAnalyzer) Explain(domainName, qtype string) (*shuntExplainResult, error) {
	qtype = strings.ToUpper(strings.TrimSpace(qtype))
	if qtype == "" {
		qtype = "A"
	}

	matches := make([]shuntMatchedRule, 0)
	markSet := make(map[uint8]bool)
	for _, rule := range a.rules {
		provider := a.providers[rule.Tag]
		if provider == nil || provider.Matcher == nil {
			continue
		}
		if _, ok := provider.Matcher.Match(domainName); !ok {
			continue
		}
		matches = append(matches, shuntMatchedRule{
			Tag:         rule.Tag,
			Mark:        rule.Mark,
			OutputTag:   rule.OutputTag,
			PluginType:  provider.PluginType,
			SourceFiles: append([]string(nil), provider.SourceFiles...),
		})
		markSet[rule.Mark] = true
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Mark == matches[j].Mark {
			return matches[i].Tag < matches[j].Tag
		}
		return matches[i].Mark < matches[j].Mark
	})

	decision, path := decideShuntAction(qtype, markSet, a.switches, a.rules)
	result := &shuntExplainResult{
		Domain:       domain.NormalizeDomain(domainName),
		QType:        qtype,
		Switches:     cloneStringMap(a.switches),
		DefaultMark:  a.defaultMark,
		DefaultTag:   a.defaultTag,
		Matches:      matches,
		Decision:     decision,
		DecisionPath: path,
		Warnings:     append([]string(nil), a.warnings...),
	}
	return result, nil
}

func (a *shuntAnalyzer) Conflicts() []shuntConflictEntry {
	byRule := make(map[string][]shuntConflictRule)
	for _, rule := range a.rules {
		provider := a.providers[rule.Tag]
		if provider == nil {
			continue
		}
		for _, ruleKey := range provider.RuleKeys {
			byRule[ruleKey] = append(byRule[ruleKey], shuntConflictRule{
				Tag:       rule.Tag,
				Mark:      rule.Mark,
				OutputTag: rule.OutputTag,
			})
		}
	}

	conflicts := make([]shuntConflictEntry, 0)
	for ruleKey, providers := range byRule {
		if len(providers) < 2 {
			continue
		}
		sort.Slice(providers, func(i, j int) bool {
			if providers[i].Mark == providers[j].Mark {
				return providers[i].Tag < providers[j].Tag
			}
			return providers[i].Mark < providers[j].Mark
		})
		conflicts = append(conflicts, shuntConflictEntry{
			RuleKey:   ruleKey,
			Providers: providers,
		})
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].RuleKey < conflicts[j].RuleKey })
	return conflicts
}

func (a *shuntAnalyzer) loadSwitches() error {
	switchFile := filepath.Join(a.baseDir, "rule", "switches.json")
	data, err := os.ReadFile(switchFile)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		return nil
	default:
		return fmt.Errorf("read switches file: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, &a.switches); err != nil {
		return fmt.Errorf("decode switches file: %w", err)
	}
	return nil
}

func (a *shuntAnalyzer) loadRuleSet() error {
	ruleSetPath := filepath.Join(a.baseDir, "sub_config", "rule_set.yaml")
	data, err := os.ReadFile(ruleSetPath)
	if err != nil {
		return fmt.Errorf("read rule_set.yaml: %w", err)
	}
	var cfg shuntRuleSetFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("decode rule_set.yaml: %w", err)
	}

	for _, plugin := range cfg.Plugins {
		switch plugin.Type {
		case "domain_set_light", "domain_set":
			var args shuntFileProviderArgs
			if err := plugin.Args.Decode(&args); err != nil {
				return fmt.Errorf("decode file provider %s args: %w", plugin.Tag, err)
			}
			provider, err := a.loadTextProvider(plugin.Tag, plugin.Type, args.Files)
			if err != nil {
				return err
			}
			a.providers[plugin.Tag] = provider
		case "sd_set_light", "sd_set":
			var args shuntSRSProviderArgs
			if err := plugin.Args.Decode(&args); err != nil {
				return fmt.Errorf("decode srs provider %s args: %w", plugin.Tag, err)
			}
			provider, err := a.loadSRSProvider(plugin.Tag, plugin.Type, args.LocalConfig)
			if err != nil {
				return err
			}
			a.providers[plugin.Tag] = provider
		case "domain_mapper":
			if plugin.Tag != "unified_matcher1" {
				continue
			}
			var args shuntDomainMapperArgs
			if err := plugin.Args.Decode(&args); err != nil {
				return fmt.Errorf("decode unified_matcher1 args: %w", err)
			}
			a.defaultMark = args.DefaultMark
			a.defaultTag = args.DefaultTag
			a.rules = args.Rules
		}
	}
	if _, ok := a.providers["adguard"]; !ok {
		if provider, err := a.loadAdguardProvider(); err == nil && provider != nil {
			a.providers["adguard"] = provider
		} else if err != nil {
			a.warnings = append(a.warnings, err.Error())
		}
	}
	return nil
}

func (a *shuntAnalyzer) loadTextProvider(tag, pluginType string, files []string) (*shuntProvider, error) {
	p := &shuntProvider{
		Tag:         tag,
		PluginType:  pluginType,
		Matcher:     domain.NewMixMatcher[struct{}](),
		SourceFiles: make([]string, 0, len(files)),
	}
	p.Matcher.SetDefaultMatcher(domain.MatcherDomain)
	ruleSet := make(map[string]struct{})
	for _, file := range files {
		path := filepath.Join(a.baseDir, file)
		p.SourceFiles = append(p.SourceFiles, path)
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open provider file %s (%s): %w", tag, path, err)
		}
		if err := domain.LoadFromTextReader(p.Matcher, f, nil); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("load provider file %s (%s): %w", tag, path, err)
		}
		_ = f.Close()
		if err := collectRuleKeysFromTextFile(path, ruleSet); err != nil {
			return nil, err
		}
	}
	p.RuleKeys = mapKeys(ruleSet)
	return p, nil
}

func (a *shuntAnalyzer) loadSRSProvider(tag, pluginType, localConfig string) (*shuntProvider, error) {
	p := &shuntProvider{
		Tag:        tag,
		PluginType: pluginType,
		Matcher:    domain.NewMixMatcher[struct{}](),
	}
	p.Matcher.SetDefaultMatcher(domain.MatcherDomain)

	configPath := filepath.Join(a.baseDir, localConfig)
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		return p, nil
	default:
		return nil, fmt.Errorf("read provider local_config %s: %w", configPath, err)
	}

	var sources []shuntSRSSource
	if err := json.Unmarshal(data, &sources); err != nil {
		return nil, fmt.Errorf("decode provider local_config %s: %w", configPath, err)
	}

	ruleSet := make(map[string]struct{})
	for _, src := range sources {
		if !src.Enabled || strings.TrimSpace(src.Files) == "" {
			continue
		}
		sourcePath := filepath.Join(a.baseDir, src.Files)
		p.SourceFiles = append(p.SourceFiles, sourcePath)
		b, err := os.ReadFile(sourcePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read SRS source %s (%s): %w", tag, sourcePath, err)
		}
		ok, _, _, rules, err := loadRulesFromSRSBytes(b, src.EnableRegexp)
		if err != nil {
			return nil, fmt.Errorf("load SRS source %s (%s): %w", tag, sourcePath, err)
		}
		if !ok {
			continue
		}
		for _, rule := range rules {
			if err := p.Matcher.Add(rule, struct{}{}); err == nil {
				ruleSet[rule] = struct{}{}
			}
		}
	}
	p.RuleKeys = mapKeys(ruleSet)
	return p, nil
}

func (a *shuntAnalyzer) loadAdguardProvider() (*shuntProvider, error) {
	configPath := filepath.Join(a.baseDir, "adguard", "config.json")
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		return nil, nil
	default:
		return nil, fmt.Errorf("read adguard config: %w", err)
	}

	type adguardConfigItem struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	var items []adguardConfigItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode adguard config: %w", err)
	}

	p := &shuntProvider{
		Tag:        "adguard",
		PluginType: "adguard_rule",
		Matcher:    domain.NewMixMatcher[struct{}](),
	}
	p.Matcher.SetDefaultMatcher(domain.MatcherDomain)
	ruleSet := make(map[string]struct{})
	for _, item := range items {
		if !item.Enabled || strings.TrimSpace(item.Name) == "" {
			continue
		}
		path := filepath.Join(a.baseDir, "adguard", item.Name+".txt")
		p.SourceFiles = append(p.SourceFiles, path)
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open adguard rule file %s: %w", path, err)
		}
		rules, err := collectAdguardDenyRules(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("parse adguard rule file %s: %w", path, err)
		}
		for _, rule := range rules {
			if err := p.Matcher.Add(rule, struct{}{}); err == nil {
				ruleSet[rule] = struct{}{}
			}
		}
	}
	p.RuleKeys = mapKeys(ruleSet)
	return p, nil
}

func decideShuntAction(qtype string, marks map[uint8]bool, switches map[string]string, rules []shuntRuleConfig) (shuntDecision, []shuntDecisionStep) {
	blockResponse := switches["block_response"] != "off"
	blockQueryType := switches["block_query_type"] != "off"
	blockIPv6 := switches["block_ipv6"] == "on"
	adBlock := switches["ad_block"] == "on"
	coreMode := strings.TrimSpace(switches["core_mode"])
	if coreMode == "" {
		coreMode = "secure"
	}
	outputTagByMark := make(map[uint8]string)
	tagByMark := make(map[uint8]string)
	for _, rule := range rules {
		if _, ok := outputTagByMark[rule.Mark]; !ok {
			outputTagByMark[rule.Mark] = rule.OutputTag
		}
		if _, ok := tagByMark[rule.Mark]; !ok {
			tagByMark[rule.Mark] = rule.Tag
		}
	}
	path := make([]shuntDecisionStep, 0, 16)
	stepOrder := 0
	appendStep := func(stage string, mark uint8, action, reason string, matched, hit bool) {
		stepOrder++
		path = append(path, shuntDecisionStep{
			Order:       stepOrder,
			Stage:       stage,
			Mark:        mark,
			Action:      action,
			Reason:      reason,
			Matched:     matched,
			RuleTag:     tagByMark[mark],
			OutputTag:   outputTagByMark[mark],
			DecisionHit: hit,
		})
	}

	switch qtype {
	case "SOA", "PTR", "HTTPS", "TYPE65":
		appendStep("precheck", 0, "reject", "检查特殊查询类型是否拦截", blockQueryType, blockQueryType)
		if blockQueryType {
			return shuntDecision{Stage: "precheck", Action: "reject", Reason: "block_query_type:on for special qtype"}, path
		}
	case "AAAA":
		appendStep("precheck", 28, "reject", "检查 IPv6 是否被 block_ipv6:on 拦截", blockIPv6, blockIPv6)
		if blockIPv6 {
			return shuntDecision{Stage: "precheck", Action: "reject", Reason: "block_ipv6:on", Matched: 28}, path
		}
	}

	appendStep("precheck", 1, "reject", "blocklist + block_response:on", marks[1] && blockResponse, marks[1] && blockResponse)
	if marks[1] && blockResponse {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "blocklist + block_response:on", Matched: 1}, path
	}
	appendStep("precheck", 2, "reject", "记忆无V4 + block_response:on", qtype == "A" && marks[2] && blockResponse, qtype == "A" && marks[2] && blockResponse)
	if qtype == "A" && marks[2] && blockResponse {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "记忆无V4 + block_response:on", Matched: 2}, path
	}
	appendStep("precheck", 3, "reject", "记忆无V6 + block_response:on", qtype == "AAAA" && marks[3] && blockResponse, qtype == "AAAA" && marks[3] && blockResponse)
	if qtype == "AAAA" && marks[3] && blockResponse {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "记忆无V6 + block_response:on", Matched: 3}, path
	}
	appendStep("precheck", 5, "reject", "广告屏蔽 + ad_block:on", marks[5] && adBlock, marks[5] && adBlock)
	if marks[5] && adBlock {
		return shuntDecision{Stage: "precheck", Action: "reject", Reason: "广告屏蔽 + ad_block:on", Matched: 5}, path
	}
	appendStep("precheck", 6, "domestic", "DDNS 域名直接走国内上游", marks[6], marks[6])
	if marks[6] {
		return shuntDecision{Stage: "precheck", Action: "domestic", Reason: "DDNS 域名直接走国内上游", Matched: 6}, path
	}

	for _, step := range []struct {
		Mark   uint8
		Action string
		Reason string
	}{
		{7, "sequence_fakeip", "灰名单优先走 fakeip/代理"},
		{8, "sequence_local", "白名单走国内直连链路"},
		{11, "sequence_local_divert", "记忆直连走国内链路"},
		{12, "sequence_fakeip", "记忆代理走 fakeip/代理"},
		{13, "sequence_local", "订阅直连补充走国内链路"},
		{14, "sequence_fakeip_addlist", "订阅代理走 fakeip/代理并加入清单"},
		{15, "sequence_fakeip_addlist", "订阅代理补充走 fakeip/代理并加入清单"},
		{16, "sequence_local", "订阅直连走国内链路"},
	} {
		appendStep("sequence_known_domain", step.Mark, step.Action, step.Reason, marks[step.Mark], marks[step.Mark])
		if marks[step.Mark] {
			return shuntDecision{Stage: "sequence_known_domain", Action: step.Action, Reason: step.Reason, Matched: step.Mark}, path
		}
	}
	path = append(path, shuntDecisionStep{
		Order:       len(path) + 1,
		Stage:       "sequence_fallback",
		Action:      "not_in_list_" + coreMode + "_" + strings.ToLower(qtype),
		Reason:      "未命中 known-domain 优先级，进入列表外解析逻辑",
		Matched:     true,
		DecisionHit: true,
	})
	return shuntDecision{
		Stage:  "sequence_fallback",
		Action: "not_in_list_" + coreMode + "_" + strings.ToLower(qtype),
		Reason: "未命中 known-domain 优先级，进入列表外解析逻辑",
	}, path
}

func collectRuleKeysFromTextFile(path string, ruleSet map[string]struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open text rule file %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		ruleSet[normalizeConflictRuleKey(line)] = struct{}{}
	}
	return scanner.Err()
}

func normalizeConflictRuleKey(rule string) string {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return ""
	}
	lower := strings.ToLower(rule)
	for _, prefix := range []string{"full:", "domain:", "keyword:", "regexp:"} {
		if strings.HasPrefix(lower, prefix) {
			if prefix == "regexp:" {
				return prefix + strings.TrimSpace(rule[len(prefix):])
			}
			return prefix + domain.NormalizeDomain(strings.TrimSpace(rule[len(prefix):]))
		}
	}
	return "domain:" + domain.NormalizeDomain(rule)
}

func collectAdguardDenyRules(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	rules := make([]string, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, "0123456789") && (strings.Contains(line, "127.0.0.1") || strings.Contains(line, "0.0.0.0") || strings.Contains(line, "::")) {
			parts := strings.Fields(line)
			if len(parts) > 1 {
				continue
			}
		}
		if strings.Contains(line, "#?#") || strings.Contains(line, "##") || strings.Contains(line, "$$") {
			continue
		}
		switch {
		case shuntAllowRuleRegex.MatchString(line):
			continue
		case shuntBlockRuleRegex.MatchString(line):
			m := shuntBlockRuleRegex.FindStringSubmatch(line)
			rules = append(rules, convertAdguardRule(m[1]))
		case shuntRegexRuleRegex.MatchString(line):
			m := shuntRegexRuleRegex.FindStringSubmatch(line)
			rules = append(rules, "regexp:"+m[1])
		case shuntFullMatchRegex.MatchString(line):
			m := shuntFullMatchRegex.FindStringSubmatch(line)
			rules = append(rules, "full:"+domain.NormalizeDomain(m[1]))
		}
	}
	return rules, scanner.Err()
}

func convertAdguardRule(domainStr string) string {
	domainStr = strings.TrimPrefix(domainStr, "*.")
	domainStr = strings.TrimPrefix(domainStr, ".")
	if strings.Contains(domainStr, "*") {
		regexStr := strings.ReplaceAll(domainStr, ".", `\.`)
		regexStr = strings.ReplaceAll(regexStr, "*", ".*")
		return "regexp:" + regexStr
	}
	return "domain:" + domain.NormalizeDomain(domainStr)
}

func loadRulesFromSRSBytes(b []byte, enableRegexp bool) (ok bool, count int, lastRule string, rules []string, err error) {
	ruleSet := make(map[string]struct{})
	collector := &srsRuleCollector{rules: ruleSet}

	r := bytes.NewReader(b)
	var mb [3]byte
	if _, err = io.ReadFull(r, mb[:]); err != nil || mb != magicBytes {
		return false, 0, "", nil, nil
	}
	var version uint8
	if err = binary.Read(r, binary.BigEndian, &version); err != nil || version > ruleSetVersionCurrent {
		return false, 0, "", nil, nil
	}
	zr, err := zlib.NewReader(r)
	if err != nil {
		return false, 0, "", nil, nil
	}
	defer zr.Close()
	br := bufio.NewReader(zr)
	length, err := binary.ReadUvarint(br)
	if err != nil {
		return false, 0, "", nil, nil
	}
	for i := uint64(0); i < length; i++ {
		count += readSRSRuleCompat(br, collector, &lastRule, enableRegexp)
	}
	return true, count, lastRule, mapKeys(ruleSet), nil
}

type srsRuleCollector struct {
	rules map[string]struct{}
}

func (c *srsRuleCollector) Add(s string, _ struct{}) error {
	c.rules[normalizeConflictRuleKey(s)] = struct{}{}
	return nil
}

func readSRSRuleCompat(r *bufio.Reader, m interface{ Add(string, struct{}) error }, last *string, enableRegexp bool) int {
	ct := 0
	mode, err := r.ReadByte()
	if err != nil {
		return 0
	}
	switch mode {
	case 0:
		ct += readSRSDefaultRuleCompat(r, m, last, enableRegexp)
	case 1:
		_, _ = r.ReadByte()
		n, _ := binary.ReadUvarint(r)
		for i := uint64(0); i < n; i++ {
			ct += readSRSRuleCompat(r, m, last, enableRegexp)
		}
		_, _ = r.ReadByte()
	}
	return ct
}

func readSRSDefaultRuleCompat(r *bufio.Reader, m interface{ Add(string, struct{}) error }, last *string, enableRegexp bool) int {
	count := 0
	for {
		item, err := r.ReadByte()
		if err != nil {
			break
		}
		switch item {
		case ruleItemDomain:
			matcher, err := scdomain.ReadMatcher(r)
			if err != nil {
				return count
			}
			doms, suffix := matcher.Dump()
			for _, d := range doms {
				*last = "full:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
				}
			}
			for _, d := range suffix {
				*last = "domain:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
				}
			}
		case ruleItemDomainKeyword:
			sl, _ := varbin.ReadValue[[]string](r, binary.BigEndian)
			for _, d := range sl {
				*last = "keyword:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
				}
			}
		case ruleItemDomainRegex:
			sl, _ := varbin.ReadValue[[]string](r, binary.BigEndian)
			if enableRegexp {
				for _, d := range sl {
					*last = "regexp:" + d
					if m.Add(*last, struct{}{}) == nil {
						count++
					}
				}
			}
		case ruleItemFinal:
			return count
		default:
			return count
		}
	}
	return count
}

func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func mapKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const (
	ruleItemFinal         = 0
	ruleItemDomain        = 1
	ruleItemDomainKeyword = 2
	ruleItemDomainRegex   = 3
)

var magicBytes = [3]byte{0x53, 0x52, 0x53}

const (
	ruleSetVersion1 = 1 + iota
	ruleSetVersion2
	ruleSetVersion3
)

const ruleSetVersionCurrent = ruleSetVersion3
