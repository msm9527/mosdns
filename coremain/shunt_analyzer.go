package coremain

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
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
	RuleKeys    []string
	SourceFiles []string
	match       func(string) bool
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

type shuntConflictEntry struct {
	RuleKey   string              `json:"rule_key"`
	Providers []shuntConflictRule `json:"providers"`
}

type shuntConflictRule struct {
	Tag       string `json:"tag"`
	Mark      uint8  `json:"mark"`
	OutputTag string `json:"output_tag"`
}

type shuntDomainMapperArgs struct {
	DefaultMark uint8             `yaml:"default_mark"`
	DefaultTag  string            `yaml:"default_tag"`
	Rules       []shuntRuleConfig `yaml:"rules"`
}

type shuntDomainProviderArgs struct {
	Files         []string `yaml:"files"`
	GeneratedFrom string   `yaml:"generated_from"`
}

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
		if provider == nil || !provider.matchDomain(domainName) {
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
	return &shuntExplainResult{
		Domain:       domain.NormalizeDomain(domainName),
		QType:        qtype,
		Switches:     cloneStringMap(a.switches),
		DefaultMark:  a.defaultMark,
		DefaultTag:   a.defaultTag,
		Matches:      matches,
		Decision:     decision,
		DecisionPath: path,
		Warnings:     append([]string(nil), a.warnings...),
	}, nil
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
		conflicts = append(conflicts, shuntConflictEntry{RuleKey: ruleKey, Providers: providers})
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].RuleKey < conflicts[j].RuleKey })
	return conflicts
}

func (a *shuntAnalyzer) loadSwitches() error {
	values, _, err := loadSwitchesFromCustomConfigForBaseDir(a.baseDir)
	if err != nil {
		return fmt.Errorf("load switches config: %w", err)
	}
	a.switches = values
	return nil
}

func (a *shuntAnalyzer) loadRuleSet() error {
	policies, err := loadDataSourcePolicies(a.baseDir)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if err := a.loadPolicy(policy); err != nil {
			return err
		}
	}
	return nil
}

func (a *shuntAnalyzer) loadPolicy(policy dataSourcePolicy) error {
	switch policy.Type {
	case "domain_set_light", "domain_set":
		var args shuntDomainProviderArgs
		if err := policy.Args.Decode(&args); err != nil {
			return fmt.Errorf("decode domain provider %s args: %w", policy.Name, err)
		}
		provider, err := a.loadStaticDomainProvider(policy.Name, policy.Type, args)
		if err != nil {
			return err
		}
		a.providers[policy.Name] = provider
	case "sd_set_light", "sd_set":
		var args diversionPolicyArgs
		if err := policy.Args.Decode(&args); err != nil {
			return fmt.Errorf("decode diversion provider %s args: %w", policy.Name, err)
		}
		provider, err := a.loadBoundDomainProvider(policy.Name, policy.Type, args)
		if err != nil {
			return err
		}
		a.providers[policy.Name] = provider
	case "adguard_rule":
		var args adguardPolicyArgs
		if err := policy.Args.Decode(&args); err != nil {
			return fmt.Errorf("decode adguard provider %s args: %w", policy.Name, err)
		}
		provider, err := a.loadAdguardProvider(policy.Name, args)
		if err != nil {
			return err
		}
		a.providers[policy.Name] = provider
	case "domain_mapper":
		if policy.Name != "unified_matcher1" {
			return nil
		}
		var args shuntDomainMapperArgs
		if err := policy.Args.Decode(&args); err != nil {
			return fmt.Errorf("decode unified_matcher1 args: %w", err)
		}
		a.defaultMark = args.DefaultMark
		a.defaultTag = args.DefaultTag
		a.rules = args.Rules
	}
	return nil
}

