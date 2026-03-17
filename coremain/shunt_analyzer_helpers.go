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

func newMatcherProvider(
	tag string,
	pluginType string,
	matcher *domain.MixMatcher[struct{}],
	ruleKeys []string,
	sourceFiles []string,
) *shuntProvider {
	return &shuntProvider{
		Tag:         tag,
		PluginType:  pluginType,
		RuleKeys:    ruleKeys,
		SourceFiles: sourceFiles,
		match: func(domainName string) bool {
			_, ok := matcher.Match(domainName)
			return ok
		},
	}
}

func addRulesToMatcher(
	matcher *domain.MixMatcher[struct{}],
	rules []string,
	ruleSet map[string]struct{},
) {
	for _, rule := range rules {
		if err := matcher.Add(rule, struct{}{}); err == nil {
			ruleSet[normalizeConflictRuleKey(rule)] = struct{}{}
		}
	}
}

func loadRulesFromLocalDomainFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return rulesource.ParseDomainBytes(formatFromPath(path), data)
}

func mergeAdguardRulesForAnalyzer(
	source rulesource.Source,
	data []byte,
	allowMatcher *domain.MixMatcher[struct{}],
	denyMatcher *domain.MixMatcher[struct{}],
	ruleSet map[string]struct{},
) error {
	if source.Behavior == rulesource.BehaviorAdguard {
		result, err := rulesource.ParseAdguardBytes(source.Format, data)
		if err != nil {
			return fmt.Errorf("parse adguard source %s: %w", source.ID, err)
		}
		for _, rule := range result.Allow {
			_ = allowMatcher.Add(rule, struct{}{})
		}
		for _, rule := range result.Deny {
			if err := denyMatcher.Add(rule, struct{}{}); err == nil {
				ruleSet[normalizeConflictRuleKey(rule)] = struct{}{}
			}
		}
		return nil
	}
	rules, err := rulesource.ParseDomainBytes(source.Format, data)
	if err != nil {
		return fmt.Errorf("parse adguard domain source %s: %w", source.ID, err)
	}
	addRulesToMatcher(denyMatcher, rules, ruleSet)
	return nil
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

func formatFromPath(path string) rulesource.Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".list":
		return rulesource.FormatList
	case ".rules":
		return rulesource.FormatRules
	case ".json":
		return rulesource.FormatJSON
	case ".yaml", ".yml":
		return rulesource.FormatYAML
	case ".srs":
		return rulesource.FormatSRS
	case ".mrs":
		return rulesource.FormatMRS
	default:
		return rulesource.FormatTXT
	}
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
