package domain_mapper

import (
	"slices"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
)

type providerResultBuilder struct {
	marks []uint8
	tags  []string

	seenMarks map[uint8]struct{}
	seenTags  map[string]struct{}
}

func buildProviderResults(ruleConfigs []RuleConfig) map[string]*MatchResult {
	builders := make(map[string]*providerResultBuilder)
	for _, rule := range ruleConfigs {
		providerTag := strings.TrimSpace(rule.Tag)
		if providerTag == "" {
			continue
		}
		builder := builders[providerTag]
		if builder == nil {
			builder = &providerResultBuilder{
				seenMarks: make(map[uint8]struct{}, 4),
				seenTags:  make(map[string]struct{}, 4),
			}
			builders[providerTag] = builder
		}
		if rule.Mark > 0 {
			if _, ok := builder.seenMarks[rule.Mark]; !ok {
				builder.seenMarks[rule.Mark] = struct{}{}
				builder.marks = append(builder.marks, rule.Mark)
			}
		}
		tag := strings.TrimSpace(rule.OutputTag)
		if tag == "" {
			tag = providerTag
		}
		if tag == "" {
			continue
		}
		if _, ok := builder.seenTags[tag]; ok {
			continue
		}
		builder.seenTags[tag] = struct{}{}
		builder.tags = append(builder.tags, tag)
	}

	results := make(map[string]*MatchResult, len(builders))
	for providerTag, builder := range builders {
		if len(builder.marks) == 0 && len(builder.tags) == 0 {
			continue
		}
		slices.Sort(builder.marks)
		results[providerTag] = &MatchResult{
			Marks:      append([]uint8(nil), builder.marks...),
			JoinedTags: strings.Join(builder.tags, "|"),
		}
	}
	return results
}

func buildProviderValidators(providers map[string]data_provider.RuleExporter) map[string]coremain.HotRuleRuntimeValidator {
	validators := make(map[string]coremain.HotRuleRuntimeValidator, len(providers))
	for tag, provider := range providers {
		validator, ok := provider.(coremain.HotRuleRuntimeValidator)
		if !ok || validator == nil {
			continue
		}
		validators[tag] = validator
	}
	return validators
}

func appendUniqueMatchSource(dst []matchSource, src matchSource) []matchSource {
	if src.result == nil {
		return dst
	}
	src.providerTag = strings.TrimSpace(src.providerTag)
	if src.providerTag == "" {
		return dst
	}
	for _, existing := range dst {
		if existing.providerTag == src.providerTag {
			return dst
		}
	}
	return append(dst, src)
}

func appendMatchSources(dst []matchSource, src []matchSource) []matchSource {
	for _, source := range src {
		dst = appendUniqueMatchSource(dst, source)
	}
	return dst
}

func cloneMatchSources(src []matchSource) []matchSource {
	if len(src) == 0 {
		return nil
	}
	return append([]matchSource(nil), src...)
}

func (dm *DomainMapper) allowRuleSource(providerTag, qname string, now time.Time) bool {
	validators, _ := dm.validators.Load().(map[string]coremain.HotRuleRuntimeValidator)
	validator := validators[strings.TrimSpace(providerTag)]
	if validator == nil {
		return true
	}
	domain := strings.TrimSuffix(ensureFQDN(qname), ".")
	if domain == "" {
		return false
	}
	return validator.AllowHotRule(domain, now)
}

func (dm *DomainMapper) resolveSources(sources []matchSource, qname string, now time.Time) *MatchResult {
	var merged *MatchResult
	for _, source := range sources {
		if !dm.allowRuleSource(source.providerTag, qname, now) {
			continue
		}
		merged = mergeMatchResult(merged, source.result)
	}
	return merged
}

func (dm *DomainMapper) resolveCompiledMatch(compiled *compiledMatch, qname string, now time.Time) *MatchResult {
	if compiled == nil {
		return nil
	}
	return dm.resolveSources(compiled.sources, qname, now)
}