func (a *shuntAnalyzer) loadStaticDomainProvider(
	tag string,
	pluginType string,
	args shuntDomainProviderArgs,
) (*shuntProvider, error) {
	ruleSet := make(map[string]struct{})
	sourceFiles := make([]string, 0, len(args.Files)+1)
	matcher := domain.NewDomainMixMatcher()
	for _, file := range args.Files {
		path := filepath.Join(a.baseDir, file)
		rules, err := loadRulesFromLocalDomainFile(path)
		if err != nil {
			return nil, fmt.Errorf("load provider file %s (%s): %w", tag, path, err)
		}
		sourceFiles = append(sourceFiles, path)
		addRulesToMatcher(matcher, rules, ruleSet)
	}
	if strings.TrimSpace(args.GeneratedFrom) != "" {
		rules, sourceRef, err := a.loadGeneratedDomainRules(args.GeneratedFrom)
		if err != nil {
			return nil, err
		}
		if sourceRef != "" {
			sourceFiles = append(sourceFiles, sourceRef)
		}
		addRulesToMatcher(matcher, rules, ruleSet)
	}
	return newMatcherProvider(tag, pluginType, matcher, mapKeys(ruleSet), sourceFiles), nil
}

func (a *shuntAnalyzer) loadBoundDomainProvider(
	tag string,
	pluginType string,
	args diversionPolicyArgs,
) (*shuntProvider, error) {
	sources, err := LoadRuleSourcesByBindingForBaseDir(
		a.baseDir,
		args.ConfigFile,
		rulesource.ScopeDiversion,
		strings.TrimSpace(args.BindTo),
	)
	if err != nil {
		return nil, err
	}
	matcher := domain.NewDomainMixMatcher()
	ruleSet := make(map[string]struct{})
	sourceFiles := make([]string, 0, len(sources))
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		data, path, err := a.readRuleSourceFile(rulesource.ScopeDiversion, source)
		if err != nil {
			return nil, err
		}
		rules, err := rulesource.ParseDomainBytes(source.Format, data)
		if err != nil {
			return nil, fmt.Errorf("parse diversion source %s: %w", source.ID, err)
		}
		sourceFiles = append(sourceFiles, path)
		addRulesToMatcher(matcher, rules, ruleSet)
	}
	return newMatcherProvider(tag, pluginType, matcher, mapKeys(ruleSet), sourceFiles), nil
}

func (a *shuntAnalyzer) loadAdguardProvider(tag string, args adguardPolicyArgs) (*shuntProvider, error) {
	cfg, _, err := rulesource.LoadConfig(resolvePolicyConfigPath(a.baseDir, args.ConfigFile), rulesource.ScopeAdguard)
	if err != nil {
		return nil, err
	}
	allowMatcher := domain.NewDomainMixMatcher()
	denyMatcher := domain.NewDomainMixMatcher()
	ruleSet := make(map[string]struct{})
	sourceFiles := make([]string, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if !source.Enabled {
			continue
		}
		data, path, err := a.readRuleSourceFile(rulesource.ScopeAdguard, source)
		if err != nil {
			return nil, err
		}
		sourceFiles = append(sourceFiles, path)
		if err := mergeAdguardRulesForAnalyzer(source, data, allowMatcher, denyMatcher, ruleSet); err != nil {
			return nil, err
		}
	}
	return &shuntProvider{
		Tag:         tag,
		PluginType:  "adguard_rule",
		RuleKeys:    mapKeys(ruleSet),
		SourceFiles: sourceFiles,
		match: func(domainName string) bool {
			if _, ok := allowMatcher.Match(domainName); ok {
				return false
			}
			_, ok := denyMatcher.Match(domainName)
			return ok
		},
	}, nil
}

func (a *shuntAnalyzer) loadGeneratedDomainRules(poolTag string) ([]string, string, error) {
	state, ok, err := LoadDomainPoolStateFromPath(runtimeStateDBPathForBaseDir(a.baseDir), strings.TrimSpace(poolTag))
	if err != nil {
		return nil, "", fmt.Errorf("load generated domain rules %s: %w", poolTag, err)
	}
	if !ok {
		return nil, "", nil
	}
	rules := make([]string, 0, len(state.Domains))
	for _, item := range state.Domains {
		if item.Promoted {
			rules = append(rules, "full:"+domain.NormalizeDomain(item.Domain))
		}
	}
	return rules, "db://domain_pool/" + strings.TrimSpace(poolTag), nil
}

func (a *shuntAnalyzer) readRuleSourceFile(
	scope rulesource.Scope,
	source rulesource.Source,
) ([]byte, string, error) {
	path, err := rulesource.ResolveLocalPath(a.baseDir, scope, source)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read rule source %s: %w", path, err)
	}
	return data, path, nil
}

func (p *shuntProvider) matchDomain(domainName string) bool {
	if p == nil || p.match == nil {
		return false
	}
	return p.match(domainName)
}
