package domain_mapper

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	domainmatcher "github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
)

var _ coremain.HotRuleConsumer = (*DomainMapper)(nil)

func (dm *DomainMapper) AddHotRules(providerTag string, rules []string) error {
	return dm.updateHotRules(providerTag, rules, false)
}

func (dm *DomainMapper) ReplaceHotRules(providerTag string, rules []string) error {
	return dm.updateHotRules(providerTag, rules, true)
}

func (dm *DomainMapper) updateHotRules(providerTag string, rules []string, replace bool) error {
	providerTag = strings.TrimSpace(providerTag)
	if providerTag == "" {
		return nil
	}
	normalized := normalizeHotRules(rules)

	dm.hotMu.Lock()
	defer dm.hotMu.Unlock()

	if dm.hotRules == nil {
		dm.hotRules = make(map[string]map[string]struct{})
	}
	if replace {
		dm.hotRules[providerTag] = makeHotRuleSet(normalized)
	} else {
		set := dm.hotRules[providerTag]
		if set == nil {
			set = make(map[string]struct{}, len(normalized))
			dm.hotRules[providerTag] = set
		}
		for _, rule := range normalized {
			set[rule] = struct{}{}
		}
	}
	dm.rebuildHotLookupLocked()
	return nil
}

func (dm *DomainMapper) replaceHotRulesFromProviders(providers map[string]data_provider.RuleExporter) error {
	next := make(map[string]map[string]struct{}, len(providers))
	for tag, provider := range providers {
		rules, ok, err := snapshotProviderHotRules(provider)
		if err != nil {
			return fmt.Errorf("snapshot provider %s hot rules: %w", tag, err)
		}
		if !ok {
			continue
		}
		next[tag] = makeHotRuleSet(normalizeHotRules(rules))
	}
	dm.hotMu.Lock()
	dm.hotRules = next
	dm.rebuildHotLookupLocked()
	dm.hotMu.Unlock()
	return nil
}

func (dm *DomainMapper) refreshProviderHotRules(tag string, provider data_provider.RuleExporter) error {
	rules, ok, err := snapshotProviderHotRules(provider)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return dm.ReplaceHotRules(tag, rules)
}

func snapshotProviderHotRules(provider data_provider.RuleExporter) ([]string, bool, error) {
	snapshotProvider, ok := provider.(coremain.HotRuleSnapshotProvider)
	if !ok || snapshotProvider == nil {
		return nil, false, nil
	}
	rules, err := snapshotProvider.SnapshotHotRules()
	if err != nil {
		return nil, false, err
	}
	return rules, true, nil
}

func (dm *DomainMapper) rebuildHotLookupLocked() {
	lookup := make(map[string]*MatchResult)
	for providerTag, rules := range dm.hotRules {
		result := dm.hotResultForProvider(providerTag)
		if result == nil {
			continue
		}
		for rule := range rules {
			lookup[rule] = mergeMatchResult(lookup[rule], result)
		}
	}
	dm.hotLookup.Store(lookup)
}

func (dm *DomainMapper) hotResultForProvider(providerTag string) *MatchResult {
	marks := make([]uint8, 0, 4)
	seenMarks := make(map[uint8]struct{}, 4)
	tags := make([]string, 0, 4)
	seenTags := make(map[string]struct{}, 4)

	for _, rule := range dm.ruleConfigs {
		if strings.TrimSpace(rule.Tag) != providerTag {
			continue
		}
		if rule.Mark > 0 {
			if _, exists := seenMarks[rule.Mark]; !exists {
				seenMarks[rule.Mark] = struct{}{}
				marks = append(marks, rule.Mark)
			}
		}
		tag := strings.TrimSpace(rule.OutputTag)
		if tag == "" {
			tag = strings.TrimSpace(rule.Tag)
		}
		if tag == "" {
			continue
		}
		if _, exists := seenTags[tag]; exists {
			continue
		}
		seenTags[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(marks) == 0 && len(tags) == 0 {
		return nil
	}
	slices.Sort(marks)
	return &MatchResult{
		Marks:      marks,
		JoinedTags: strings.Join(tags, "|"),
	}
}

func (dm *DomainMapper) match(qname string) (*MatchResult, bool) {
	matcher := dm.matcher.Load().(*domainmatcher.MixMatcher[*MatchResult])
	mainResult, mainOK := matcher.Match(qname)
	hotResult, hotOK := dm.matchHot(qname)
	result := mergeMatchResult(mainResult, hotResult)
	return result, result != nil && (mainOK || hotOK)
}

func (dm *DomainMapper) matchHot(qname string) (*MatchResult, bool) {
	lookup := dm.hotLookup.Load().(map[string]*MatchResult)
	result, ok := lookup[ensureFQDN(qname)]
	return result, ok && result != nil
}

func mergeMatchResult(mainResult, hotResult *MatchResult) *MatchResult {
	if mainResult == nil {
		return cloneMatchResult(hotResult)
	}
	if hotResult == nil {
		return cloneMatchResult(mainResult)
	}
	merged := cloneMatchResult(mainResult)
	for _, mark := range hotResult.Marks {
		if !slices.Contains(merged.Marks, mark) {
			merged.Marks = append(merged.Marks, mark)
		}
	}
	slices.Sort(merged.Marks)
	merged.JoinedTags = mergeTagStrings(merged.JoinedTags, hotResult.JoinedTags)
	return merged
}

func cloneMatchResult(result *MatchResult) *MatchResult {
	if result == nil {
		return nil
	}
	return &MatchResult{
		Marks:      append([]uint8(nil), result.Marks...),
		JoinedTags: result.JoinedTags,
	}
}

func normalizeHotRules(rules []string) []string {
	normalized := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		key := normalizeHotRule(rule)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized
}

func normalizeHotRule(rule string) string {
	rule = normalizeRuleKey(rule)
	if !strings.HasPrefix(rule, "full:") {
		return ""
	}
	return ensureFQDN(strings.TrimPrefix(rule, "full:"))
}

func makeHotRuleSet(rules []string) map[string]struct{} {
	set := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		set[rule] = struct{}{}
	}
	return set
}

func ensureFQDN(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = domainmatcher.NormalizeDomain(strings.TrimSuffix(name, "."))
	if name == "" {
		return ""
	}
	return name + "."
}
